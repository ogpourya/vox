// Package config holds shared endpoint and credential settings.
package config

import "os"

const (
	// GoogleSpeechAPIKey is intentionally hardcoded and shared by both
	// transcription (STT) and synthesis (TTS).
	GoogleSpeechAPIKey = "AIzaSyBOti4mM-6x9WDnZIjIeyEU21OpBXqWBgw"
	GoogleSpeechAPIURL = "https://www.google.com/speech-api/v2/recognize?client=chromium&lang=%s&key=%s"
	GoogleTTSAPIURL    = "https://texttospeech.googleapis.com/v1/text:synthesize?key=%s"
)

// APIKey returns the shared Google key, overridable via VOX_GOOGLE_API_KEY
// so a quota-exhausted key can be rotated without a code change.
func APIKey() string {
	if k := os.Getenv("VOX_GOOGLE_API_KEY"); k != "" {
		return k
	}
	return GoogleSpeechAPIKey
}
