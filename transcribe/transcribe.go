package transcribe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ogpourya/vox/config"
)

const (
	maxHTTPAttempts = 3
	// maxResponseBytes caps how much of an STT response we buffer.
	maxResponseBytes = 1 << 20
	// maxErrBody is how much of an error response surfaces in Error().
	maxErrBody = 500
)

// apiURLFormat builds the STT request URL from (lang, key). A variable
// rather than the config const directly so tests can redirect it.
var apiURLFormat = config.GoogleSpeechAPIURL

// APIError is a non-2xx response from the transcription endpoint.
type APIError struct {
	StatusCode int
	Body       string
	// RetryAfter honors the server's Retry-After header (0 if absent).
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > maxErrBody {
		body = body[:maxErrBody] + "…"
	}
	return fmt.Sprintf("Google API error %d: %s", e.StatusCode, body)
}

// TransportError is a network-level failure after our internal retries.
type TransportError struct {
	Err error
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// IsRetryable reports whether err is worth retrying at the chunk level:
// rate limits and server errors yes, client errors and local failures no.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	var transportErr *TransportError
	return errors.As(err, &transportErr)
}

// Transcribe converts an audio file of any format and transcribes it.
func Transcribe(ctx context.Context, audioPath, lang string) (*string, error) {
	if _, err := os.Stat(audioPath); err != nil {
		return nil, fmt.Errorf("input audio file error: %w", err)
	}

	tmpWav, err := os.CreateTemp("", "vox_temp_*.wav")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp WAV file: %w", err)
	}
	tmpWavPath := tmpWav.Name()
	tmpWav.Close()
	defer os.Remove(tmpWavPath)

	if err := convertToWav(ctx, audioPath, tmpWavPath); err != nil {
		return nil, err
	}

	return transcribeWavFile(ctx, tmpWavPath, lang)
}

// TranscribeWAV transcribes a WAV file that is already 16kHz mono s16le,
// skipping the redundant ffmpeg conversion (used for pre-split chunks).
func TranscribeWAV(ctx context.Context, wavPath, lang string) (*string, error) {
	if _, err := os.Stat(wavPath); err != nil {
		return nil, fmt.Errorf("input audio file error: %w", err)
	}
	return transcribeWavFile(ctx, wavPath, lang)
}

func transcribeWavFile(ctx context.Context, wavPath, lang string) (*string, error) {
	audioData, err := os.ReadFile(wavPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read WAV file: %w", err)
	}
	// The endpoint wants raw s16le samples, not a RIFF container.
	audioData = stripWavHeader(audioData)

	url := fmt.Sprintf(apiURLFormat, lang, config.APIKey())
	client := &http.Client{Timeout: 60 * time.Second}

	var resp *http.Response
	var lastErr error
	for i := 0; i < maxHTTPAttempts; i++ {
		// Build the request fresh each attempt: reusing one request
		// resends an empty body because the reader is already drained.
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(audioData))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		req.Header.Set("Content-Type", "audio/l16; rate=16000; channels=1")

		resp, lastErr = client.Do(req)
		if lastErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, &TransportError{Err: fmt.Errorf("HTTP request error after retries: %w", ctx.Err())}
		case <-time.After(time.Second * time.Duration(i+1)):
		}
	}
	if lastErr != nil {
		return nil, &TransportError{Err: fmt.Errorf("HTTP request error after retries: %w", lastErr)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	return extractTranscript(string(body))
}

// stripWavHeader drops the RIFF container so only raw samples are posted.
// Non-WAV input is returned untouched.
func stripWavHeader(data []byte) []byte {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return data
	}
	for i := 12; i+8 <= len(data); {
		size := binary.LittleEndian.Uint32(data[i+4 : i+8])
		if string(data[i:i+4]) == "data" {
			end := i + 8 + int(size)
			if end > len(data) || end < 0 {
				end = len(data)
			}
			return data[i+8 : end]
		}
		if size > uint32(len(data)-i-8) {
			break
		}
		i += 8 + int(size)
	}
	return data
}

func parseRetryAfter(header string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Retry-After may also be an HTTP date.
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func extractTranscript(response string) (*string, error) {
	lines := strings.Split(strings.TrimSpace(response), "\n")

	// Accumulate every result on every line; only the last line's
	// first result used to be kept.
	var parts []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			continue
		}

		var resList []interface{}
		if v, ok := result["results"]; ok {
			resList, _ = v.([]interface{})
		} else if v, ok := result["result"]; ok {
			resList, _ = v.([]interface{})
		}

		for _, resItem := range resList {
			first, ok := resItem.(map[string]interface{})
			if !ok {
				continue
			}

			altList, ok := first["alternatives"].([]interface{})
			if !ok || len(altList) == 0 {
				altList, ok = first["alternative"].([]interface{})
				if !ok || len(altList) == 0 {
					continue
				}
			}

			alt, ok := altList[0].(map[string]interface{})
			if !ok {
				continue
			}

			if text, ok := alt["transcript"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
	}

	// No text found (e.g. silence): empty string, not an error.
	joined := strings.Join(parts, " ")
	return &joined, nil
}

func convertToWav(ctx context.Context, inputPath, outputPath string) error {
	// Suppress ffmpeg output to keep logs clean
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-i", inputPath, "-ar", "16000", "-ac", "1", outputPath)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg error: %v, details: %s", err, stderr.String())
	}
	return nil
}
