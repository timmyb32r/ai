// Package dictionary provides Chinese dictionary lookups (CC-CEDICT and BKRS).
package dictionary

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// bkrsDict implements Dictionary backed by a raw BKRS dump file.
//
// BKRS raw dump format (bkrs.info daily dabkrs_YYMMDD.gz):
//   - Records are separated by empty lines.
//   - Line 1: simplified Chinese headword
//   - Line 2: pinyin (space-separated syllables with tone numbers)
//   - Line 3: Russian body with BKRS markup:
//     [m1]sense text[/m]        — numbered meaning (m1, m2, m3, ...)
//     [p]label[/p]              — grammatical/style label (e.g. "уст.", "г.")
//     [i]note[/i]               — italic usage note
//     [c]text[/c]               — comment/annotation
//     [ref]text[/ref]           — cross-reference (e.g. "см. 上海")
//
// Parser strips all markup and builds structured Sense entries.
type bkrsDict struct {
	entries map[string]*Entry // key: simplified Chinese word
	stats   Stats
	mu      sync.RWMutex
}

// LoadBKRS loads a raw BKRS dump file (optionally gzip-compressed) and
// returns a ready-to-use Dictionary.
func LoadBKRS(dumpPath string) (Dictionary, error) {
	f, err := os.Open(dumpPath)
	if err != nil {
		return nil, fmt.Errorf("open bkrs dump: %w", err)
	}
	defer f.Close()

	// Detect and decompress gzip (BKRS daily dumps are .gz files).
	var reader io.Reader = f
	if isGzip(f) {
		if _, err := f.Seek(0, 0); err != nil {
			return nil, fmt.Errorf("seek bkrs dump: %w", err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("decompress bkrs dump: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	d := &bkrsDict{
		entries: make(map[string]*Entry, 300000), // BKRS has ~300K+ entries
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var (
		headword, pinyin, body string
		linesInRecord          int
	)

	flush := func() {
		if headword == "" {
			return
		}
		entry := parseBKRSRecord(headword, pinyin, body)
		if entry != nil {
			d.entries[entry.Simplified] = entry
		}
		headword, pinyin, body = "", "", ""
		linesInRecord = 0
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		switch linesInRecord {
		case 0:
			headword = line
		case 1:
			pinyin = line
		case 2:
			body = line
		default:
			// Multi-line body (rare, but some entries have line breaks)
			body += " " + line
		}
		linesInRecord++
	}
	flush() // last record

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan bkrs dump: %w", err)
	}

	return d, nil
}

// parseBKRSRecord parses one BKRS record (headword, pinyin, body) into an Entry.
func parseBKRSRecord(headword, pinyin, body string) *Entry {
	if headword == "" || body == "" {
		return nil
	}

	// Normalise pinyin: BKRS uses tone numbers (e.g. "zhe4"), same as CC-CEDICT.
	pinyin = strings.TrimSpace(pinyin)

	// Parse the body into structured senses.
	senses := parseBKRSSenses(body)

	// Build flat meanings for backward compatibility.
	var flat []string
	for _, s := range senses {
		text := s.Text
		if len(s.Labels) > 0 {
			text = strings.Join(s.Labels, ", ") + " " + text
		}
		flat = append(flat, text)
	}

	return &Entry{
		Simplified: headword,
		Pinyin:     pinyin,
		Meanings:   flat,
		Senses:     senses,
	}
}

// parseBKRSSenses parses BKRS body markup into structured Sense entries.
//
// Markup reference (from bkrs.info/p47):
//
//	[m1]…[/m]  — numbered meaning block (m1=значение 1, m2=значение 2, ...)
//	[p]…[/p]   — grammatical/style label (помета): уст., г., бот., ...
//	[i]…[/i]   — italic usage note / explanation in parentheses
//	[c]…[/c]   — comment/annotation (grey text)
//	[ref]…[/ref] — cross-reference (see also ...)
//
// Strategy: split body by [mN] blocks. Inside each block, extract
// [p], [i], [c], [ref] tags. Text outside tags is the translation.
func parseBKRSSenses(body string) []Sense {
	// Find all [mN] markers and their positions.
	type marker struct {
		num  int
		pos  int
		end  int // position after [/m]
	}
	var markers []marker

	// Scan for [mN]...[/m] blocks.
	i := 0
	runes := []rune(body)
	for i < len(runes) {
		// Look for [m
		if i+2 < len(runes) && runes[i] == '[' && runes[i+1] == 'm' {
			// Read the number after [m
			j := i + 2
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			if j > i+2 && j < len(runes) && runes[j] == ']' {
				numStr := string(runes[i+2 : j])
				num, err := strconv.Atoi(numStr)
				if err == nil {
					// Find matching [/m]
					endTag := strings.Index(string(runes[j+1:]), "[/m]")
					if endTag >= 0 {
						markers = append(markers, marker{
							num: num,
							pos: j + 1,                         // after [mN]
							end: j + 1 + endTag + len("[/m]"), // after [/m]
						})
						i = j + 1 + endTag + len("[/m]")
						continue
					}
				}
			}
		}
		i++
	}

	// If no [mN] markers, treat the whole body as one sense.
	if len(markers) == 0 {
		labels, notes, text := extractBKRSTags(body)
		if text == "" {
			return nil
		}
		return []Sense{{
			Number: 0,
			Labels: labels,
			Text:   text,
			Notes:  notes,
		}}
	}

	// Process each [mN] block.
	var senses []Sense
	for idx, m := range markers {
		blockEnd := len(runes)
		if idx+1 < len(markers) {
			blockEnd = markers[idx+1].pos
		}
		block := string(runes[m.pos:blockEnd])

		// Strip the [/m] at the end of this block's content
		if m.end < blockEnd {
			block = string(runes[m.pos:m.end])
		}

		labels, notes, text := extractBKRSTags(block)
		if text == "" && len(labels) == 0 && notes == "" {
			continue
		}
		senses = append(senses, Sense{
			Number: m.num,
			Labels: labels,
			Text:   text,
			Notes:  notes,
		})
	}

	// Handle text before the first [mN] marker (preamble).
	if len(markers) > 0 && markers[0].pos > 0 {
		preamble := strings.TrimSpace(string(runes[:markers[0].pos]))
		if preamble != "" {
			labels, notes, text := extractBKRSTags(preamble)
			if text != "" {
				// Prepend as unnumbered sense
				senses = append([]Sense{{
					Number: 0,
					Labels: labels,
					Text:   text,
					Notes:  notes,
				}}, senses...)
			}
		}
	}

	return senses
}

// extractBKRSTags extracts [p], [i], [c], [ref] tags from a text block.
// Returns labels, notes, and the cleaned text.
func extractBKRSTags(block string) (labels []string, notes string, text string) {
	// Remove [/m] trailing if present
	block = strings.TrimSpace(block)

	// Extract [p] tags → labels
	for {
		start := strings.Index(block, "[p]")
		if start < 0 {
			break
		}
		end := strings.Index(block, "[/p]")
		if end < start {
			break
		}
		label := strings.TrimSpace(block[start+3 : end])
		if label != "" {
			labels = append(labels, label)
		}
		block = block[:start] + block[end+4:]
	}

	// Extract [i] tags → notes (take the last one as the primary note)
	for {
		start := strings.Index(block, "[i]")
		if start < 0 {
			break
		}
		end := strings.Index(block, "[/i]")
		if end < start {
			break
		}
		notes = strings.TrimSpace(block[start+3 : end])
		block = block[:start] + block[end+4:]
	}

	// Extract [c] tags → append to notes
	for {
		start := strings.Index(block, "[c]")
		if start < 0 {
			break
		}
		end := strings.Index(block, "[/c]")
		if end < start {
			break
		}
		comment := strings.TrimSpace(block[start+3 : end])
		if notes != "" {
			notes += "; " + comment
		} else {
			notes = comment
		}
		block = block[:start] + block[end+4:]
	}

	// Extract [ref] tags → append to text as plain references
	for {
		start := strings.Index(block, "[ref]")
		if start < 0 {
			break
		}
		end := strings.Index(block, "[/ref]")
		if end < start {
			break
		}
		ref := strings.TrimSpace(block[start+5 : end])
		block = block[:start] + "см. " + ref + block[end+6:]
	}

	// Clean up: remove extra spaces, trim
	text = strings.TrimSpace(block)
	// Collapse multiple spaces
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	return labels, notes, text
}

func (d *bkrsDict) Lookup(simplified string) (*Entry, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	atomic.AddInt64(&d.stats.Total, 1)

	entry, ok := d.entries[simplified]
	if !ok {
		atomic.AddInt64(&d.stats.Misses, 1)
		return nil, fmt.Errorf("word %q not found in bkrs dictionary", simplified)
	}
	atomic.AddInt64(&d.stats.Hits, 1)
	return entry, nil
}

func (d *bkrsDict) LookupPinyin(simplified string) string {
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

func (d *bkrsDict) Stats() Stats {
	return Stats{
		Hits:   atomic.LoadInt64(&d.stats.Hits),
		Misses: atomic.LoadInt64(&d.stats.Misses),
		Total:  atomic.LoadInt64(&d.stats.Total),
	}
}

func (d *bkrsDict) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = nil
	return nil
}

// isGzip checks whether a seekable file starts with the gzip magic bytes.
func isGzip(f *os.File) bool {
	var magic [2]byte
	n, err := f.Read(magic[:])
	if err != nil || n < 2 {
		return false
	}
	return magic[0] == 0x1f && magic[1] == 0x8b
}
