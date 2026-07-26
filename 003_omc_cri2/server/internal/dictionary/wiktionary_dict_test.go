package dictionary

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"testing"
)

// Test wiktionary JSONL parsing with a small fixture.
func TestWiktionaryParseLine(t *testing.T) {
	jsonLine := `{"word": "例子", "lang_code": "zh", "pos": "noun", "sounds": [{"zh_pron": "lì zi", "ipa": "/li⁵¹ d͡z̥z̩¹/"}], "senses": [{"glosses": ["example", "instance"], "tags": [], "examples": [{"text": "举个例子", "roman": "jǔ ge lì zi", "english": "give an example"}]}]}`

	var line wiktionaryLine
	if err := json.Unmarshal([]byte(jsonLine), &line); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if line.Word != "例子" {
		t.Errorf("word = %q, want 例子", line.Word)
	}
	if line.LangCode != "zh" {
		t.Errorf("lang_code = %q, want zh", line.LangCode)
	}
	if len(line.Sounds) != 1 || line.Sounds[0].ZhPron != "lì zi" {
		t.Errorf("zh_pron = %q, want lì zi", line.Sounds[0].ZhPron)
	}
	if len(line.Senses) != 1 || len(line.Senses[0].Glosses) != 2 {
		t.Errorf("glosses count = %d, want 2", len(line.Senses[0].Glosses))
	}

	entry := parseWiktionaryLine(&line)
	if entry == nil {
		t.Fatal("parseWiktionaryLine returned nil")
	}
	if entry.Simplified != "例子" {
		t.Errorf("Simplified = %q, want 例子", entry.Simplified)
	}
	if entry.Pinyin != "li4 zi5" {
		t.Errorf("Pinyin = %q, want li4 zi5", entry.Pinyin)
	}
	if len(entry.Meanings) != 2 {
		t.Errorf("Meanings = %v, want 2 entries", entry.Meanings)
	}
	if len(entry.Senses) != 1 {
		t.Errorf("Senses count = %d, want 1", len(entry.Senses))
	}
	if entry.Senses[0].Text != "example; instance" {
		t.Errorf("Senses[0].Text = %q, want 'example; instance'", entry.Senses[0].Text)
	}
}

func TestWiktionaryParseLine_NonChinese(t *testing.T) {
	jsonLine := `{"word": "example", "lang_code": "en", "pos": "noun", "sounds": [], "senses": []}`

	var line wiktionaryLine
	if err := json.Unmarshal([]byte(jsonLine), &line); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if line.LangCode != "en" {
		t.Errorf("lang_code = %q, want en", line.LangCode)
	}
}

