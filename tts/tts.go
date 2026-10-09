package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ogpourya/vox/config"
)

type ttsRequest struct {
	Input struct {
		Text string `json:"text"`
	} `json:"input"`
	Voice struct {
		LanguageCode string `json:"languageCode"`
		Name         string `json:"name"`
	} `json:"voice"`
	AudioConfig struct {
		AudioEncoding string  `json:"audioEncoding"`
		SpeakingRate  float64 `json:"speakingRate"`
	} `json:"audioConfig"`
}

const (
	maxResponseBytes = 32 << 20 // TTS audio payloads can be large
	maxAttempts      = 5
	// maxRetryAfter caps a single server-requested wait.
	maxRetryAfter = 60 * time.Second
)

// apiURLFormat builds the TTS request URL from the key. A variable rather
// than the config const directly so tests can redirect it.
var apiURLFormat = config.GoogleTTSAPIURL

func Synthesize(ctx context.Context, text, voice, lang string, rate float64) ([]byte, error) {
	var reqBody ttsRequest
	reqBody.Input.Text = text
	reqBody.Voice.LanguageCode = lang
	reqBody.Voice.Name = voice
	reqBody.AudioConfig.AudioEncoding = "MP3"
	reqBody.AudioConfig.SpeakingRate = rate

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build TTS request: %w", err)
	}

	url := fmt.Sprintf(apiURLFormat, config.APIKey())
	client := &http.Client{Timeout: 60 * time.Second}

	var resp *http.Response
	for i := 0; i < maxAttempts; i++ {
		// Fresh body each attempt: a reused reader resends 0 bytes.
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to build TTS request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")

		r, err := client.Do(req)
		if err != nil {
			if i+1 < maxAttempts && sleepOrDone(ctx, time.Second*time.Duration(i+1)) {
				continue
			}
			if ctx.Err() != nil {
				return nil, fmt.Errorf("TTS request error: %w", ctx.Err())
			}
			return nil, fmt.Errorf("TTS request error after retries: %w", err)
		}

		// Only rate limits and server errors are worth retrying.
		if r.StatusCode != http.StatusTooManyRequests && r.StatusCode < 500 {
			resp = r
			break
		}
		wait := time.Second * time.Duration(i+1)
		if ra := parseRetryAfter(r.Header.Get("Retry-After")); ra > 0 {
			wait = min(ra, maxRetryAfter)
		}
		drainAndClose(r.Body)
		if i+1 == maxAttempts {
			return nil, fmt.Errorf("Google TTS error %d after retries", r.StatusCode)
		}
		if !sleepOrDone(ctx, wait) {
			return nil, fmt.Errorf("TTS request error: %w", ctx.Err())
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read TTS response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google TTS error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse TTS response: %w", err)
	}
	if result.AudioContent == "" {
		return nil, fmt.Errorf("TTS returned empty audio")
	}

	audio, err := base64.StdEncoding.DecodeString(result.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("failed to decode TTS audio: %w", err)
	}
	return audio, nil
}

// sleepOrDone waits d unless ctx is canceled, reporting whether it waited.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func drainAndClose(body io.ReadCloser) {
	io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes))
	body.Close()
}

func parseRetryAfter(header string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
