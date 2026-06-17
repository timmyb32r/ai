package tokenizer

import (
	"github.com/go-ego/gse"
)

// gseTokenizer implements Tokenizer using go-ego/gse.
type gseTokenizer struct {
	seg gse.Segmenter
}

// New creates a new Tokenizer backed by go-ego/gse.
// dictPath is the directory containing gse dictionary files (zh/s_1.txt, zh/t_1.txt, etc.).
func New(dictPath string) (Tokenizer, error) {
	t := &gseTokenizer{}
	// Load dictionaries — same approach as 001_omc_cri
	t.seg.LoadDict(
		dictPath + "/zh/s_1.txt",
		dictPath + "/zh/t_1.txt",
	)
	t.seg.LoadStop(dictPath + "/zh/stop_word.txt")
	return t, nil
}

// Segment splits Chinese text into words using gse dictionary-based segmentation
// with HMM enabled for better accuracy. Returns tokens with character (rune) positions.
// Matches 001_omc_cri's gseSegmenter.Segment() approach.
func (t *gseTokenizer) Segment(text string) []Token {
	if text == "" {
		return nil
	}

	// Use Cut() with HMM — same API as 001_omc_cri.
	// Cut returns a []string of words.
	words := t.seg.Cut(text, true)

	// Build tokens with rune offsets
	textLen := len([]rune(text))
	tokens := make([]Token, 0, len(words))
	runePos := 0
	for _, word := range words {
		wordRunes := len([]rune(word))
		if wordRunes == 0 {
			continue
		}
		end := runePos + wordRunes
		if end > textLen {
			end = textLen
		}
		tokens = append(tokens, Token{
			Text:      word,
			CharStart: runePos,
			CharEnd:   end,
		})
		runePos = end
		if runePos >= textLen {
			break
		}
	}
	return tokens
}

func (t *gseTokenizer) Close() error {
	return nil
}
