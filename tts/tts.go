package tts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		AudioEncoding string `json:"audioEncoding"`
	} `json:"audioConfig"`
}

func Synthesize(text, voice, lang string) ([]byte, error) {
	var reqBody ttsRequest
	reqBody.Input.Text = text
	reqBody.Voice.LanguageCode = lang
	reqBody.Voice.Name = voice
	reqBody.AudioConfig.AudioEncoding = "MP3"

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build TTS request: %w", err)
	}

	url := fmt.Sprintf(config.GoogleTTSAPIURL, config.GoogleTTSAPIKey)
	client := &http.Client{Timeout: 60 * time.Second}

	resp, err := client.Post(url, "application/json; charset=utf-8", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("TTS request error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
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
