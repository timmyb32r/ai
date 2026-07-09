// Package unihan provides the most-probable Mandarin reading for a single
// Chinese character, derived from the Unicode Unihan database.
//
// It is used to fill in a plausible pinyin syllable for a lone character whose
// dictionary reading is ambiguous (multiple readings → "?"). CC-CEDICT cannot
// help here: it stores one line per reading with no frequency information.
// Unihan's kHanyuPinlu field, in contrast, lists each reading WITH a corpus
// frequency, e.g.:
//
//	U+7684  kHanyuPinlu  de(75596) dì(157) dí(84)   → de wins (99.7%)
//	U+7740  kHanyuPinlu  zhe(10643) zháo(545) ...    → zhe wins (93%)
//
// kMandarin (a single canonical reading) is used as a fallback when a
// character has no kHanyuPinlu frequencies.
package unihan

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	pinyinlib "github.com/criradio/server/internal/pinyin"
)

// Reading is the resolved most-probable reading for a character.
type Reading struct {
	Pinyin string  // diacritic syllable, e.g. "de", "zhe"
	Share  float64 // frequency share of this reading (0..1); 0 when unknown (kMandarin fallback)
	Source string  // "kHanyuPinlu" or "kMandarin"
}

// Resolver maps a rune to its most-probable reading.
type Resolver struct {
	table map[rune]Reading
}

// Load parses a Unihan_Readings.txt file (as shipped in the Unicode UCD
// Unihan.zip) and builds a resolver. A nil resolver is safe to use and always
// reports "not found", so callers can treat a missing file as "feature off".
func Load(path string) (*Resolver, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open unihan readings: %w", err)
	}
	defer f.Close()
	return parse(f)
}

// pinluCount accumulates frequency-weighted readings for one character.
type charAcc struct {
	pinlu    map[string]int // reading → frequency
	pinluTop string         // running argmax reading
	pinluSum int
	mandarin string // first kMandarin reading
}

func parse(r *os.File) (*Resolver, error) {
	accs := make(map[rune]*charAcc, 50000)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Format: U+XXXX<TAB>field<TAB>value
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		field := parts[1]
		if field != "kHanyuPinlu" && field != "kMandarin" {
			continue
		}
		ch, ok := parseCodepoint(parts[0])
		if !ok {
			continue
		}
		acc := accs[ch]
		if acc == nil {
			acc = &charAcc{pinlu: map[string]int{}}
			accs[ch] = acc
		}
		switch field {
		case "kHanyuPinlu":
			// tokens like "de(75596)"
			for _, tok := range strings.Fields(parts[2]) {
				syl, freq, ok := parsePinluToken(tok)
				if !ok || !pinyinlib.IsValidHierogliphPinyin(syl) {
					continue
				}
				acc.pinlu[syl] += freq
				acc.pinluSum += freq
				if acc.pinlu[syl] > acc.pinlu[acc.pinluTop] {
					acc.pinluTop = syl
				}
			}
		case "kMandarin":
			for _, syl := range strings.Fields(parts[2]) {
				if pinyinlib.IsValidHierogliphPinyin(syl) {
					acc.mandarin = syl
					break
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan unihan readings: %w", err)
	}

	table := make(map[rune]Reading, len(accs))
	for ch, acc := range accs {
		if acc.pinluTop != "" && acc.pinluSum > 0 {
			table[ch] = Reading{
				Pinyin: acc.pinluTop,
				Share:  float64(acc.pinlu[acc.pinluTop]) / float64(acc.pinluSum),
				Source: "kHanyuPinlu",
			}
		} else if acc.mandarin != "" {
			table[ch] = Reading{Pinyin: acc.mandarin, Share: 0, Source: "kMandarin"}
		}
	}
	return &Resolver{table: table}, nil
}

// Lookup returns the most-probable reading for ch, or ok=false if unknown.
// A nil resolver always returns ok=false.
func (r *Resolver) Lookup(ch rune) (Reading, bool) {
	if r == nil {
		return Reading{}, false
	}
	rd, ok := r.table[ch]
	return rd, ok
}

// Size reports how many characters have a resolved reading.
func (r *Resolver) Size() int {
	if r == nil {
		return 0
	}
	return len(r.table)
}

// parseCodepoint parses "U+7684" into a rune.
func parseCodepoint(s string) (rune, bool) {
	if !strings.HasPrefix(s, "U+") {
		return 0, false
	}
	n, err := strconv.ParseInt(s[2:], 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}

// parsePinluToken parses "de(75596)" into ("de", 75596).
func parsePinluToken(tok string) (string, int, bool) {
	open := strings.IndexByte(tok, '(')
	close := strings.IndexByte(tok, ')')
	if open <= 0 || close <= open {
		return "", 0, false
	}
	syl := tok[:open]
	freq, err := strconv.Atoi(tok[open+1 : close])
	if err != nil {
		return "", 0, false
	}
	return syl, freq, true
}
