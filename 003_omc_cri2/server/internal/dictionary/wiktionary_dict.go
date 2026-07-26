package dictionary

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	pinyinlib "github.com/criradio/server/internal/pinyin"
)

// wiktionaryDict implements Dictionary backed by kaikki.org's pre-parsed
// Wiktionary JSONL dump (zh-extract.jsonl.gz).
type wiktionaryDict struct {
	entries     map[string]*Entry   // key: simplified Chinese word
	charPinyins map[string][]string // key: single character → pinyin readings
	stats       Stats
	mu          sync.RWMutex
}

// ── JSONL line structure ────────────────────────────────────────────────

type wiktionaryLine struct {
	Word     string `json:"word"`
	LangCode string `json:"lang_code"`
	Pos      string `json:"pos"`
	Sounds   []struct {
		ZhPron string `json:"zh_pron"`
		IPA    string `json:"ipa"`
	} `json:"sounds"`
	Senses []struct {
		Glosses  []string `json:"glosses"`
		Examples []struct {
			Text    string `json:"text"`
			Roman   string `json:"roman"`
			English string `json:"english"`
		} `json:"examples"`
		Tags []string `json:"tags"`
	} `json:"senses"`
}

// ── Constructor ─────────────────────────────────────────────────────────

// LoadWiktionary parses a kaikki.org JSONL dump (gzip-compressed) and
// returns a Dictionary backed by the extracted Chinese entries.
func LoadWiktionary(dumpPath string) (Dictionary, error) {
	f, err := os.Open(dumpPath)
	if err != nil {
		return nil, fmt.Errorf("wiktionary: open %s: %w", dumpPath, err)
	}
	defer f.Close()

	zipped := isGzipWiktionary(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("wiktionary: seek %s: %w", dumpPath, err)
	}

	var r io.Reader = f
	if zipped {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return nil, fmt.Errorf("wiktionary: gzip %s: %w", dumpPath, gerr)
		}
		defer gz.Close()
		r = gz
	}

	d := &wiktionaryDict{
		entries:     make(map[string]*Entry, 350000),
		charPinyins: make(map[string][]string, 20000),
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1*1024*1024), 10*1024*1024) // 1 MB initial, 10 MB max

	var line wiktionaryLine
	processed := 0
	chineseEntries := 0

	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if len(lineBytes) == 0 {
			continue
		}

		// Reset struct (reuse allocation).
		line.Word = ""
		line.LangCode = ""
		line.Pos = ""
		line.Sounds = line.Sounds[:0]
		line.Senses = line.Senses[:0]

		if err := json.Unmarshal(lineBytes, &line); err != nil {
			continue // skip malformed lines
		}

		processed++
		if processed%50000 == 0 {
			// Progress log — loading Wiktionary is a few seconds of work.
			_ = processed // compiler pacifier; real log would go to a logger
		}

		// Only Chinese entries.
		if line.LangCode != "zh" {
			continue
		}

		entry := parseWiktionaryLine(&line)
		if entry == nil {
			continue
		}

		// Index by simplified Chinese (the word field IS simplified).
		// First entry wins for duplicate words (different POS, same headword).
		if _, exists := d.entries[entry.Simplified]; !exists {
			d.entries[entry.Simplified] = entry
		} else {
			// Merge senses from additional entries (e.g. same word, different POS).
			if existing := d.entries[entry.Simplified]; len(entry.Senses) > 0 {
				existing.Senses = append(existing.Senses, entry.Senses...)
			}
		}
		chineseEntries++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("wiktionary: scan %s: %w", dumpPath, err)
	}

	// Build per-character pinyin map from single-character entries and
	// from multi-character entries with 1:1 syllable alignment.
	d.buildCharPinyins()

	return d, nil
}

