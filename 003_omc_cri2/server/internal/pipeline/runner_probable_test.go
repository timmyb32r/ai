package pipeline

import (
	"os"
	"testing"

	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/unihan"
)

func testUnihan(t *testing.T) *unihan.Resolver {
	t.Helper()
	const fixture = "U+7684\tkHanyuPinlu\tde(75596) dì(157) dí(84)\n" +
		"U+7684\tkMandarin\tde\n" +
		"U+5730\tkHanyuPinlu\tde(7394) dì(4976)\n" +
		"U+4E86\tkHanyuPinlu\tle(30101) liǎo(654)\n"
	f := t.TempDir() + "/Unihan_Readings.txt"
	if err := os.WriteFile(f, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := unihan.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFillProbableReadings(t *testing.T) {
	p := &Pipeline{Unihan: testUnihan(t)}

	words := []models.WordEntry{
		{Text: "的", CharPinyin: []string{"?"}, Pinyin: "de, dí, dì"},    // single-char "?" → filled + uncertain
		{Text: "地", CharPinyin: []string{"?"}, Pinyin: "?"},             // ambiguous (~60%) still filled + uncertain
		{Text: "銀", CharPinyin: []string{"?"}, Pinyin: "?"},             // unknown to Unihan → stays "?"
		{Text: "我", CharPinyin: []string{"wǒ"}, Pinyin: "wǒ"},           // deterministic single char → untouched
		{Text: "什么", CharPinyin: []string{"?", "me"}, Pinyin: "shénme"}, // multi-char with "?" → out of scope, untouched
	}
	p.fillProbableReadings(words)

	// 的 → de, uncertain
	if got := words[0].CharPinyin[0]; got != "de" {
		t.Errorf("的: CharPinyin=%q, want de", got)
	}
	if len(words[0].CharPinyinUncertain) != 1 || !words[0].CharPinyinUncertain[0] {
		t.Errorf("的: uncertain=%v, want [true]", words[0].CharPinyinUncertain)
	}
	if words[0].Pinyin != "de" {
		t.Errorf("的: word Pinyin=%q, want de (cleaned)", words[0].Pinyin)
	}

	// 地 → de, uncertain (flag set even though ambiguous)
	if words[1].CharPinyin[0] != "de" || !words[1].CharPinyinUncertain[0] {
		t.Errorf("地: got %q uncertain=%v, want de/[true]", words[1].CharPinyin[0], words[1].CharPinyinUncertain)
	}

	// 銀 unknown → stays "?", no uncertain flag
	if words[2].CharPinyin[0] != "?" {
		t.Errorf("銀: CharPinyin=%q, want ?", words[2].CharPinyin[0])
	}
	if words[2].CharPinyinUncertain != nil {
		t.Errorf("銀: uncertain=%v, want nil", words[2].CharPinyinUncertain)
	}

	// 我 deterministic → untouched, no flag
	if words[3].CharPinyin[0] != "wǒ" || words[3].CharPinyinUncertain != nil {
		t.Errorf("我: got %q uncertain=%v, want wǒ/nil", words[3].CharPinyin[0], words[3].CharPinyinUncertain)
	}

	// 什么 multi-char → untouched (out of scope)
	if words[4].CharPinyin[0] != "?" || words[4].CharPinyinUncertain != nil {
		t.Errorf("什么: got %v uncertain=%v, want [? me]/nil", words[4].CharPinyin, words[4].CharPinyinUncertain)
	}
}

func TestFillProbableReadings_NilResolver(t *testing.T) {
	p := &Pipeline{Unihan: nil}
	words := []models.WordEntry{{Text: "的", CharPinyin: []string{"?"}, Pinyin: "?"}}
	p.fillProbableReadings(words) // must not panic
	if words[0].CharPinyin[0] != "?" {
		t.Errorf("nil resolver must leave '?' intact, got %q", words[0].CharPinyin[0])
	}
}
