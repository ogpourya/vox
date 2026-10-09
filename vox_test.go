package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ogpourya/vox/transcribe"
)

func TestLangFromVoice(t *testing.T) {
	cases := []struct{ voice, want string }{
		{"en-US-Casual-K", "en-US"},
		{"en-IN-Chirp-HD-D", "en-IN"},
		{"fr-FR-Standard-A", "fr-FR"},
		{"bogus", "en-US"},
		{"", "en-US"},
	}
	for _, c := range cases {
		if got := langFromVoice(c.voice); got != c.want {
			t.Errorf("langFromVoice(%q) = %q, want %q", c.voice, got, c.want)
		}
	}
}

func TestIsSTTInput(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "talk.mp3")
	if err := os.WriteFile(audio, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"missing audio path still routes to STT for a clear error", []string{filepath.Join(dir, "missing.mp3")}, true},
		{"existing audio file", []string{audio}, true},
		{"existing regular file", []string{text}, true},
		{"directory is never audio", []string{sub}, false},
		{"plain text is spoken", []string{"hey there buddy"}, false},
		{"empty arg ignored", []string{""}, false},
		{"mixed text and audio routes to STT", []string{"hello", audio}, true},
	}
	for _, c := range cases {
		if got := isSTTInput(c.args); got != c.want {
			t.Errorf("%s: isSTTInput(%q) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

func TestUniqueFiles(t *testing.T) {
	got := uniqueFiles([]string{"a.mp3", "b.mp3", "a.mp3"})
	if len(got) != 2 || got[0] != "a.mp3" || got[1] != "b.mp3" {
		t.Errorf("uniqueFiles = %q, want [a.mp3 b.mp3]", got)
	}
	// Same file via different spellings is still one file.
	got = uniqueFiles([]string{"a/talk.mp3", "./a/talk.mp3"})
	if len(got) != 1 || got[0] != "a/talk.mp3" {
		t.Errorf("uniqueFiles with ./ prefix = %q, want [a/talk.mp3]", got)
	}
}

func TestResultKeys(t *testing.T) {
	got := resultKeys([]string{"a/talk.mp3", "b/other.mp3"})
	if got[0] != "talk.mp3" || got[1] != "other.mp3" {
		t.Errorf("distinct basenames: got %q", got)
	}

	got = resultKeys([]string{"a/talk.mp3", "b/talk.mp3"})
	if got[0] != "a/talk.mp3" || got[1] != "b/talk.mp3" {
		t.Errorf("colliding basenames must keep full paths, got %q", got)
	}
}

func TestJoinOrdered(t *testing.T) {
	if got := joinOrdered([]string{"hello", "", "   ", "world"}); got != "hello world" {
		t.Errorf("joinOrdered with gaps = %q, want %q", got, "hello world")
	}
	if got := joinOrdered([]string{"", "  "}); got != "" {
		t.Errorf("joinOrdered all-empty = %q, want %q", got, "")
	}
}

func TestChunkErrorClassification(t *testing.T) {
	if transcribe.IsRetryable(&transcribe.APIError{StatusCode: 400, Body: "bad lang"}) {
		t.Error("400 client errors must not be retried")
	}
	if transcribe.IsRetryable(fmt.Errorf("ffmpeg error: exit status 1")) {
		t.Error("local ffmpeg failures must not be retried")
	}
	if !transcribe.IsRetryable(&transcribe.APIError{StatusCode: 429}) {
		t.Error("429 must be retried")
	}
	if !transcribe.IsRetryable(&transcribe.APIError{StatusCode: 500}) {
		t.Error("500 must be retried")
	}
	if !transcribe.IsRetryable(&transcribe.TransportError{Err: fmt.Errorf("EOF")}) {
		t.Error("transport errors must be retried")
	}
}
