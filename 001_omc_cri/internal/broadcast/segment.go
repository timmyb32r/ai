package broadcast

import (
	"path/filepath"

	"github.com/go-ego/gse"
)

// WordSegmenter segments Chinese text into word-boundary offsets.
// Implementations must be safe for concurrent use.
type WordSegmenter interface {
	// Segment returns word boundaries as rune-offset pairs into text.
	// Returns nil for empty text.
	Segment(text string) []WordBoundary
}

// gseSegmenter wraps gse.Segmenter to implement WordSegmenter.
// gse is a pure-Go Chinese word segmentation library (zero CGO).
type gseSegmenter struct {
	seg *gse.Segmenter
}

// NewGseSegmenter creates a segmenter. dictDir is the directory containing
// the gse dictionary files (zh/s_1.txt, zh/t_1.txt). When empty the
// embedded dictionary in the module cache is used (suitable for dev only).
func NewGseSegmenter(dictDir string) *gseSegmenter {
	seg := new(gse.Segmenter)
	var err error
	if dictDir != "" {
		err = seg.LoadDict(
			filepath.Join(dictDir, "zh", "s_1.txt"),
			filepath.Join(dictDir, "zh", "t_1.txt"),
		)
	} else {
		err = seg.LoadDict()
	}
	if err != nil {
		// Log but continue — segmentation will return nil, and the client
		// falls back to single-character highlighting.
	}
	return &gseSegmenter{seg: seg}
}

// Segment implements WordSegmenter.
func (s *gseSegmenter) Segment(text string) []WordBoundary {
	if text == "" || s.seg == nil {
		return nil
	}
	textLen := len([]rune(text))
	// Cut with HMM enabled for better unknown-word handling.
	words := s.seg.Cut(text, true)
	var bounds []WordBoundary
	runePos := 0
	for _, word := range words {
		wordRunes := len([]rune(word))
		if wordRunes == 0 {
			continue
		}
		// The segmenter may produce output whose rune count slightly exceeds
		// the input; clamp each boundary to the text length so the client
		// never receives an out-of-range index.
		end := runePos + wordRunes
		if end > textLen {
			end = textLen
		}
		bounds = append(bounds, WordBoundary{
			CharStart: runePos,
			CharEnd:   end,
		})
		runePos = end
		if runePos >= textLen {
			break
		}
	}
	return bounds
}
