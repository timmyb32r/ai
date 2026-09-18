package pipeline

import (
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/models"
	pinyinlib "github.com/criradio/server/internal/pinyin"
	"strings"
)

func (p *Processor) fillProbableReadings(words []models.WordEntry) {
	for i := range words {
		w := &words[i]
		if !hasUnknownReading(w.CharPinyin) {
			continue
		}
		chars := []rune(w.Text)
		// Prefer a whole-word CEDICT reading for multi-char words; if the word
		// is in neither BKRS nor CEDICT, fall back to per-character resolution.
		if len(chars) > 1 && p.fillFromCedict(w, chars) {
			continue
		}
		p.fillPerChar(w, chars)
	}
}

// attachCedictMeanings adds CC-CEDICT English glosses to each word (as a second
// dictionary source for the word popup). It never overwrites the primary
// (BKRS) translation — the two are shown side by side in the UI. No-op when
// CEDICT is not loaded (e.g. DICT=cedict mode, where CEDICT is already primary).
func (p *Processor) attachCedictMeanings(words []models.WordEntry) {
	if p.Cedict == nil {
		return
	}
	for i := range words {
		if entry, err := p.Cedict.Lookup(words[i].Text); err == nil {
			words[i].CedictMeanings = entry.Meanings
		}
	}
}

// hasUnknownReading reports whether any per-character syllable is the "?" marker.
func hasUnknownReading(charPinyin []string) bool {
	for _, s := range charPinyin {
		if s == "?" {
			return true
		}
	}
	return false
}

// fillFromCedict resolves a multi-character word's "?" readings using CEDICT's
// word-level pinyin (space-separated, one syllable per character). CEDICT gives
// the context-correct reading for the specific word (e.g. 天问 → "Tian1 wen4"),
// so the result is treated as certain (no uncertainty marker). Words missing
// from CEDICT — or whose pinyin does not align 1:1 with the characters — are
// left untouched.
// attachWiktionaryMeanings adds kaikki.org Wiktionary English glosses to each
// word (as a third dictionary tab, independent of BKRS and CC-CEDICT).
func (p *Processor) attachWiktionaryMeanings(words []models.WordEntry) {
	if p.Wiktionary == nil {
		return
	}
	for i := range words {
		if entry, err := p.Wiktionary.Lookup(words[i].Text); err == nil {
			words[i].WiktionaryMeanings = entry.Meanings
		}
	}
}

func (p *Processor) fillFromCedict(w *models.WordEntry, chars []rune) bool {
	if p.Cedict == nil {
		return false
	}
	entry, err := p.Cedict.Lookup(w.Text)
	if err != nil {
		return false
	}
	fields := strings.Fields(entry.Pinyin)
	if len(fields) != len(chars) {
		return false
	}
	syllables := make([]string, len(fields))
	for i, f := range fields {
		syl := pinyinlib.NumberedToDiacritic(f)
		if !pinyinlib.IsValidHieroglyphPinyin(syl) {
			return false // don't emit anything unless the whole word is clean
		}
		syllables[i] = syl
	}
	if len(w.CharPinyin) != len(syllables) {
		w.CharPinyin = make([]string, len(syllables))
	}
	copy(w.CharPinyin, syllables)
	w.CharPinyinUncertain = nil // CEDICT word reading is authoritative
	w.Pinyin = strings.Join(syllables, " ")
	return true
}

// fillPerChar resolves each remaining "?" position independently — the general
// fallback when neither BKRS nor CEDICT yields a whole-word reading (e.g. 一状,
// which is in no dictionary as a word). Per character:
//   - exactly one dictionary reading → set it deterministically (certain);
//   - several readings → most-probable Unihan reading, marked uncertain
//     (e.g. 一 → yī with a trailing "?" in the UI);
//   - unknown to both → keep "?".
//
// It also covers lone single-character words (的 → de + "?").
func (p *Processor) fillPerChar(w *models.WordEntry, chars []rune) {
	if len(w.CharPinyin) != len(chars) {
		return
	}
	uncertain := w.CharPinyinUncertain
	changed := false
	for i, ch := range chars {
		if w.CharPinyin[i] != "?" {
			continue
		}
		var readings []string
		if p.Dictionary != nil {
			readings = p.Dictionary.CharReadings(string(ch))
		}
		if len(readings) == 1 {
			// Unambiguous character — deterministic, no uncertainty marker.
			w.CharPinyin[i] = readings[0]
			changed = true
			continue
		}
		// Ambiguous (>1) or unknown (0) — take the most-probable Unihan reading.
		if p.Unihan == nil {
			continue
		}
		if reading, ok := p.Unihan.Lookup(ch); ok {
			w.CharPinyin[i] = reading.Pinyin
			if uncertain == nil {
				uncertain = make([]bool, len(chars))
			}
			uncertain[i] = true
			changed = true
		}
	}
	if uncertain != nil {
		w.CharPinyinUncertain = uncertain
	}
	// Rebuild the word-level pinyin from the resolved syllables when the source
	// was missing/ambiguous, so the full-line romanisation is clean too.
	if changed && (w.Pinyin == "" || w.Pinyin == "_" || w.Pinyin == "?" || strings.ContainsAny(w.Pinyin, ",;?")) {
		w.Pinyin = strings.Join(w.CharPinyin, " ")
	}
}

// sampleRMS estimates RMS amplitude from a sparse sample of the PCM buffer.
// Returns 0.0 for truly silent (all-zero) audio.
func resolveByContext(charIdx int, chars []rune, dict dictionary.Dictionary) string {
	target := string(chars[charIdx])
	readings := dict.CharReadings(target)
	if len(readings) <= 1 {
		return ""
	}
	// Try left+current window.
	if charIdx > 0 {
		sub := string(chars[charIdx-1 : charIdx+1])
		if entry, err := dict.Lookup(sub); err == nil && len(entry.CharPinyins) == 2 {
			for _, r := range readings {
				if entry.CharPinyins[1] == r {
					return r
				}
			}
		}
	}
	// Try current+right window.
	if charIdx < len(chars)-1 {
		sub := string(chars[charIdx : charIdx+2])
		if entry, err := dict.Lookup(sub); err == nil && len(entry.CharPinyins) == 2 {
			for _, r := range readings {
				if entry.CharPinyins[0] == r {
					return r
				}
			}
		}
	}
	return ""
}
