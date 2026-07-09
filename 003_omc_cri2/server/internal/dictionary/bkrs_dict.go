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

	pinyinlib "github.com/criradio/server/internal/pinyin"
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
	entries     map[string]*Entry   // key: simplified Chinese word
	charPinyins map[string][]string // key: single character → possible pinyin readings
	stats       Stats
	mu          sync.RWMutex
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
	// isGzip reads 2 bytes to check magic — always rewind after.
	gzipped := isGzip(f)
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek bkrs dump: %w", err)
	}
	var reader io.Reader = f
	if gzipped {
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

	// Build character→pinyins map from single-character entries AND
	// from multi-character entries with space-separated pinyin (1:1 alignment).
	d.charPinyins = make(map[string][]string, 20000)
	addReading := func(ch, syl string) {
		syl = strings.Trim(syl, " \t.,;()[]")
		// Only register genuine single syllables — this rejects markup, glosses
		// AND multi-syllable strings (e.g. a whole-word reading "kōngfáng" that
		// would otherwise pollute the map for 空).
		if !pinyinlib.IsValidHierogliphPinyin(syl) {
			return
		}
		for _, existing := range d.charPinyins[ch] {
			if existing == syl {
				return
			}
		}
		d.charPinyins[ch] = append(d.charPinyins[ch], syl)
	}
	for word, entry := range d.entries {
		chars := []rune(word)
		if len(chars) == 1 {
			// Single-character entry: the pinyin field may list several
			// alternative readings separated by commas/semicolons
			// (e.g. 拉 → "lā, lá, là, lǎ"). Register EVERY reading — otherwise
			// multi-reading characters never enter the map, and words that
			// contain them cannot be split per-character (the whole-word
			// pinyin then leaks onto each character).
			for _, syl := range splitReadings(entry.Pinyin) {
				addReading(string(chars[0]), syl)
			}
			continue
		}
		// Multi-character entry with 1:1 space-separated pinyin
		// (e.g. "fang1 mian4") — align each syllable to its character. Skip
		// entries whose pinyin lists comma/semicolon-separated ALTERNATIVE
		// whole-word readings (e.g. "kòngfáng, kōngfáng"): those fields are not
		// a per-character syllable sequence.
		if strings.ContainsAny(entry.Pinyin, ",;") {
			continue
		}
		syllables := strings.Fields(entry.Pinyin)
		if len(syllables) == len(chars) {
			for i, ch := range chars {
				addReading(string(ch), syllables[i])
			}
		}
	}

	return d, nil
}

