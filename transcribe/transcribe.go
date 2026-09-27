package transcribe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ogpourya/vox/config"
)

func Transcribe(audioPath, lang string) (*string, error) {
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

	if err := convertToWav(audioPath, tmpWavPath); err != nil {
		return nil, fmt.Errorf("ffmpeg conversion failed: %w", err)
	}

	audioData, err := os.ReadFile(tmpWavPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read WAV file: %w", err)
	}

	url := fmt.Sprintf(config.GoogleSpeechAPIURL, lang, config.GoogleSpeechAPIKey)

	req, err := http.NewRequest("POST", url, bytes.NewReader(audioData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "audio/l16; rate=16000; channels=1")

	// CHANGED: Increased timeout to 60s for stability
	client := &http.Client{Timeout: 60 * time.Second}

	var resp *http.Response
	for i := 0; i < 3; i++ {
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(time.Second * time.Duration(i+1))
	}
	if err != nil {
		return nil, fmt.Errorf("HTTP request error after retries: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google API error %d: %s", resp.StatusCode, string(body))
	}

	transcript, err := extractTranscript(string(body))
	if err != nil {
		return nil, err
	}
	return transcript, nil
}

func extractTranscript(response string) (*string, error) {
	lines := strings.Split(strings.TrimSpace(response), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
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

		if len(resList) == 0 {
			continue
		}

		first, ok := resList[0].(map[string]interface{})
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

		text, ok := alt["transcript"].(string)
		if ok && text != "" {
			return &text, nil
		}
	}

	// CHANGED: If no text found, return empty string instead of error.
	// This prevents retries on silent chunks (like intro music).
	empty := ""
	return &empty, nil
}

func convertToWav(inputPath, outputPath string) error {
	// Suppress ffmpeg output to keep logs clean
	cmd := exec.Command("ffmpeg", "-v", "error", "-y", "-i", inputPath, "-ar", "16000", "-ac", "1", outputPath)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg error: %v, details: %s", err, stderr.String())
	}
	return nil
}
