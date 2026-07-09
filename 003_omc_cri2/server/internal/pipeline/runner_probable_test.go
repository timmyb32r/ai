package pipeline

import (
	"os"
	"testing"

	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/unihan"
)

func testUnihan(t *testing.T) *unihan.Resolver {
	t.Helper()
	const fixture = "U+7684\tkHanyuPinlu\tde(75596) dì(157) dí(84)\n" +
		"U+7684\tkMandarin\tde\n" +
		"U+5730\tkHanyuPinlu\tde(7394) dì(4976)\n" +
		"U+4E86\tkHanyuPinlu\tle(30101) liǎo(654)\n" +
		"U+4E00\tkHanyuPinlu\tyī(84490) yì(2) yí(1)\n" // 一
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
		{Text: "什么", CharPinyin: []string{"?", "me"}, Pinyin: "shénme"}, // 什 unknown to this fixture + no dict → stays "?"
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

// TestFillProbableReadings_EndToEndWithDict drives the real production path:
// a multi-reading single character (的) is looked up in BKRS, where
// splitWordPinyin collapses its ambiguous reading to "?", and the pipeline then
// fills it from Unihan. This is exactly what produces "de?" on screen (server
// emits "de" + uncertain flag; the client appends the "?").
func TestFillProbableReadings_EndToEndWithDict(t *testing.T) {
	dump := "" +
		"的\n de, dí, dì, dī\n[m1]притяжательная частица[/m]\n\n" +
		"是\n shì\n[m1]быть[/m]\n\n"
	df := t.TempDir() + "/bkrs.dump"
	if err := os.WriteFile(df, []byte(dump), 0644); err != nil {
		t.Fatal(err)
	}
	dict, err := dictionary.LoadBKRS(df)
	if err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Unihan: testUnihan(t)}

	// 的 — dictionary reading is ambiguous → "?" from splitWordPinyin.
	de, err := dict.Lookup("的")
	if err != nil {
		t.Fatal(err)
	}
	if len(de.CharPinyins) != 1 || de.CharPinyins[0] != "?" {
		t.Fatalf("的: dict CharPinyins=%v, want [?] (ambiguous)", de.CharPinyins)
	}
	deWords := []models.WordEntry{{
		Text: "的", CharPinyin: append([]string{}, de.CharPinyins...), Pinyin: de.Pinyin,
	}}
	p.fillProbableReadings(deWords)
	if deWords[0].CharPinyin[0] != "de" {
		t.Errorf("的: after fill CharPinyin=%q, want de", deWords[0].CharPinyin[0])
	}
	if len(deWords[0].CharPinyinUncertain) != 1 || !deWords[0].CharPinyinUncertain[0] {
		t.Errorf("的: uncertain=%v, want [true]", deWords[0].CharPinyinUncertain)
	}

	// 是 — deterministic single reading, must stay untouched and unflagged.
	shi, err := dict.Lookup("是")
	if err != nil {
		t.Fatal(err)
	}
	shiWords := []models.WordEntry{{
		Text: "是", CharPinyin: append([]string{}, shi.CharPinyins...), Pinyin: shi.Pinyin,
	}}
	p.fillProbableReadings(shiWords)
	if shiWords[0].CharPinyin[0] != "shì" || shiWords[0].CharPinyinUncertain != nil {
		t.Errorf("是: got %q uncertain=%v, want shì/nil", shiWords[0].CharPinyin[0], shiWords[0].CharPinyinUncertain)
	}
}

// TestFillProbableReadings_CedictFallback covers the 天问 case: BKRS has the
// word but with no pinyin, so its per-char readings are "?". CEDICT provides the
// context-correct word-level pinyin, which is split 1:1 per character with no
// uncertainty marker.
func TestFillProbableReadings_CedictFallback(t *testing.T) {
	cedictData := "天問 天问 [Tian1 wen4] /Tianwen (Mars mission)/\n"
	cf := t.TempDir() + "/cedict.u8"
	if err := os.WriteFile(cf, []byte(cedictData), 0644); err != nil {
		t.Fatal(err)
	}
	cd, err := dictionary.Load(cf)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{Cedict: cd, Unihan: testUnihan(t)}

	// 天问 — resolved from CEDICT, certain (no flag).
	tw := []models.WordEntry{{Text: "天问", CharPinyin: []string{"?", "?"}, Pinyin: "_"}}
	p.fillProbableReadings(tw)
	if got := tw[0].CharPinyin; len(got) != 2 || got[0] != "tiān" || got[1] != "wèn" {
		t.Errorf("天问: CharPinyin=%v, want [tiān wèn]", got)
	}
	if tw[0].CharPinyinUncertain != nil {
		t.Errorf("天问: uncertain=%v, want nil (CEDICT is authoritative)", tw[0].CharPinyinUncertain)
	}
	if tw[0].Pinyin != "tiān wèn" {
		t.Errorf("天问: word Pinyin=%q, want 'tiān wèn'", tw[0].Pinyin)
	}

	// A multi-char word absent from CEDICT keeps its "?".
	other := []models.WordEntry{{Text: "嫦娥", CharPinyin: []string{"?", "?"}, Pinyin: "_"}}
	p.fillProbableReadings(other)
	if other[0].CharPinyin[0] != "?" || other[0].CharPinyin[1] != "?" {
		t.Errorf("嫦娥: got %v, want [? ?] (not in CEDICT)", other[0].CharPinyin)
	}
}

// TestFillPerChar_DeterministicAndUnihan covers 一状: a "word" present in no
// dictionary (BKRS stub with pinyin "_", absent from CEDICT). Per character:
// 状 has one reading → deterministic zhuàng (certain); 一 has several → Unihan
// top yī (uncertain).
func TestFillPerChar_DeterministicAndUnihan(t *testing.T) {
	dump := "" +
		"状\n zhuàng\n[m1]вид[/m]\n\n" +
		"一\n yī, yì, yí\n[m1]один[/m]\n\n" +
		"一状\n _\n[m1](не слово)[/m]\n\n"
	df := t.TempDir() + "/bkrs.dump"
	if err := os.WriteFile(df, []byte(dump), 0644); err != nil {
		t.Fatal(err)
	}
	dict, err := dictionary.LoadBKRS(df)
	if err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Dictionary: dict, Unihan: testUnihan(t)} // Cedict nil

	e, err := dict.Lookup("一状")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.CharPinyins) != 2 || e.CharPinyins[0] != "?" || e.CharPinyins[1] != "?" {
		t.Fatalf("一状: dict CharPinyins=%v, want [? ?]", e.CharPinyins)
	}
	words := []models.WordEntry{{
		Text: "一状", CharPinyin: append([]string{}, e.CharPinyins...), Pinyin: e.Pinyin,
	}}
	p.fillProbableReadings(words)

	if got := words[0].CharPinyin; len(got) != 2 || got[0] != "yī" || got[1] != "zhuàng" {
		t.Errorf("一状: CharPinyin=%v, want [yī zhuàng]", got)
	}
	unc := words[0].CharPinyinUncertain
	if len(unc) != 2 || !unc[0] || unc[1] {
		t.Errorf("一状: uncertain=%v, want [true false] (一 guessed, 状 certain)", unc)
	}
	if words[0].Pinyin != "yī zhuàng" {
		t.Errorf("一状: word Pinyin=%q, want 'yī zhuàng'", words[0].Pinyin)
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