// parseWiktionaryLine converts a single JSONL line into an Entry.
// Returns nil if the entry has no useful data (no pinyin, no meanings).
func parseWiktionaryLine(line *wiktionaryLine) *Entry {
	// Extract pinyin from sounds[].zh_pron — convert diacritics to numbered.
	pinyin := ""
	var charPinyins []string
	if len(line.Sounds) > 0 {
		for _, s := range line.Sounds {
			if s.ZhPron != "" {
				pinyin = diacriticWordToNumbered(s.ZhPron)
				break
			}
		}
	}

	// Extract glosses as flat Meanings.
	var meanings []string
	for _, sense := range line.Senses {
		for _, g := range sense.Glosses {
			g = strings.TrimSpace(g)
			if g != "" {
				meanings = append(meanings, g)
			}
		}
	}

	// Skip entries with no pinyin AND no meanings.
	if pinyin == "" && len(meanings) == 0 {
		return nil
	}

	// Build structured Senses.
	var senses []Sense
	for i, sense := range line.Senses {
		if len(sense.Glosses) == 0 {
			continue
		}
		text := strings.TrimSpace(strings.Join(sense.Glosses, "; "))
		if text == "" {
			continue
		}
		senses = append(senses, Sense{
			Number: i + 1,
			Labels: sense.Tags,
			Text:   text,
		})
	}

	// Per-character pinyin pre-compute: split if syllables == characters.
	chars := []rune(line.Word)
	if pinyin != "" && len(chars) > 0 {
		syllables := strings.Fields(pinyin)
		if len(syllables) == len(chars) {
			charPinyins = make([]string, len(chars))
			copy(charPinyins, syllables)
		}
	}

	_ = charPinyins // stored in entry during buildCharPinyins pass

	return &Entry{
		Traditional: line.Word, // Wiktionary doesn't separate trad/simp — use word for both
		Simplified:  line.Word,
		Pinyin:      pinyin,
		Meanings:    meanings,
		Senses:      senses,
	}
}

// ── Per-character pinyin map ────────────────────────────────────────────

// buildCharPinyins constructs the charPinyins map by scanning all loaded
// entries: single-character entries contribute their readings directly,
// and multi-character entries with exact 1:1 syllable-to-character alignment
// contribute each syllable for its corresponding character.
func (d *wiktionaryDict) buildCharPinyins() {
	for word, entry := range d.entries {
		chars := []rune(word)
		syllables := strings.Fields(entry.Pinyin)
		if len(syllables) != len(chars) {
			continue
		}
		for i, ch := range chars {
			syl := cleanSyllableWikt(syllables[i])
			if syl == "" || syl == "?" {
				continue
			}
			if !pinyinlib.IsValidHieroglyphPinyin(syl) {
				continue
			}
			chStr := string(ch)
			existing := d.charPinyins[chStr]
			// Dedup: don't add the same reading twice.
			found := false
			for _, e := range existing {
				if e == syl {
					found = true
					break
				}
			}
			if !found {
				d.charPinyins[chStr] = append(existing, syl)
			}
		}
	}
}

// ── Dictionary interface ────────────────────────────────────────────────

func (d *wiktionaryDict) Lookup(simplified string) (*Entry, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	atomic.AddInt64(&d.stats.Total, 1)

	entry, ok := d.entries[simplified]
	if !ok {
		atomic.AddInt64(&d.stats.Misses, 1)
		return nil, fmt.Errorf("word %q not found in wiktionary", simplified)
	}
	atomic.AddInt64(&d.stats.Hits, 1)

	// Split word pinyin into per-character syllables.
	entry.CharPinyins = splitWordPinyinWikt(simplified, entry.Pinyin, d.charPinyins)
	return entry, nil
}

func (d *wiktionaryDict) LookupPinyin(simplified string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	atomic.AddInt64(&d.stats.Total, 1)

	entry, ok := d.entries[simplified]
	if !ok {
		atomic.AddInt64(&d.stats.Misses, 1)
		return ""
	}
	atomic.AddInt64(&d.stats.Hits, 1)

	// Return only the first reading (strip comma/semicolon alternatives).
	if idx := strings.IndexAny(entry.Pinyin, ",;"); idx >= 0 {
		return strings.TrimSpace(entry.Pinyin[:idx])
	}
	return entry.Pinyin
}

func (d *wiktionaryDict) CharReadings(ch string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.charPinyins[ch]
}

func (d *wiktionaryDict) Stats() Stats {
	return Stats{
		Hits:   atomic.LoadInt64(&d.stats.Hits),
		Misses: atomic.LoadInt64(&d.stats.Misses),
		Total:  atomic.LoadInt64(&d.stats.Total),
	}
}

func (d *wiktionaryDict) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = nil
	d.charPinyins = nil
	return nil
}

// ── Pinyin: diacritic → numbered conversion ────────────────────────────

