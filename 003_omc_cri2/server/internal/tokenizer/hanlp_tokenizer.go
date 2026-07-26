package tokenizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/criradio/server/internal/models"
)

// HanLPTokenizer segments Chinese text using a self-hosted HanLP REST API.
// POST /parse with JSON body {"text": "..."} → parses tok/fine from response.
type HanLPTokenizer struct {
	url    string
	client *http.Client
}

// NewHanLP creates a tokenizer connected to the HanLP REST server at url
// (e.g. "http://localhost:8765" for the hanlp-rest Docker image).
func NewHanLP(url string) *HanLPTokenizer {
	return &HanLPTokenizer{
		url: url,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type hanlpRequest struct {
	Text string `json:"text"`
}

type hanlpResponse struct {
	TokFine []string `json:"tok/fine"`
	TokCoarse []string `json:"tok/coarse"`
}

// Segment calls the HanLP REST API and converts the result to []models.Token.
func (h *HanLPTokenizer) Segment(text string) ([]models.Token, error) {
	if text == "" {
		return nil, nil
	}

	body, err := json.Marshal(hanlpRequest{Text: text})
	if err != nil {
		return nil, fmt.Errorf("hanlp marshal: %w", err)
	}

	resp, err := h.client.Post(h.url+"/parse", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hanlp request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("hanlp HTTP %d: %s", resp.StatusCode, string(b))
	}

	var parsed hanlpResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("hanlp decode: %w", err)
	}

	// Diagnostic: log raw HanLP response for visual quality inspection.
	log.Printf("[hanlp] text=%q tok/fine=%s tok/coarse=%s",
		truncate(text, 120),
		strings.Join(parsed.TokFine, " | "),
		strings.Join(parsed.TokCoarse, " | "),
	)

	// Convert tok/fine to []models.Token with rune positions.
	// HanLP returns space-separated tokens. We compute CharStart/CharEnd
	// by scanning the original text rune by rune and matching tokens.
	return tokensFromStrings(parsed.TokFine, []rune(text)), nil
}

// tokensFromStrings converts a slice of token strings to []models.Token
// with correct CharStart/CharEnd indices in the original rune slice.
func tokensFromStrings(words []string, runes []rune) []models.Token {
	tokens := make([]models.Token, 0, len(words))
	pos := 0 // current position in runes

	for _, w := range words {
		// Skip whitespace-only tokens
		if len(w) == 0 {
			continue
		}

		// Find this word in the rune slice starting from pos
		wordRunes := []rune(w)
		if len(wordRunes) == 0 {
			continue
		}

		// Scan forward to find the word
		found := false
		for i := pos; i+len(wordRunes) <= len(runes); i++ {
			if runeSlicesEqual(runes[i:i+len(wordRunes)], wordRunes) {
				tokens = append(tokens, models.Token{
					Text:      w,
					CharStart: i,
					CharEnd:   i + len(wordRunes),
				})
				pos = i + len(wordRunes)
				found = true
				break
			}
		}
		if !found {
			// Fallback: place at current position
			tokens = append(tokens, models.Token{
				Text:      w,
				CharStart: pos,
				CharEnd:   pos + len(wordRunes),
			})
			pos += len(wordRunes)
		}
	}

	return tokens
}

func runeSlicesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Close is a no-op for the HTTP client.
func (h *HanLPTokenizer) Close() error {
	return nil
}

// truncate shortens s to maxLen runes, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
