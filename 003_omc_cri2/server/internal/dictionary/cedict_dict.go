package dictionary

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// cedictDict implements Dictionary backed by a CC-CEDICT file.
type cedictDict struct {
	entries map[string]*Entry // key: simplified Chinese word
	stats   Stats
	mu      sync.RWMutex
}

// Load loads a CC-CEDICT file and returns a ready-to-use Dictionary.
// The CC-CEDICT format is: Traditional Simplified [pinyin] /definition1/definition2/...
func Load(dictPath string) (Dictionary, error) {
	f, err := os.Open(dictPath)
	if err != nil {
		return nil, fmt.Errorf("open dictionary: %w", err)
	}
	defer f.Close()

	d := &cedictDict{
		entries: make(map[string]*Entry, 130000), // CC-CEDICT has ~123K entries
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // Handle long lines
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, err := parseLine(line)
		if err != nil {
			// Skip malformed lines silently in production
			continue
		}
		d.entries[entry.Simplified] = entry
		// Also index by traditional for cross-lookup
		if entry.Traditional != entry.Simplified {
			if _, exists := d.entries[entry.Traditional]; !exists {
				d.entries[entry.Traditional] = entry
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan dictionary: %w", err)
	}

	return d, nil
}

// parseLine parses one CC-CEDICT line.
// Format: Traditional Simplified [pinyin] /definition1/definition2/...
func parseLine(line string) (*Entry, error) {
	// Split on first '['
	chars := []rune(line)
	openBracket := -1
	closeBracket := -1
	for i, c := range chars {
		if c == '[' && openBracket == -1 {
			openBracket = i
		}
		if c == ']' && openBracket != -1 {
			closeBracket = i
			break
		}
	}
	if openBracket == -1 || closeBracket == -1 {
		return nil, fmt.Errorf("no pinyin brackets in line")
	}

	// The part before '[' is "Traditional Simplified"
	head := strings.TrimSpace(string(chars[:openBracket]))
	headParts := strings.Fields(head)
	if len(headParts) < 2 {
		return nil, fmt.Errorf("invalid head: %q", head)
	}

	traditional := headParts[0]
	simplified := headParts[1]

	pinyin := string(chars[openBracket+1 : closeBracket])

	// Meanings are between / / slashes
	meaningsStr := string(chars[closeBracket+1:])
	var meanings []string
	for _, m := range strings.Split(meaningsStr, "/") {
		m = strings.TrimSpace(m)
		if m != "" {
			meanings = append(meanings, m)
		}
	}

	return &Entry{
		Traditional: traditional,
		Simplified:  simplified,
		Pinyin:      pinyin,
		Meanings:    meanings,
	}, nil
}

func (d *cedictDict) Lookup(simplified string) (*Entry, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	atomic.AddInt64(&d.stats.Total, 1)

	entry, ok := d.entries[simplified]
	if !ok {
		atomic.AddInt64(&d.stats.Misses, 1)
		return nil, fmt.Errorf("word %q not found in dictionary", simplified)
	}
	atomic.AddInt64(&d.stats.Hits, 1)
	return entry, nil
}

func (d *cedictDict) LookupPinyin(simplified string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	atomic.AddInt64(&d.stats.Total, 1)

	entry, ok := d.entries[simplified]
	if !ok {
		atomic.AddInt64(&d.stats.Misses, 1)
		return ""
	}
	atomic.AddInt64(&d.stats.Hits, 1)
	return entry.Pinyin
}

func (d *cedictDict) Stats() Stats {
	return Stats{
		Hits:   atomic.LoadInt64(&d.stats.Hits),
		Misses: atomic.LoadInt64(&d.stats.Misses),
		Total:  atomic.LoadInt64(&d.stats.Total),
	}
}

func (d *cedictDict) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = nil
	return nil
}
