// Package tokenizer provides Chinese word segmentation using go-ego/gse.
package tokenizer

// Token represents a single segmented Chinese word with its character positions.
type Token struct {
	Text      string // the word text
	CharStart int    // index of first character in original text
	CharEnd   int    // index after last character (exclusive)
}

// Tokenizer segments Chinese text into words.
type Tokenizer interface {
	// Segment splits Chinese text into words using dictionary-based segmentation.
	Segment(text string) []Token
	// Close releases resources held by the tokenizer.
	Close() error
}
