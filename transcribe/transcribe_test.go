package transcribe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtractTranscriptSingle(t *testing.T) {
	resp := `{"result":[{"alternative":[{"transcript":"hello world"}]}]}`
	got, err := extractTranscript(resp)
	if err != nil {
		t.Fatal(err)
	}
	if *got != "hello world" {
		t.Errorf("got %q, want %q", *got, "hello world")
	}
}

func TestExtractTranscriptAccumulatesLines(t *testing.T) {
	resp := "{\"result\":[{\"alternative\":[{\"transcript\":\"hello\"}]}]}\n" +
		"{\"result\":[{\"alternative\":[{\"transcript\":\"world\"}]}]}\n"
	got, err := extractTranscript(resp)
	if err != nil {
		t.Fatal(err)
	}
	if *got != "hello world" {
		t.Errorf("got %q, want %q", *got, "hello world")
	}
}

func TestExtractTranscriptEmpty(t *testing.T) {
	for _, resp := range []string{"", "{}\n", "{\"result\":[]}\n"} {
		got, err := extractTranscript(resp)
		if err != nil {
			t.Fatalf("extractTranscript(%q) error: %v", resp, err)
		}
		if *got != "" {
			t.Errorf("extractTranscript(%q) = %q, want empty", resp, *got)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("30"); got != 30*time.Second {
		t.Errorf("parseRetryAfter(30) = %v", got)
	}
	if got := parseRetryAfter("bogus"); got != 0 {
		t.Errorf("parseRetryAfter(bogus) = %v, want 0", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(empty) = %v, want 0", got)
	}
}

// The retry loop must resend the full body: reusing one http.Request
// transmits 0 bytes because the reader is already drained.
func TestTranscribeWAVRetriesWithFullBody(t *testing.T) {
	const wavSize = 8000
	payload := make([]byte, wavSize)
	for i := range payload {
		payload[i] = byte(i)
	}
	wav := filepath.Join(t.TempDir(), "chunk.wav")
	if err := os.WriteFile(wav, payload, 0644); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var retryBodyLen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		retryBodyLen.Store(int32(len(body)))
		fmt.Fprintln(w, `{"result":[{"alternative":[{"transcript":"hey there"}]}]}`)
	}))
	defer srv.Close()

	old := apiURLFormat
	apiURLFormat = srv.URL + "/recognize?lang=%s&key=%s"
	defer func() { apiURLFormat = old }()

	got, err := TranscribeWAV(context.Background(), wav, "en-US")
	if err != nil {
		t.Fatalf("TranscribeWAV = %v", err)
	}
	if *got != "hey there" {
		t.Errorf("TranscribeWAV = %q, want %q", *got, "hey there")
	}
	if calls.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2", calls.Load())
	}
	if retryBodyLen.Load() != wavSize {
		t.Errorf("retry sent %d bytes, want full %d", retryBodyLen.Load(), wavSize)
	}
}

func TestExtractTranscriptMultiResult(t *testing.T) {
	resp := `{"result":[{"alternative":[{"transcript":"hello"}]},{"alternative":[{"transcript":"world"}]}]}`
	got, err := extractTranscript(resp)
	if err != nil {
		t.Fatal(err)
	}
	if *got != "hello world" {
		t.Errorf("got %q, want %q", *got, "hello world")
	}
}

func TestStripWavHeader(t *testing.T) {
	// RIFF/WAVE with a "data" subchunk: only samples survive.
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00"), make([]byte, 16)...)
	wav = append(wav, []byte("data\x04\x00\x00\x00")...)
	wav = append(wav, 1, 2, 3, 4)
	got := stripWavHeader(wav)
	if len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Errorf("stripWavHeader kept %d bytes, want 4 sample bytes", len(got))
	}

	plain := []byte{9, 8, 7}
	if got := stripWavHeader(plain); len(got) != 3 {
		t.Errorf("non-WAV input must pass through untouched, got %d bytes", len(got))
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 {
		t.Errorf("parseRetryAfter(future date) = %v, want positive", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", got)
	}
}

func TestAPIErrorTruncates(t *testing.T) {
	err := &APIError{StatusCode: 500, Body: strings.Repeat("x", 10000)}
	if got := err.Error(); len(got) > maxErrBody+100 {
		t.Errorf("Error() is %d chars, want bounded", len(got))
	}
	if err.Body != strings.Repeat("x", 10000) {
		t.Error("Error() must not mutate the stored body")
	}
}

func TestTranscribeWAVCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	old := apiURLFormat
	apiURLFormat = srv.URL + "/recognize?lang=%s&key=%s"
	defer func() { apiURLFormat = old }()

	wav := filepath.Join(t.TempDir(), "chunk.wav")
	if err := os.WriteFile(wav, []byte{1, 2, 3}, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := TranscribeWAV(ctx, wav, "en-US"); err == nil {
		t.Error("canceled context must fail, not block on the server")
	}
}
