package broadcast

import (
	"bufio"
	"log"
	"os"
	"strings"
	"sync"
	"unicode"
)

// cedictEntry holds the pinyin and first English definition for one CC-CEDICT
// entry, keyed by simplified Chinese word.
type cedictEntry struct {
	Pinyin  string
	English string
}

// Enricher adds pinyin romanization and English translation to SubtitleEvents.
// Both are looked up word-by-word in the CC-CEDICT dictionary.
//
// Dictionary loading is lazy (sync.Once) so server startup is not blocked.
// Until the dictionary is loaded, pinyin and English fields are left empty.
type Enricher struct {
	dict     map[string]cedictEntry
	dictMu   sync.RWMutex
	loadOnce sync.Once
	dictPath string
}

// NewEnricher creates an Enricher.  The CC-CEDICT dictionary at dictPath is
// loaded on first use, not during construction, so server startup is fast.
func NewEnricher(dictPath string) *Enricher {
	return &Enricher{dictPath: dictPath}
}

// Enrich populates the Pinyin and English fields of ev and its Words from
// CC-CEDICT.  It is safe to call before the dictionary is loaded (fields will
// be empty).  The first call blocks until the dictionary is loaded.
func (e *Enricher) Enrich(ev *SubtitleEvent) {
	// Load dictionary synchronously — the first call blocks until loaded,
	// subsequent calls return immediately (sync.Once).
	e.ensureDict()

	// Per-word pinyin and English from CC-CEDICT.
	runes := []rune(ev.TextZh)
	partsEn := make([]string, 0, len(ev.Words))
	partsPy := make([]string, 0, len(ev.Words))
	for i := range ev.Words {
		w := &ev.Words[i]
		wordText := string(runes[w.CharStart:w.CharEnd])
		entry := e.lookup(wordText)
		wordLen := w.CharEnd - w.CharStart // number of characters in this word
		if entry.Pinyin != "" {
			w.Pinyin = entry.Pinyin
			partsPy = append(partsPy, entry.Pinyin)
		} else {
			// Word not in dictionary — emit one empty syllable per
			// character so that per-character pinyin alignment
			// (charIdx → syllable) stays correct for the whole text.
			for range wordLen {
				partsPy = append(partsPy, "")
			}
		}
		if entry.English != "" {
			w.English = entry.English
			partsEn = append(partsEn, entry.English)
		}
	}
	ev.Pinyin = strings.Join(partsPy, " ")
	ev.English = strings.Join(partsEn, "; ")
}

// lookup returns the CC-CEDICT entry for a simplified Chinese word,
// or a zero entry if the word is not in the dictionary or the dictionary
// hasn't loaded.
func (e *Enricher) lookup(word string) cedictEntry {
	e.dictMu.RLock()
	defer e.dictMu.RUnlock()
	if e.dict == nil {
		return cedictEntry{}
	}
	return e.dict[word]
}

// ensureDict loads the CC-CEDICT dictionary exactly once.
func (e *Enricher) ensureDict() {
	e.loadOnce.Do(func() {
		if e.dictPath == "" {
			log.Printf("enricher: no CC-CEDICT path configured; enrichment disabled")
			return
		}
		m, err := loadCEDICT(e.dictPath)
		if err != nil {
			log.Printf("enricher: failed to load CC-CEDICT from %s: %v; enrichment disabled", e.dictPath, err)
			return
		}
		e.dictMu.Lock()
		e.dict = m
		e.dictMu.Unlock()
		log.Printf("enricher: CC-CEDICT loaded (%d entries)", len(m))
	})
}

