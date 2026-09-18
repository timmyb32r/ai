// Package tokenizer provides Chinese word segmentation.
package tokenizer

import "github.com/criradio/server/internal/models"

// Tokenizer segments Chinese text into words.
type Tokenizer interface {
	// Segment splits Chinese text into words.
	Segment(text string) ([]models.Token, error)
	// Close releases resources held by the tokenizer.
	Close() error
}