// diacriticWordToNumbered converts a space-separated diacritic-marked
// pinyin string (e.g. "lì zi") into numbered form ("li4 zi5").
// Neutral-tone syllables (no diacritic mark) are left without a tone digit.
func diacriticWordToNumbered(zhPron string) string {
	syllables := strings.Fields(zhPron)
	parts := make([]string, len(syllables))
	for i, syl := range syllables {
		parts[i] = diacriticToNumbered(syl)
	}
	return strings.Join(parts, " ")
}

// diacriticToNumbered converts a single diacritic-marked pinyin syllable
// (e.g. "lì", "wèn", "nǐ", "tā", "de") into numbered form ("li4", "wen4",
// "ni3", "ta1", "de5"). If the syllable already has a tone digit (1-5),
// it is returned unchanged. Neutral-tone syllables (no diacritic, no digit)
// get tone 5 appended, consistent with the BKRS convention.
func diacriticToNumbered(syl string) string {
	if syl == "" {
		return ""
	}
	runes := []rune(syl)
	// Already numbered — return as-is.
	for _, r := range runes {
		if r >= '1' && r <= '5' {
			return syl
		}
	}
	for i, r := range runes {
		tone := toneNumber(r)
		if tone > 0 {
			// Replace diacritic vowel with base vowel + tone digit.
			base := diacriticBase(r)
			prefix := string(runes[:i])
			suffix := string(runes[i+1:])
			return fmt.Sprintf("%s%c%s%d", prefix, base, suffix, tone)
		}
	}
	// No tone mark and no tone digit — neutral tone (tone 5).
	return syl + "5"
}

// toneNumber returns the tone number (1-4) for a diacritic vowel,
// or 0 if the rune is not a tone-marked vowel.
func toneNumber(r rune) int {
	switch r {
	case 'ā', 'á', 'ǎ', 'à':
		return toneFromAcute(r)
	case 'ē', 'é', 'ě', 'è':
		return toneFromAcute(r)
	case 'ī', 'í', 'ǐ', 'ì':
		return toneFromAcute(r)
	case 'ō', 'ó', 'ǒ', 'ò':
		return toneFromAcute(r)
	case 'ū', 'ú', 'ǔ', 'ù':
		return toneFromAcute(r)
	case 'ǖ', 'ǘ', 'ǚ', 'ǜ':
		return toneFromAcute(r)
	case 'ń', 'ň', 'ǹ':
		return toneFromAcute(r)
	default:
		return 0
	}
}

// toneFromAcute maps diacritic vowels to tone numbers based on their
// Unicode code point position within the four-tone sequence.
func toneFromAcute(r rune) int {
	switch r {
	case 'ā', 'ē', 'ī', 'ō', 'ū', 'ǖ':
		return 1
	case 'á', 'é', 'í', 'ó', 'ú', 'ǘ', 'ń':
		return 2
	case 'ǎ', 'ě', 'ǐ', 'ǒ', 'ǔ', 'ǚ', 'ň':
		return 3
	case 'à', 'è', 'ì', 'ò', 'ù', 'ǜ', 'ǹ':
		return 4
	default:
		return 0
	}
}

// diacriticBase returns the base ASCII vowel for a diacritic-marked vowel.
func diacriticBase(r rune) rune {
	switch r {
	case 'ā', 'á', 'ǎ', 'à':
		return 'a'
	case 'ē', 'é', 'ě', 'è':
		return 'e'
	case 'ī', 'í', 'ǐ', 'ì':
		return 'i'
	case 'ō', 'ó', 'ǒ', 'ò':
		return 'o'
	case 'ū', 'ú', 'ǔ', 'ù':
		return 'u'
	case 'ǖ', 'ǘ', 'ǚ', 'ǜ':
		return 'ü'
	case 'ń', 'ň', 'ǹ':
		return 'n'
	default:
		return r
	}
}

// ── Pinyin splitting for CharPinyins ────────────────────────────────────