// loadCEDICT parses a CC-CEDICT text file.
// Format per line: traditional simplified [pinyin] /definition1/definition2/
// Returns a map from simplified Chinese word to its pinyin and first English
// definition.
func loadCEDICT(path string) (map[string]cedictEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Diagnose empty / mis-encoded files early.
	if fi, err := f.Stat(); err == nil {
		log.Printf("loadCEDICT: file size = %d bytes", fi.Size())
		if fi.Size() == 0 {
			log.Printf("loadCEDICT: WARNING — file is empty; no entries will be loaded")
			return make(map[string]cedictEntry), nil
		}
		// Peek at first 200 bytes to confirm it looks like CC-CEDICT.
		peek := make([]byte, 200)
		n, _ := f.Read(peek)
		if n > 0 {
			log.Printf("loadCEDICT: first %d bytes: %q", n, string(peek[:n]))
		}
		// Seek back to beginning for the scanner.
		f.Seek(0, 0)
	}

	dict := make(map[string]cedictEntry, 120000) // CC-CEDICT has ~120K entries
	scanner := bufio.NewScanner(f)
	// CC-CEDICT has some very long lines (rich definitions); 64 KB default is
	// often too small and causes bufio.ErrTooLong.
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	var (
		totalLines int
		tooFew     int
		notHan     int
		noContent  int
		added      int
		sampleRej  [][]string // store first few rejected lines for diagnosis
	)
	const maxSamples = 5

	for scanner.Scan() {
		totalLines++
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		// Format: traditional simplified [pinyin] /defs/
		fields := strings.Fields(line)
		if len(fields) < 2 {
			tooFew++
			if len(sampleRej) < maxSamples {
				sampleRej = append(sampleRej, []string{"too-few-fields", line[:min(120, len(line))]})
			}
			continue
		}
		simplified := fields[1]
		// Must be Chinese characters (skip non-Chinese entries).
		if !isAllChinese(simplified) {
			notHan++
			if len(sampleRej) < maxSamples {
				sampleRej = append(sampleRej, []string{"not-han", simplified, line[:min(120, len(line))]})
			}
			continue
		}
		// Extract pinyin from [brackets] and first English definition.
		pinyin := extractPinyin(line)
		english := extractFirstDef(line)
		if pinyin == "" && english == "" {
			noContent++
			if len(sampleRej) < maxSamples {
				sampleRej = append(sampleRej, []string{"no-content", simplified, line[:min(120, len(line))]})
			}
			continue
		}
		dict[simplified] = cedictEntry{Pinyin: pinyin, English: english}
		added++
	}

	log.Printf("loadCEDICT: %d total, %d added, skipped: %d too-few-fields, %d not-han, %d no-content",
		totalLines, added, tooFew, notHan, noContent)
	for _, s := range sampleRej {
		log.Printf("loadCEDICT sample-reject [%s]: %q", s[0], s[1:])
	}
	if err := scanner.Err(); err != nil {
		log.Printf("loadCEDICT: scanner error: %v", err)
	}

	return dict, scanner.Err()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// extractPinyin extracts the pinyin from a CC-CEDICT line and converts
// numbered tones (zhi2) to diacritic marks (zhí).
// Pinyin appears between brackets: traditional simplified [pin1 yin1] /defs/
func extractPinyin(line string) string {
	start := strings.IndexByte(line, '[')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(line[start:], ']')
	if end < 0 {
		return ""
	}
	raw := line[start+1 : start+end]
	return numberedToDiacritic(raw)
}

// numberedToDiacritic converts pinyin with tone numbers to diacritic marks.
// Multi-syllable pinyin is space-separated: "ni3 hao3" → "nǐ hǎo".
// Each syllable is converted independently using standard tone-placement rules:
//   - 'a' or 'e' always gets the mark
//   - in 'ou', 'o' gets the mark
//   - otherwise the last vowel gets the mark
//   - neutral tone (5) just strips the digit
//   - CC-CEDICT "u:" is normalised to "ü" before conversion
func numberedToDiacritic(s string) string {
	parts := strings.Split(s, " ")
	for i, p := range parts {
		parts[i] = syllableToDiacritic(p)
	}
	return strings.Join(parts, " ")
}

// syllableToDiacritic converts a single pinyin syllable with tone number.
func syllableToDiacritic(s string) string {
	// Normalise alternative ü spellings to the composed character.
	s = strings.ReplaceAll(s, "u:", "ü")
	s = strings.ReplaceAll(s, "v", "ü")

	// Find the tone number (1-5) at or near the end.
	tone, tonePos := 0, -1
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c >= '1' && c <= '5' {
			tone = int(c - '0')
			tonePos = i
			break
		}
		if c < 'a' || c > 'z' {
			break // non-letter before digit — no tone
		}
	}
	if tone == 0 || tone == 5 {
		if tonePos >= 0 {
			return s[:tonePos] + s[tonePos+1:]
		}
		return s
	}

	// Find the vowel that carries the tone.
	idx, ch := findToneVowel(s[:tonePos])
	if idx < 0 {
		// No vowel found — just strip the digit.
		return s[:tonePos] + s[tonePos+1:]
	}

	toned := tonedVowel(ch, tone)
	if toned == 0 {
		return s[:tonePos] + s[tonePos+1:] // unknown vowel — strip digit
	}
	runes := []rune(s[:tonePos])
	runes[idx] = toned
	return string(runes) + s[tonePos+1:]
}

// findToneVowel finds the vowel in a syllable (without the tone digit)
// that should carry the diacritic mark.  Returns the rune index and the rune.
// Uses rune-level iteration throughout to handle multi-byte UTF-8 correctly.
func findToneVowel(s string) (int, rune) {
	runes := []rune(s)

	// Rule 1: 'a' or 'e' gets the mark.
	for i, r := range runes {
		if r == 'a' || r == 'e' {
			return i, r
		}
	}
	// Rule 2: in 'ou', 'o' gets the mark.
	for i := 0; i < len(runes)-1; i++ {
		if runes[i] == 'o' && runes[i+1] == 'u' {
			return i, 'o'
		}
	}
	// Rule 3: last vowel (a, e, i, o, u, ü) gets the mark.
	vowels := "aeiouü"
	for i := len(runes) - 1; i >= 0; i-- {
		for _, v := range vowels {
			if runes[i] == v {
				return i, v
			}
		}
	}
	return -1, 0
}

// tonedVowel returns the diacritic version of a vowel for the given tone.
func tonedVowel(v rune, tone int) rune {
	// Map vowel + tone to composed diacritic character.
	type vt struct {
		v rune
		t int
	}
	m := map[vt]rune{
		{'a', 1}: 'ā', {'a', 2}: 'á', {'a', 3}: 'ǎ', {'a', 4}: 'à',
		{'e', 1}: 'ē', {'e', 2}: 'é', {'e', 3}: 'ě', {'e', 4}: 'è',
		{'i', 1}: 'ī', {'i', 2}: 'í', {'i', 3}: 'ǐ', {'i', 4}: 'ì',
		{'o', 1}: 'ō', {'o', 2}: 'ó', {'o', 3}: 'ǒ', {'o', 4}: 'ò',
		{'u', 1}: 'ū', {'u', 2}: 'ú', {'u', 3}: 'ǔ', {'u', 4}: 'ù',
		{'ü', 1}: 'ǖ', {'ü', 2}: 'ǘ', {'ü', 3}: 'ǚ', {'ü', 4}: 'ǜ',
	}
	return m[vt{v, tone}]
}

func isAllChinese(s string) bool {
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return len(s) > 0
}

// extractFirstDef returns the first /definition/ from a CC-CEDICT line.
func extractFirstDef(line string) string {
	start := strings.IndexByte(line, '/')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(line[start+1:], '/')
	if end < 0 {
		return line[start+1:]
	}
	return line[start+1 : start+1+end]
}