// parseBKRSRecord parses one BKRS record (headword, pinyin, body) into an Entry.
func parseBKRSRecord(headword, pinyin, body string) *Entry {
	if headword == "" || body == "" {
		return nil
	}

	// Normalise pinyin: strip BKRS body markup that leaks into pinyin line
	// (e.g. "xiàng; xiang; [c][i]в именах также[/c] [c][/i][/c]shàng").
	pinyin = cleanPinyin(pinyin)

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

// indexRunes returns the index of the first occurrence of substr in s,
// or -1 if not found. Uses rune-based comparison (safe for UTF-8).
func indexRunes(s, substr []rune) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for k := 0; k < len(substr); k++ {
			if s[i+k] != substr[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// cleanPinyin strips BKRS markup tags (and their content) that leak into pinyin lines.
// Example: "xiàng; xiang; [c][i]в именах[/i][/c]shàng" → "xiàng; xiang; shàng"
func cleanPinyin(p string) string {
	p = strings.TrimSpace(p)
	// Remove paired tags with their content: [c]...[/c], [i]...[/i], [ref]...[/ref], [ex]...[/ex]
	for _, tag := range []string{"c", "i", "ref", "ex"} {
		for {
			start := strings.Index(p, "["+tag+"]")
			if start < 0 {
				break
			}
			end := strings.Index(p, "[/"+tag+"]")
			if end < start {
				break
			}
			p = p[:start] + p[end+len("[/"+tag+"]"):]
		}
	}
	// Remove unpaired closing tags: [/c], [/i], [/p], [/ref], [/b], [/ex]
	for _, tag := range []string{"[/c]", "[/i]", "[/p]", "[/ref]", "[/b]", "[/ex]", "[/*]", "[*]", "[p]", "[b]"} {
		p = strings.ReplaceAll(p, tag, "")
	}
	// Remove parenthesised alternative readings / notes, e.g. "gōngqiǎo(què)"
	// → "gōngqiǎo". These are secondary pronunciations, not part of the
	// primary syllable sequence.
	p = removeParenthes(p)
	p = strings.Join(strings.Fields(p), " ")
	return p
}

// removeParenthes drops balanced parenthetical groups (ASCII and full-width).
func removeParenthes(p string) string {
	var b strings.Builder
	depth := 0
	for _, r := range p {
		switch r {
		case '(', '（':
			depth++
		case ')', '）':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	if depth != 0 {
		// Unbalanced — fall back to the original to avoid dropping everything.
		return p
	}
	return b.String()
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
		num   int
		start int // position of '[' in [mN]
		pos   int // position after [mN] (content start)
		end   int // position after [/m]
	}
	var markers []marker

	// Scan for [mN]...[/m] blocks.
	i := 0
	runes := []rune(body)
	for i < len(runes) {
		if i+2 < len(runes) && runes[i] == '[' && runes[i+1] == 'm' {
			j := i + 2
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			if j > i+2 && j < len(runes) && runes[j] == ']' {
				numStr := string(runes[i+2 : j])
				num, err := strconv.Atoi(numStr)
				if err == nil {
					endTag := indexRunes(runes[j+1:], []rune("[/m]"))
					if endTag >= 0 {
						markers = append(markers, marker{
							num:   num,
							start: i,
							pos:   j + 1,
							end:   j + 1 + endTag + len("[/m]"),
						})
						i = j + 1 + endTag + len("[/m]")
						continue
					}
				}
			}
		}
		i++
	}

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
		// Take content up to the [/m] close tag for this block.
		contentEnd := blockEnd
		if m.end <= blockEnd {
			contentEnd = m.end - len("[/m]") // exclude [/m] itself
		}
		if contentEnd < m.pos {
			contentEnd = m.pos
		}
		block := string(runes[m.pos:contentEnd])

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
	if len(markers) > 0 && markers[0].start > 0 {
		preamble := strings.TrimSpace(string(runes[:markers[0].start]))
		if preamble != "" {
			labels, notes, text := extractBKRSTags(preamble)
			if text != "" {
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

	// Split word pinyin into per-character syllables with disambiguation.
	entry.CharPinyins = splitWordPinyin(simplified, entry.Pinyin, d.charPinyins)
	return entry, nil
}

// splitReadings splits a single character's pinyin field into its individual
// alternative readings. BKRS lists them separated by commas/semicolons (and
// occasionally slashes/whitespace), e.g. 拉 → "lā, lá, là, lǎ". Each resulting
// token is one candidate syllable for that character.
func splitReadings(pinyin string) []string {
	return strings.FieldsFunc(pinyin, func(r rune) bool {
		return r == ',' || r == ';' || r == '/' || r == ' ' || r == '\t'
	})
}

// cleanSyllable returns "?" if the syllable contains comma/semicolon
// (indicating multiple alternative readings), otherwise returns it unchanged.
func cleanSyllable(syl string) string {
	if strings.ContainsAny(syl, ",;") {
		return "?"
	}
	return syl
}

// splitWordPinyin splits a word's pinyin string into per-character syllables.
// The word's pinyin is treated as the ground truth: it is aligned or segmented
// into exactly one syllable per character. Positions that cannot be resolved
// become "?" — the whole-word pinyin is never leaked onto a character.
func splitWordPinyin(word, pinyin string, charMap map[string][]string) []string {
	chars := []rune(word)
	n := len(chars)

	// Single character: it must have exactly ONE reading, and that reading must
	// be a real syllable. Multiple readings (comma/semicolon OR several
	// space-separated tokens) are ambiguous → "?"; junk / "_" → "?".
	if n == 1 {
		if strings.ContainsAny(pinyin, ",;") {
			return []string{"?"}
		}
		fields := strings.Fields(strings.ReplaceAll(pinyin, "-", " "))
		if len(fields) == 1 && pinyinlib.IsValidHierogliphPinyin(fields[0]) {
			return []string{cleanSyllable(fields[0])}
		}
		return []string{"?"}
	}

	// Comma/semicolon separate ALTERNATIVE readings of the whole word
	// (e.g. "kòngdì, kōngdì"). They are NOT a per-character syllable sequence,
	// so we keep only the first reading before splitting.
	if idx := strings.IndexAny(pinyin, ",;"); idx >= 0 {
		pinyin = strings.TrimSpace(pinyin[:idx])
	}
	// Dashes act as syllable separators (e.g. "ke1-ji4-xin1-wen2").
	pinyin = strings.ReplaceAll(pinyin, "-", " ")
	syllables := strings.Fields(pinyin)

	// A) Exact 1:1 spacing where every token is itself a single syllable
	//    (e.g. "fang1 mian4", "tā men").
	if len(syllables) == n && allSingleSyllables(syllables) {
		result := make([]string, n)
		for i := range chars {
			result[i] = cleanSyllable(syllables[i])
		}
		return result
	}

	// B) Segment the whole pinyin into exactly n syllables. Handles run-together
	//    pinyin ("yánchǐ") and sub-word grouping ("meiguo zhengfu").
	joined := strings.Join(syllables, "")
	//    When the source has NO tone information, char-map alignment recovers
	//    tones from the dictionary's own per-character readings. When the source
	//    is already toned we skip it, since the structural segmenter preserves
	//    the source's exact tone (char-map matching is tone-insensitive and
	//    could otherwise substitute the wrong reading).
	if !hasToneInfo(joined) {
		if r := splitWithCharMap(joined, chars, charMap, false); r != nil {
			return r
		}
	}
	//    Structural maximal-munch segmentation honouring space/apostrophe
	//    boundaries — the fix for diacritic pinyin with no tone-digit marks.
	if seg, ok := pinyinlib.SegmentByCount(pinyin, n); ok {
		return seg
	}
	//    Toned source that the segmenter could not split — try char-map anyway.
	if r := splitWithCharMap(joined, chars, charMap, false); r != nil {
		return r
	}

	// C) The tokens are ALTERNATIVE whole-word readings separated by spaces
	//    (e.g. "tīngdèng chéngchéng"): each token already spans every character.
	//    Take the first token and split it into n syllables.
	if len(syllables) > 1 {
		first := syllables[0]
		if r := splitWithCharMap(first, chars, charMap, false); r != nil {
			return r
		}
		if seg, ok := pinyinlib.SegmentByCount(first, n); ok {
			return seg
		}
	}

	// D) Numbered-pattern splitting, then a partial char-map alignment for the
	//    elided-syllable case (e.g. erhua 儿 in "yi2hui4" → [yi2, hui4, ?]).
	if r := splitBySyllablePattern(joined, n); r != nil {
		return r
	}
	if r := splitWithCharMap(joined, chars, charMap, true); r != nil {
		return r
	}
	// Genuinely unsplittable — mark unknown rather than leak the whole word.
	return unknownSyllables(n)
}

// hasToneInfo reports whether the pinyin carries tone information — a tone
// digit (1-5) or a diacritic-marked vowel.
func hasToneInfo(pinyin string) bool {
	for _, r := range pinyin {
		if r >= '1' && r <= '5' {
			return true
		}
		if isToneVowelRune(r) {
			return true
		}
	}
	return false
}

// isToneVowelRune reports whether r is a tone-marked pinyin vowel.
func isToneVowelRune(r rune) bool {
	switch r {
	case 'ā', 'á', 'ǎ', 'à',
		'ē', 'é', 'ě', 'è',
		'ī', 'í', 'ǐ', 'ì',
		'ō', 'ó', 'ǒ', 'ò',
		'ū', 'ú', 'ǔ', 'ù',
		'ǖ', 'ǘ', 'ǚ', 'ǜ',
		'ń', 'ň', 'ǹ':
		return true
	}
	return false
}

// allSingleSyllables reports whether every token is exactly one valid syllable.
func allSingleSyllables(syllables []string) bool {
	for _, s := range syllables {
		if !pinyinlib.IsValidHierogliphPinyin(s) {
			return false
		}
	}
	return true
}

// unknownSyllables returns a slice of n "?" markers.
func unknownSyllables(n int) []string {
	result := make([]string, n)
	for i := range result {
		result[i] = "?"
	}
	return result
}

// splitWithCharMap aligns the characters of a word to their readings by
// consuming the (space-joined) pinyin left to right. When partial is true and
// the pinyin runs out before every character is matched, the remaining
// characters are marked "?" (the elided-syllable case, e.g. erhua 儿 in
// "一会儿"/"yi2hui4"); when partial is false the alignment must be exact.
func splitWithCharMap(pinyin string, chars []rune, charMap map[string][]string, partial bool) []string {
	result := make([]string, len(chars))
	remaining := pinyin
	for i, ch := range chars {
		if remaining == "" && partial {
			for j := i; j < len(chars); j++ {
				result[j] = "?"
			}
			return result
		}
		candidates := charMap[string(ch)]
		if len(candidates) == 0 {
			return nil
		}
		best := ""
		for _, c := range candidates {
			plain := stripDiacritics(strings.TrimRight(c, "0123456789"))
			plainRem := stripDiacritics(strings.TrimRight(remaining, "0123456789"))
			if strings.HasPrefix(plainRem, plain) && len(c) > len(best) {
				best = c
			}
		}
		if best == "" {
			return nil
		}
		result[i] = best
		// Consume the matching prefix from remaining.
		// We can't use len(best) directly because remaining may have
		// diacritics while best has ASCII+tone. Instead, scan remaining
		// for the first non-matching character.
		plainBest := strings.TrimRight(best, "0123456789")
		plainRunes := []rune(plainBest)
		remRunes := []rune(remaining)
		consume := 0
		plainIdx := 0
		for consume < len(remRunes) && plainIdx < len(plainRunes) {
			rc := stripDiacriticsRune(remRunes[consume])
			pc := stripDiacriticsRune(plainRunes[plainIdx])
			if rc == pc {
				consume++
				plainIdx++
			} else {
				break
			}
		}
		// Also skip trailing tone digit in remaining after the matched prefix.
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

func splitBySyllablePattern(pinyin string, charCount int) []string {
	var syllables []string
	i := 0
	runes := []rune(pinyin)
	for i < len(runes) {
		// Skip leading apostrophes (syllable separators).
		for i < len(runes) && isApostrophe(runes[i]) {
			i++
		}
		start := i
		for i < len(runes) && isPinyinLetter(runes[i]) {
			i++
		}
		if i < len(runes) && runes[i] >= '1' && runes[i] <= '5' {
			i++
			syllables = append(syllables, string(runes[start:i]))
			// Skip trailing apostrophe after tone digit.
			if i < len(runes) && isApostrophe(runes[i]) {
				i++
			}
		} else if i > start {
			// Letters without tone digit.
			syllables = append(syllables, string(runes[start:i]))
			// Skip trailing apostrophe.
			if i < len(runes) && isApostrophe(runes[i]) {
				i++
			}
		} else {
			i++
		}
	}
	if len(syllables) == charCount {
		// Every piece must be a single valid syllable. Without tone digits this
		// splitter treats a whole diacritic run (e.g. "gōngqiǎo") as one token,
		// so we reject such bogus multi-syllable pieces instead of emitting them.
		for _, s := range syllables {
			if !pinyinlib.IsValidHierogliphPinyin(s) {
				return nil
			}
		}
		for i, s := range syllables {
			syllables[i] = cleanSyllable(s)
		}
		return syllables
	}
	return nil
}

func isPinyinLetter(c rune) bool {
	if (c >= 'a' && c <= 'z') || c == 'ü' || c == 'v' || c == ':' {
		return true
	}
	// Diacritic-marked pinyin vowels: à-ǜ range covers all tone-marked
	// a, e, i, o, u, ü variants used in Hanyu Pinyin.
	return (c >= 0x00E0 && c <= 0x01DC) && c != 0x00F0 && c != 0x00F7 && c != 0x00FE
}

// isApostrophe returns true for apostrophe-like characters used as
// pinyin syllable separators (e.g. Xī'ān → xi1'an1).
func isApostrophe(c rune) bool {
	return c == '\'' || c == '’' || c == '‘' || c == 'ʼ'
}

// stripDiacriticsRune converts a single diacritic vowel to its base form.
func stripDiacriticsRune(c rune) rune {
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
	default:
		return c
	}
}

// stripDiacritics removes tone diacritics from pinyin vowels,
// converting e.g. "mén" → "men" for comparison purposes.
func stripDiacritics(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch c {
		case 'ā', 'á', 'ǎ', 'à':
			b.WriteRune('a')
		case 'ē', 'é', 'ě', 'è':
			b.WriteRune('e')
		case 'ī', 'í', 'ǐ', 'ì':
			b.WriteRune('i')
		case 'ō', 'ó', 'ǒ', 'ò':
			b.WriteRune('o')
		case 'ū', 'ú', 'ǔ', 'ù':
			b.WriteRune('u')
		case 'ǖ', 'ǘ', 'ǚ', 'ǜ':
			b.WriteRune('ü')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
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
	// Return first reading only (strip comma-separated alternatives).
	pinyin := entry.Pinyin
	if idx := strings.IndexAny(pinyin, ",;"); idx >= 0 {
		pinyin = strings.TrimSpace(pinyin[:idx])
	}
	return pinyin
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

func (d *bkrsDict) CharReadings(ch string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.charPinyins[ch]
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