// splitWordPinyinWikt splits a word's pinyin into per-character syllables.
// Wiktionary zh_pron is already space-separated in most cases, so this is
// simpler than the BKRS equivalent.
func splitWordPinyinWikt(word, pinyin string, charMap map[string][]string) []string {
	chars := []rune(word)
	n := len(chars)

	if n == 1 {
		// Single character: use the reading if it's a single valid syllable.
		if strings.ContainsAny(pinyin, ",;") {
			return []string{"?"}
		}
		fields := strings.Fields(pinyin)
		if len(fields) == 1 && pinyinlib.IsValidHieroglyphPinyin(fields[0]) {
			return []string{cleanSyllableWikt(fields[0])}
		}
		return []string{"?"}
	}

	// Multi-character: strip alternative readings (comma/semicolon).
	if idx := strings.IndexAny(pinyin, ",;"); idx >= 0 {
		pinyin = strings.TrimSpace(pinyin[:idx])
	}

	syllables := strings.Fields(pinyin)

	// A) Exact 1:1 alignment.
	if len(syllables) == n && allSingleSyllablesWikt(syllables) {
		result := make([]string, n)
		for i := range chars {
			result[i] = cleanSyllableWikt(syllables[i])
		}
		return result
	}

	// B) Try segmenting joined pinyin.
	joined := strings.Join(syllables, "")
	if seg, ok := pinyinlib.SegmentByCount(joined, n); ok {
		return seg
	}

	// C) Char-map alignment.
	if r := splitWithCharMapWikt(joined, chars, charMap); r != nil {
		return r
	}

	// D) Fallback: try only the first token.
	if len(syllables) > 1 {
		first := syllables[0]
		if seg, ok := pinyinlib.SegmentByCount(first, n); ok {
			return seg
		}
		if r := splitWithCharMapWikt(first, chars, charMap); r != nil {
			return r
		}
	}

	return unknownSyllablesWikt(n)
}

func cleanSyllableWikt(syl string) string {
	if strings.ContainsAny(syl, ",;") {
		return "?"
	}
	return syl
}

func allSingleSyllablesWikt(syllables []string) bool {
	for _, s := range syllables {
		if !pinyinlib.IsValidHieroglyphPinyin(s) {
			return false
		}
	}
	return true
}

func unknownSyllablesWikt(n int) []string {
	result := make([]string, n)
	for i := range result {
		result[i] = "?"
	}
	return result
}

// splitWithCharMapWikt aligns characters to readings from the charPinyins map.
func splitWithCharMapWikt(pinyin string, chars []rune, charMap map[string][]string) []string {
	result := make([]string, len(chars))
	remaining := pinyin
	for i, ch := range chars {
		candidates := charMap[string(ch)]
		if len(candidates) == 0 {
			return nil
		}
		best := ""
		for _, c := range candidates {
			plain := stripDiacriticsWikt(strings.TrimRight(c, "0123456789"))
			plainRem := stripDiacriticsWikt(strings.TrimRight(remaining, "0123456789"))
			if strings.HasPrefix(plainRem, plain) && len(c) > len(best) {
				best = c
			}
		}
		if best == "" {
			return nil
		}
		result[i] = best
		// Consume matching prefix from remaining.
		plainBest := strings.TrimRight(best, "0123456789")
		remRunes := []rune(remaining)
		bestRunes := []rune(plainBest)
		consume := 0
		bi := 0
		for consume < len(remRunes) && bi < len(bestRunes) {
			if stripDiacriticsWiktRune(remRunes[consume]) == stripDiacriticsWiktRune(bestRunes[bi]) {
				consume++
				bi++
			} else {
				break
			}
		}
		if consume < len(remRunes) && remRunes[consume] >= '1' && remRunes[consume] <= '5' {
			consume++
		}
		if consume > 0 {
			remaining = string(remRunes[consume:])
		} else {
			return nil
		}
	}
	if remaining != "" {
		return nil
	}
	return result
}

func stripDiacriticsWikt(s string) string {
	var b strings.Builder
	for _, c := range s {
		b.WriteRune(stripDiacriticsWiktRune(c))
	}
	return b.String()
}

func stripDiacriticsWiktRune(c rune) rune {
	switch c {
	case 'ā', 'á', 'ǎ', 'à':
		return 'a'
	case 'ē', 'é', 'ě', 'è':
		return 'e'
	case 'ī', 'í', 'ǐ', 'ì':
		return 'i'
	case 'ō', 'ó', 'ǒ', 'ò':
		return 'o'
	case 'ū', 'ú', 'ǔ', 'ù':
		return 'u'
	case 'ǖ', 'ǘ', 'ǚ', 'ǜ':
		return 'ü'
	case 'ń', 'ň', 'ǹ':
		return 'n'
	default:
		return c
	}
}

// ── Gzip detection ──────────────────────────────────────────────────────

func isGzipWiktionary(f *os.File) bool {
	var magic [2]byte
	n, err := f.Read(magic[:])
	if err != nil || n < 2 {
		return false
	}
	return magic[0] == 0x1f && magic[1] == 0x8b
}