func TestDiacriticToNumbered(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"lì", "li4"},
		{"wèn", "wen4"},
		{"nǐ", "ni3"},
		{"tā", "ta1"},
		{"mā", "ma1"},
		{"má", "ma2"},
		{"mǎ", "ma3"},
		{"mà", "ma4"},
		{"de", "de5"},      // neutral tone → 5
		{"zǐ", "zi3"},
		{"lǜ", "lü4"},
		{"nǚ", "nü3"},
		{"", ""},           // empty stays empty
		{"li4", "li4"},     // already numbered — no change
	}
	for _, tc := range tests {
		got := diacriticToNumbered(tc.input)
		if got != tc.expected {
			t.Errorf("diacriticToNumbered(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestDiacriticWordToNumbered(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"lì zi", "li4 zi5"},
		{"yī gè", "yi1 ge4"},
		{"nǐ hǎo", "ni3 hao3"},
		{"zhōng guó", "zhong1 guo2"},
		{"de", "de5"},
	}
	for _, tc := range tests {
		got := diacriticWordToNumbered(tc.input)
		if got != tc.expected {
			t.Errorf("diacriticWordToNumbered(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSplitWordPinyinWikt(t *testing.T) {
	tests := []struct {
		word   string
		pinyin string
		want   []string
	}{
		{"例子", "li4 zi5", []string{"li4", "zi5"}},
		{"一个", "yi1 ge4", []string{"yi1", "ge4"}},
		{"好", "hao3", []string{"hao3"}},
		{"什么", "shen2 me5", []string{"shen2", "me5"}},
		{"未知词", "", []string{"?", "?", "?"}},
	}
	for _, tc := range tests {
		got := splitWordPinyinWikt(tc.word, tc.pinyin, nil)
		if len(got) != len(tc.want) {
			t.Errorf("splitWordPinyinWikt(%q, %q) len = %d, want %d", tc.word, tc.pinyin, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitWordPinyinWikt(%q, %q)[%d] = %q, want %q", tc.word, tc.pinyin, i, got[i], tc.want[i])
			}
		}
	}
}

// Test loading a small gzip-JSONL fixture.
func TestLoadWiktionary_Fixture(t *testing.T) {
	// Build a small JSONL fixture in memory.
	lines := []string{
		`{"word": "例子", "lang_code": "zh", "pos": "noun", "sounds": [{"zh_pron": "lì zi"}], "senses": [{"glosses": ["example"]}]}`,
		`{"word": "一个", "lang_code": "zh", "pos": "det", "sounds": [{"zh_pron": "yī gè"}], "senses": [{"glosses": ["one", "a"]}]}`,
		`{"word": "good", "lang_code": "en", "pos": "adj", "sounds": [], "senses": [{"glosses": ["of high quality"]}]}`,
		`{"word": "好", "lang_code": "zh", "pos": "adj", "sounds": [{"zh_pron": "hǎo"}], "senses": [{"glosses": ["good"]}]}`,
		`{"word": "的", "lang_code": "zh", "pos": "part", "sounds": [{"zh_pron": "de"}], "senses": [{"glosses": ["possessive particle"]}]}`,
		``, // empty line — should be skipped
	}

	// Write as gzip file.
	testFile := t.TempDir() + "/test.jsonl.gz"
	f, err := os.Create(testFile)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	for _, line := range lines {
		gw.Write([]byte(line + "\n"))
	}
	gw.Close()
	f.Close()

	d, err := LoadWiktionary(testFile)
	if err != nil {
		t.Fatalf("LoadWiktionary: %v", err)
	}
	defer d.Close()

	// Check stats.
	stats := d.Stats()
	if stats.Total != 0 {
		t.Errorf("Stats after load: Total=%d, want 0", stats.Total)
	}

	// Lookup Chinese word.
	entry, err := d.Lookup("例子")
	if err != nil {
		t.Fatalf("Lookup 例子: %v", err)
	}
	if entry.Pinyin != "li4 zi5" {
		t.Errorf("例子 pinyin = %q, want li4 zi5", entry.Pinyin)
	}
	if len(entry.Meanings) != 1 || entry.Meanings[0] != "example" {
		t.Errorf("例子 meanings = %v, want [example]", entry.Meanings)
	}
	if entry.CharPinyins == nil || len(entry.CharPinyins) != 2 {
		t.Errorf("例子 CharPinyins = %v, want 2 syllables", entry.CharPinyins)
	}

	// Non-Chinese entry should not be indexed.
	_, err = d.Lookup("good")
	if err == nil {
		t.Error("Lookup 'good' should fail (English entry)")
	}

	// CharReadings — 的 should have reading "de5" from its zh_pron "de"
	readings := d.CharReadings("的")
	if len(readings) == 0 {
		t.Error("CharReadings 的 returned empty")
	}
	found := false
	for _, r := range readings {
		if r == "de5" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CharReadings 的 = %v, want [de5]", readings)
	}

	// Stats after lookups.
	stats2 := d.Stats()
	if stats2.Total < 2 {
		t.Errorf("Stats Total=%d, want at least 2", stats2.Total)
	}

	// Close.
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestWiktionaryLookupPinyin(t *testing.T) {
	// Reuse fixture test for quick check.
	testFile := t.TempDir() + "/test.jsonl.gz"
	f, _ := os.Create(testFile)
	gw := gzip.NewWriter(f)
	gw.Write([]byte(`{"word": "中国", "lang_code": "zh", "pos": "noun", "sounds": [{"zh_pron": "zhōng guó"}], "senses": [{"glosses": ["China"]}]}` + "\n"))
	gw.Close()
	f.Close()

	d, err := LoadWiktionary(testFile)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	pinyin := d.LookupPinyin("中国")
	if pinyin != "zhong1 guo2" {
		t.Errorf("LookupPinyin 中国 = %q, want zhong1 guo2", pinyin)
	}

	// Unknown word.
	pinyin = d.LookupPinyin("不存在")
	if pinyin != "" {
		t.Errorf("LookupPinyin 不存在 = %q, want empty", pinyin)
	}
}

func TestToneNumber(t *testing.T) {
	tests := []struct {
		r rune
		n int
	}{
		{'ā', 1}, {'á', 2}, {'ǎ', 3}, {'à', 4},
		{'ē', 1}, {'ě', 3},
		{'ō', 1}, {'ò', 4},
		{'ǖ', 1}, {'ǘ', 2}, {'ǚ', 3}, {'ǜ', 4},
		{'a', 0}, {'e', 0}, {' ', 0},
	}
	for _, tc := range tests {
		if got := toneNumber(tc.r); got != tc.n {
			t.Errorf("toneNumber(%c) = %d, want %d", tc.r, got, tc.n)
		}
	}
}
