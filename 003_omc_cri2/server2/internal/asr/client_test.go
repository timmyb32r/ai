package asr

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientPCMAndTokenTimes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcribe" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("wrong protocol")
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) != 3200 || int16(binary.LittleEndian.Uint16(b[:2])) != 32767 || int16(binary.LittleEndian.Uint16(b[2:4])) != -32767 {
			t.Error("wrong PCM encoding")
		}
		fmt.Fprint(w, `{"text":"中国AI","tokens":["中国","AI"],"timestamps":[0,0.05]}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, time.Second)
	defer c.Close()
	pcm := make([]float32, 1600)
	pcm[0], pcm[1] = 2, -2
	r, err := c.Transcribe(context.Background(), pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Timestamps) != 2 || r.Tokens[0] != "中国" {
		t.Fatal("tokens must not be expanded into runes")
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	cases := []string{
		`{}`, `{"text":"中","tokens":[],"timestamps":[]}`,
		`{"text":"中","tokens":["中"],"timestamps":[]}`,
		`{"text":"中","tokens":["中"],"timestamps":[-1]}`,
		`{"text":"中","tokens":["中"],"timestamps":[100]}`,
		`{"text":"中","tokens":["中"],"timestamps":[1e999]}`,
		`{"text":"中文","tokens":["中","文"],"timestamps":[0.05,0]}`,
		`{"text":"中","tokens":[""],"timestamps":[0]}`,
		`{"text":"","tokens":[],"timestamps":[]} {}`,
		strings.Repeat("x", maxResponseBytes+1),
	}
	for _, body := range cases {
		t.Run(fmt.Sprintf("%d", len(body))+body[:min(len(body), 20)], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
			defer srv.Close()
			c := NewClient(srv.URL, time.Second)
			defer c.Close()
			if _, err := c.Transcribe(context.Background(), make([]float32, 1600)); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
}

func TestClientSilenceAndInputValidation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"text":"","tokens":[],"timestamps":[]}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, time.Second)
	defer c.Close()
	if _, err := c.Transcribe(context.Background(), make([]float32, 1600)); err != nil {
		t.Fatal(err)
	}
	bad := make([]float32, 1600)
	bad[0] = float32(math.NaN())
	for _, pcm := range [][]float32{nil, make([]float32, 1599), make([]float32, MaxSamples+1), bad} {
		if _, err := c.Transcribe(context.Background(), pcm); err == nil {
			t.Error("accepted invalid input")
		}
	}
	if calls != 1 {
		t.Errorf("invalid requests reached server: %d", calls)
	}
}

func TestClientCancellationAndHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"ready":true}`)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, time.Second)
	defer c.Close()
	if err := c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Transcribe(ctx, make([]float32, 1600)); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cancellation not propagated")
	}
}
