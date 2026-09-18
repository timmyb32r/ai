package pipeline

import (
	"context"
	"fmt"
	"math"
	"testing"
	"unicode"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/models"
)

type textTestTokenizer func(string) ([]models.Token, error)

func (f textTestTokenizer) Segment(s string) ([]models.Token, error) { return f(s) }
func (textTestTokenizer) Close() error                               { return nil }

type textTestDictionary map[string]*dictionary.Entry

func (d textTestDictionary) Lookup(s string) (*dictionary.Entry, error) {
	if e := d[s]; e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("missing %s", s)
}
func (d textTestDictionary) LookupPinyin(s string) string {
	if e := d[s]; e != nil {
		return e.Pinyin
	}
	return ""
}
func (d textTestDictionary) CharReadings(s string) []string {
	if e := d[s]; e != nil && e.Pinyin != "" {
		return []string{e.Pinyin}
	}
	return nil
}
func (textTestDictionary) Stats() dictionary.Stats { return dictionary.Stats{} }
func (textTestDictionary) Close() error            { return nil }

func runeTextTokenizer(text string) ([]models.Token, error) {
	var tokens []models.Token
	for i, r := range []rune(text) {
		if !unicode.IsSpace(r) {
			tokens = append(tokens, models.Token{Text: string(r), CharStart: i, CharEnd: i + 1})
		}
	}
	return tokens, nil
}

func textTestProcessor() *Processor {
	return &Processor{Tokenizer: textTestTokenizer(runeTextTokenizer), Dictionary: textTestDictionary{}}
}

func TestProcessRejectsIncompleteHanLPCoverage(t *testing.T) {
	for _, tokens := range [][]models.Token{
		{{Text: "天", CharStart: 0, CharEnd: 1}},
		{{Text: "地", CharStart: 1, CharEnd: 2}},
		nil,
		{{Text: "天", CharStart: 0, CharEnd: 1}, {Text: "天", CharStart: 0, CharEnd: 1}},
	} {
		p := textTestProcessor()
		p.Tokenizer = textTestTokenizer(func(string) ([]models.Token, error) { return tokens, nil })
		_, err := p.Process(context.Background(), &asr.Result{Text: "天地", Tokens: []string{"天", "地"}, Timestamps: []float64{0.2, 0.7}}, 100, 3)
		if err == nil {
			t.Fatalf("accepted incomplete/inconsistent tokens: %+v", tokens)
		}
	}
}

func TestProcessAllowsWhitespaceOutsideHanLPTokens(t *testing.T) {
	p := textTestProcessor()
	seg, err := p.Process(context.Background(), &asr.Result{Text: "天 地", Tokens: []string{"天", " ", "地"}, Timestamps: []float64{0.2, 0.5, 0.7}}, 100, 3)
	if err != nil || seg.TextZh != "天 地" || len(seg.Words) != 2 {
		t.Fatalf("segment=%+v err=%v", seg, err)
	}
}

func TestProcessSubdividesMultiRuneModelTimingForHanLPWords(t *testing.T) {
	p := textTestProcessor()
	seg, err := p.Process(context.Background(), &asr.Result{Text: "你好世界", Tokens: []string{"你好", "世界"}, Timestamps: []float64{0.3, 2.8}}, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Words) != 4 {
		t.Fatal(seg.Words)
	}
	for i, w := range seg.Words {
		if w.EndSec <= w.StartSec || w.StartSec < 100 || w.EndSec > 103 {
			t.Fatalf("invalid word timing: %+v", w)
		}
		if i > 0 && w.StartSec < seg.Words[i-1].EndSec {
			t.Fatalf("overlap: %+v", seg.Words)
		}
	}
	if math.Abs(seg.Words[0].EndSec-101.55) > 1e-8 {
		t.Fatal("multi-rune token was not divided inside next timestamp interval", seg.Words)
	}
}

func TestProcessCrossBoundaryWordKeepsAuthoritativePronunciation(t *testing.T) {
	for _, useCedict := range []bool{false, true} {
		p := textTestProcessor()
		p.Tokenizer = textTestTokenizer(func(string) ([]models.Token, error) {
			return []models.Token{{Text: "重庆", CharStart: 0, CharEnd: 2}}, nil
		})
		dict := textTestDictionary{
			"重庆": {Pinyin: "chóng qìng", CharPinyins: []string{"chóng", "qìng"}, Meanings: []string{"Chongqing"}},
			"重":  {Pinyin: "zhòng", CharPinyins: []string{"zhòng"}, Meanings: []string{"heavy"}},
		}
		p.Dictionary = dict
		if useCedict {
			dict["重庆"] = &dictionary.Entry{Pinyin: "? ?", CharPinyins: []string{"?", "?"}}
			p.Cedict = textTestDictionary{"重庆": {Pinyin: "chong2 qing4"}}
		}
		seg, err := p.Process(context.Background(), &asr.Result{Text: "重庆", Tokens: []string{"重", "庆"}, Timestamps: []float64{2.9, 3.1}}, 100, 3)
		if err != nil {
			t.Fatal(err)
		}
		if seg.TextZh != "重" || len(seg.Words) != 1 || seg.Words[0].Text != "重" || seg.Words[0].CharEnd != 1 {
			t.Fatal(seg)
		}
		w := seg.Words[0]
		if len(w.CharPinyin) != 1 || w.CharPinyin[0] != "chóng" || w.Pinyin != "chóng" || w.Trans != "heavy" {
			t.Fatalf("lost full-word pronunciation or kept mismatched definition (CEDICT=%v): %+v", useCedict, w)
		}
		if dict["重庆"].CharPinyins[1] != map[bool]string{false: "qìng", true: "?"}[useCedict] {
			t.Fatal("mutated shared dictionary entry")
		}
	}
}

func TestProcessOverlapOwnsHalfOpenTimeAndPreservesRepeatedSyllables(t *testing.T) {
	p := textTestProcessor()
	first, err := p.Process(context.Background(), &asr.Result{Text: "哈哈", Tokens: []string{"哈", "哈"}, Timestamps: []float64{2.9, 3.1}}, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Process(context.Background(), &asr.Result{Text: "哈", Tokens: []string{"哈"}, Timestamps: []float64{0.1}}, 103, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.TextZh != "哈" || second.TextZh != "哈" || first.Words[0].StartSec >= second.Words[0].StartSec {
		t.Fatalf("lost a legitimate repeat or published lookahead twice: first=%+v second=%+v", first, second)
	}
	boundary, err := p.Process(context.Background(), &asr.Result{Text: "哈", Tokens: []string{"哈"}, Timestamps: []float64{3}}, 100, 3)
	if err != nil || boundary.HasContent || len(boundary.Words) != 0 {
		t.Fatalf("exact right boundary must belong to next segment: %+v, %v", boundary, err)
	}
}
