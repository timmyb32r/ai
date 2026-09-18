// Package asr talks to the resident SenseVoice service. Times are per token,
// relative to the supplied PCM, and are not indices into Unicode runes.
package asr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const SampleRate = 16000
const MaxSamples = 30 * SampleRate
const maxResponseBytes = 1 << 20

type Result struct {
	Text       string    `json:"text"`
	Tokens     []string  `json:"tokens"`
	Timestamps []float64 `json:"timestamps"`
}

type Client struct {
	url  string
	http *http.Client
}

func NewClient(url string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 35 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.MaxIdleConnsPerHost = 1
	transport.ResponseHeaderTimeout = timeout
	return &Client{url: strings.TrimRight(url, "/"), http: &http.Client{
		Transport: transport, Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) Close() error { c.http.CloseIdleConnections(); return nil }

func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ASR health HTTP %d", resp.StatusCode)
	}
	var health struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health); err != nil {
		return err
	}
	if !health.Ready {
		return fmt.Errorf("ASR model is not ready")
	}
	return nil
}

func (c *Client) Transcribe(ctx context.Context, pcm []float32) (*Result, error) {
	if len(pcm) < SampleRate/10 || len(pcm) > MaxSamples {
		return nil, fmt.Errorf("ASR PCM must contain 0.1–30 seconds at 16000 Hz: %d samples", len(pcm))
	}
	data := make([]byte, len(pcm)*2)
	for i, sample := range pcm {
		if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
			return nil, fmt.Errorf("ASR PCM sample %d is not finite", i)
		}
		if sample > 1 {
			sample = 1
		}
		if sample < -1 {
			sample = -1
		}
		// Match the old PCM16 quantization, without per-sample file writes.
		binary.LittleEndian.PutUint16(data[2*i:], uint16(int16(sample*32767)))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/transcribe", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ASR request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ASR HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ASR response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("ASR response exceeds 1 MiB")
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("ASR returned invalid UTF-8")
	}
	var wire struct {
		Text       *string    `json:"text"`
		Tokens     *[]string  `json:"tokens"`
		Timestamps *[]float64 `json:"timestamps"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("ASR JSON: %w", err)
	}
	if wire.Text == nil || wire.Tokens == nil || wire.Timestamps == nil {
		return nil, fmt.Errorf("ASR response missing text, tokens or timestamps")
	}
	r := &Result{Text: *wire.Text, Tokens: *wire.Tokens, Timestamps: *wire.Timestamps}
	if len(r.Tokens) != len(r.Timestamps) {
		return nil, fmt.Errorf("ASR token/timestamp count differs")
	}
	if len(r.Tokens) > 16384 {
		return nil, fmt.Errorf("ASR returned too many tokens")
	}
	if strings.TrimSpace(r.Text) != "" && len(r.Tokens) == 0 {
		return nil, fmt.Errorf("ASR returned text without timing information")
	}
	duration := float64(len(pcm)) / SampleRate
	for i, ts := range r.Timestamps {
		if math.IsNaN(ts) || math.IsInf(ts, 0) || ts < 0 || ts > duration+0.25 || (i > 0 && ts < r.Timestamps[i-1]) {
			return nil, fmt.Errorf("ASR invalid timestamp at token %d", i)
		}
		if r.Tokens[i] == "" {
			return nil, fmt.Errorf("ASR empty token %d", i)
		}
	}
	return r, nil
}
