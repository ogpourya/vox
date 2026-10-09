package config

import (
	"os"
	"strings"
	"testing"
)

func TestEndpointsUseHTTPS(t *testing.T) {
	for name, url := range map[string]string{"STT": GoogleSpeechAPIURL, "TTS": GoogleTTSAPIURL} {
		if !strings.HasPrefix(url, "https://") {
			t.Errorf("%s URL %q must use https", name, url)
		}
	}
}

func TestAPIKeyEnvOverride(t *testing.T) {
	t.Setenv("VOX_GOOGLE_API_KEY", "override-key")
	if got := APIKey(); got != "override-key" {
		t.Errorf("APIKey() = %q, want override", got)
	}
}

func TestAPIKeyDefault(t *testing.T) {
	if err := os.Unsetenv("VOX_GOOGLE_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if got := APIKey(); got != GoogleSpeechAPIKey {
		t.Errorf("APIKey() = %q, want built-in default", got)
	}
}
