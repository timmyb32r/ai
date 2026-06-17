// Package dictionary provides Chinese-English dictionary lookups using CC-CEDICT.
package dictionary

import "fmt"

// Entry represents a single CC-CEDICT dictionary entry.
type Entry struct {
	Traditional string
	Simplified  string
	Pinyin      string
	Meanings    []string
}

func (e *Entry) String() string {
	return fmt.Sprintf("%s %s [%s] %v", e.Traditional, e.Simplified, e.Pinyin, e.Meanings)
}

// Stats tracks dictionary lookup statistics.
type Stats struct {
	Hits   int64
	Misses int64
	Total  int64
}

// HitRate returns the ratio of successful lookups.
func (s *Stats) HitRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(s.Total)
}

// Dictionary provides lookups against the CC-CEDICT database.
type Dictionary interface {
	// Lookup finds a word in the dictionary and returns its full entry.
	Lookup(simplified string) (*Entry, error)
	// LookupPinyin returns only the pinyin for a word (faster than full lookup).
	LookupPinyin(simplified string) string
	// Stats returns current lookup statistics.
	Stats() Stats
	// Close releases resources.
	Close() error
}
