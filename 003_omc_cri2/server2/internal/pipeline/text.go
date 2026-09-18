package pipeline

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/tokenizer"
	"github.com/criradio/server/internal/unihan"
)

type Processor struct {
	Tokenizer  tokenizer.Tokenizer
	Dictionary dictionary.Dictionary
	Cedict     dictionary.Dictionary
	Wiktionary dictionary.Dictionary
	Unihan     *unihan.Resolver
}

// Process preserves the model's token boundaries before expanding timestamps to
// rune offsets. One model token is not necessarily one Chinese character.
func (p *Processor) Process(ctx context.Context, result *asr.Result, start, duration float64) (*models.TranscriptSegment, error) {
	if result == nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || math.IsNaN(start) || math.IsInf(start, 0) {
		return nil, fmt.Errorf("invalid transcript input")
	}
	text, stamps, err := alignedText(result)
	if err != nil {
		return nil, err
	}
	seg := &models.TranscriptSegment{TimelineStartSec: start, TimelineEndSec: start + duration, Words: []models.WordEntry{}}
	if text == "" {
		return seg, nil
	}
	var tokens []models.Token
	if tok, ok := p.Tokenizer.(interface {
		SegmentContext(context.Context, string) ([]models.Token, error)
	}); ok {
		tokens, err = tok.SegmentContext(ctx, text)
	} else {
		tokens, err = p.Tokenizer.Segment(text)
	}
	if err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	runes := []rune(text)
	// A valid subset is not a complete tokenization: the client renders Words,
	// so accepting a missing non-whitespace rune would silently hide speech.
	if err = validateTokenCoverage(tokens, runes); err != nil {
		return nil, err
	}
	stamps = spreadRuneTimestamps(stamps, duration)
	cut := 0
	for cut < len(stamps) && stamps[cut] < duration {
		cut++
	}
	seg.TextZh = string(runes[:cut])
	seg.RawTimestamps = stamps[:cut]
	seg.HasContent = seg.TextZh != ""
	for _, t := range tokens {
		if t.CharStart >= cut {
			break
		}
		w := p.wordForPrefix(t, min(t.CharEnd, cut), runes)
		w.StartSec = start + stamps[t.CharStart]
		end := duration
		if w.CharEnd < len(stamps) {
			end = min(duration, stamps[w.CharEnd])
		}
		w.EndSec = start + max(stamps[t.CharStart], end)
		if w.EndSec <= w.StartSec {
			return nil, fmt.Errorf("word has no representable positive timing interval")
		}
		seg.Words = append(seg.Words, w)
	}
	if seg.HasContent && len(seg.Words) == 0 && strings.TrimSpace(seg.TextZh) != "" {
		return nil, fmt.Errorf("tokenizer returned no words for speech")
	}
	p.fillProbableReadings(seg.Words)
	p.attachCedictMeanings(seg.Words)
	p.attachWiktionaryMeanings(seg.Words)
	var py, en []string
	for _, w := range seg.Words {
		if w.Pinyin != "" {
			py = append(py, w.Pinyin)
		}
		if w.Trans != "" {
			en = append(en, w.Trans)
		}
	}
	seg.TextPinyin = strings.Join(py, " ")
	seg.TextEn = strings.Join(en, " / ")
	return seg, nil
}

func validateTokenCoverage(tokens []models.Token, runes []rune) error {
	last := 0
	for _, t := range tokens {
		if t.CharStart < last || t.CharStart < 0 || t.CharEnd <= t.CharStart || t.CharEnd > len(runes) || string(runes[t.CharStart:t.CharEnd]) != t.Text {
			return fmt.Errorf("tokenizer returned inconsistent offsets")
		}
		for _, ch := range runes[last:t.CharStart] {
			if !unicode.IsSpace(ch) {
				return fmt.Errorf("tokenizer omitted non-whitespace text")
			}
		}
		last = t.CharEnd
	}
	for _, ch := range runes[last:] {
		if !unicode.IsSpace(ch) {
			return fmt.Errorf("tokenizer omitted non-whitespace text")
		}
	}
	return nil
}

