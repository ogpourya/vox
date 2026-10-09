package tts

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSynthesizeRetries429(t *testing.T) {
	const audio = "fake-mp3-bytes"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintln(w, "quota, try again")
			return
		}
		io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, `{"audioContent":%q}`, base64.StdEncoding.EncodeToString([]byte(audio)))
	}))
	defer srv.Close()

	old := apiURLFormat
	apiURLFormat = srv.URL + "/synthesize?key=%s"
	defer func() { apiURLFormat = old }()

	got, err := Synthesize(context.Background(), "hi", "voice", "en-US", 1.0)
	if err != nil {
		t.Fatalf("Synthesize = %v", err)
	}
	if string(got) != audio {
		t.Errorf("Synthesize = %q, want %q", got, audio)
	}
	if calls.Load() != 2 {
		t.Errorf("server saw %d requests, want 2", calls.Load())
	}
}

func TestSynthesizeFailsAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	}))
	defer srv.Close()

	old := apiURLFormat
	apiURLFormat = srv.URL + "/synthesize?key=%s"
	defer func() { apiURLFormat = old }()

	if _, err := Synthesize(context.Background(), "hi", "voice", "en-US", 1.0); err == nil {
		t.Error("persistent 500 must fail after retries")
	}
}

func TestSynthesizeClientErrorFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, "bad voice")
	}))
	defer srv.Close()

	old := apiURLFormat
	apiURLFormat = srv.URL + "/synthesize?key=%s"
	defer func() { apiURLFormat = old }()

	if _, err := Synthesize(context.Background(), "hi", "bogus", "en-US", 1.0); err == nil {
		t.Error("400 must fail")
	}
	if calls.Load() != 1 {
		t.Errorf("400 retried %d times, want exactly 1 request", calls.Load())
	}
}