// A model token can contain several runes which HanLP splits into words. Their
// individual times are not observed: distribute them proportionally inside the
// next observed token interval. The final token is bounded by the segment end.
// This does not remove text: the half-open segment boundary determines ownership
// of lookahead text; identical syllables at distinct times are always retained.
func spreadRuneTimestamps(stamps []float64, end float64) []float64 {
	out := append([]float64(nil), stamps...)
	for first := 0; first < len(stamps); {
		next := first + 1
		for next < len(stamps) && stamps[next] == stamps[first] {
			next++
		}
		limit := end
		if next < len(stamps) {
			limit = stamps[next]
		}
		if next-first > 1 && limit > stamps[first] {
			for i := first + 1; i < next; i++ {
				out[i] = stamps[first] + (limit-stamps[first])*float64(i-first)/float64(next-first)
			}
		}
		first = next
	}
	return out
}

// Resolve pronunciation with the full HanLP word before clipping it to this
// audio segment. Definitions still describe the displayed fragment, while
// aligned character readings retain word context (重庆 must keep chóng, not zhòng).
func (p *Processor) wordForPrefix(t models.Token, end int, runes []rune) models.WordEntry {
	full := p.word(t)
	if end == t.CharEnd {
		return full
	}
	resolved := []models.WordEntry{full}
	p.fillProbableReadings(resolved)
	full = resolved[0]
	clipped := t
	clipped.CharEnd = end
	clipped.Text = string(runes[t.CharStart:end])
	w := p.word(clipped)
	length := end - t.CharStart
	if len(full.CharPinyin) == t.CharEnd-t.CharStart {
		w.CharPinyin = append([]string(nil), full.CharPinyin[:length]...)
		if len(full.CharPinyinUncertain) == len(full.CharPinyin) {
			w.CharPinyinUncertain = append([]bool(nil), full.CharPinyinUncertain[:length]...)
		}
		w.Pinyin = strings.Join(w.CharPinyin, " ")
	}
	return w
}

func alignedText(r *asr.Result) (string, []float64, error) {
	if strings.TrimSpace(r.Text) == "" && len(r.Tokens) == 0 {
		return "", nil, nil
	}
	if len(r.Tokens) == 0 || len(r.Tokens) != len(r.Timestamps) {
		return "", nil, fmt.Errorf("speech missing aligned timestamps")
	}
	var b strings.Builder
	stamps := make([]float64, 0, len(r.Tokens))
	previous := ""
	prevTime := 0.0
	for i, token := range r.Tokens {
		t := r.Timestamps[i]
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || (i > 0 && t < prevTime) {
			return "", nil, fmt.Errorf("invalid model timestamp")
		}
		prevTime = t
		if token == "" {
			continue
		}
		if asciiWord(previous) && asciiWord(token) {
			b.WriteByte(' ')
			stamps = append(stamps, t)
		}
		b.WriteString(token)
		for range []rune(token) {
			stamps = append(stamps, t)
		}
		previous = token
	}
	return b.String(), stamps, nil
}

func asciiWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func (p *Processor) word(t models.Token) models.WordEntry {
	w := models.WordEntry{Text: t.Text, CharStart: t.CharStart, CharEnd: t.CharEnd}
	if entry, err := p.Dictionary.Lookup(t.Text); err == nil {
		w.Pinyin = entry.Pinyin
		w.CharPinyin = append([]string(nil), entry.CharPinyins...)
		if len(entry.Meanings) > 0 {
			w.Trans = entry.Meanings[0]
		}
		for _, s := range entry.Senses {
			w.Senses = append(w.Senses, models.WordSense{Number: s.Number, Labels: s.Labels, Text: s.Text, Notes: s.Notes})
		}
		return w
	}
	chars := []rune(t.Text)
	var parts []string
	for i, ch := range chars {
		readings := p.Dictionary.CharReadings(string(ch))
		reading := ""
		switch len(readings) {
		case 0:
		case 1:
			reading = readings[0]
		default:
			reading = resolveByContext(i, chars, p.Dictionary)
			if reading == "" {
				reading = p.Dictionary.LookupPinyin(string(ch))
				if reading == "" || strings.ContainsAny(reading, ",;") {
					reading = "?"
				}
			}
		}
		w.CharPinyin = append(w.CharPinyin, reading)
		if reading != "" {
			parts = append(parts, reading)
		}
	}
	w.Pinyin = strings.Join(parts, " ")
	return w
}
