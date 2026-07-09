package dictionary

import (
	"os"
	"testing"
)

func TestSplitWordPinyin_Direct1to1(t *testing.T) {
	charMap := map[string][]string{
		"一": {"yi1", "yi2", "yi4"},
		"方": {"fang1"},
		"面": {"mian4"},
	}

	result := splitWordPinyin("一方面", "yi1 fang1 mian4", charMap)
	if len(result) != 3 {
		t.Fatalf("expected 3 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "yi1" {
		t.Errorf("char 一: got %q, want yi1", result[0])
	}
	if result[1] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[1])
	}
	if result[2] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[2])
	}
}

func TestSplitWordPinyin_Unspaced(t *testing.T) {
	charMap := map[string][]string{
		"方": {"fang1"},
		"面": {"mian4"},
	}

	result := splitWordPinyin("方面", "fang1mian4", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[0])
	}
	if result[1] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[1])
	}
}

func TestSplitWordPinyin_UnspacedThreeChars(t *testing.T) {
	charMap := map[string][]string{
		"一": {"yi1", "yi2", "yi4"},
		"方": {"fang1"},
		"面": {"mian4"},
	}

	result := splitWordPinyin("一方面", "yi1fang1mian4", charMap)
	if len(result) != 3 {
		t.Fatalf("expected 3 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "yi1" {
		t.Errorf("char 一: got %q, want yi1", result[0])
	}
	if result[1] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[1])
	}
	if result[2] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[2])
	}
}

func TestSplitWordPinyin_NoCharMap_Fallback(t *testing.T) {
	// When charMap has no entries, un-spaced pinyin should fall through
	// gracefully (splitUnspacedPinyin returns nil → falls to < chars branch).
	charMap := map[string][]string{}

	result := splitWordPinyin("方面", "fang1 mian4", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[0])
	}
	if result[1] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[1])
	}
}

func TestSplitWordPinyin_SingleChar(t *testing.T) {
	charMap := map[string][]string{
		"的": {"de5", "di2", "di4"},
	}

	result := splitWordPinyin("的", "de5", charMap)
	if len(result) != 1 {
		t.Fatalf("expected 1 syllable, got %d: %v", len(result), result)
	}
	if result[0] != "de5" {
		t.Errorf("char 的: got %q, want de5", result[0])
	}
}

func TestSplitWordPinyin_FewerSyllables(t *testing.T) {
	// 一会儿: 3 chars, 2 syllables
	charMap := map[string][]string{
		"一": {"yi1", "yi2"},
		"会": {"hui4", "kuai4"},
		"儿": {"er2", "r5"},
	}

	result := splitWordPinyin("一会儿", "yi2 hui4", charMap)
	if len(result) != 3 {
		t.Fatalf("expected 3 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "yi1" && result[0] != "yi2" {
		t.Errorf("char 一: got %q, want yi1 or yi2", result[0])
	}
	if result[1] != "hui4" {
		t.Errorf("char 会: got %q, want hui4", result[1])
	}
	// The source pinyin "yi2 hui4" has only two syllables for three
	// characters — the 儿 (erhua) syllable is missing. An unresolved
	// character must be marked unknown, NOT filled with the previous
	// character's reading (which is exactly the whole-word-pinyin-leak bug).
	if result[2] != "?" {
		t.Errorf("char 儿: got %q, want ? (unresolved, must not copy 会's reading)", result[2])
	}
}

func TestSplitWordPinyin_UnspacedFallback_NoCharMap(t *testing.T) {
	// No charMap — should use regex-based syllable splitting.
	charMap := map[string][]string{}

	result := splitWordPinyin("方面", "fang1mian4", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[0])
	}
	if result[1] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[1])
	}
}

func TestSplitWordPinyin_UnspacedFallback_ThreeChars(t *testing.T) {
	charMap := map[string][]string{}
	result := splitWordPinyin("一方面", "yi1fang1mian4", charMap)
	if len(result) != 3 {
		t.Fatalf("expected 3 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "yi1" {
		t.Errorf("char 一: got %q, want yi1", result[0])
	}
	if result[1] != "fang1" {
		t.Errorf("char 方: got %q, want fang1", result[1])
	}
	if result[2] != "mian4" {
		t.Errorf("char 面: got %q, want mian4", result[2])
	}
}

func TestSplitWordPinyin_UnspacedNoTones(t *testing.T) {
	// "重要" with un-spaced, un-toned pinyin — must split using charMap
	// built from multi-char words (1:1 alignment).
	charMap := map[string][]string{
		"重": {"zhong4", "chong2"},
		"要": {"yao4", "yao1"},
	}

	result := splitWordPinyin("重要", "zhongyao", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "zhong4" {
		t.Errorf("char 重: got %q, want zhong4", result[0])
	}
	if result[1] != "yao4" {
		t.Errorf("char 要: got %q, want yao4", result[1])
	}
}

func TestLoadBKRS_CharPinyins_AfterLookup(t *testing.T) {
	// Write a mini BKRS dump to a temp file.
	dump := "" +
		"一\n" + "yi1\n" + "[m1]один[/m]\n" +
		"\n" +
		"方\n" + "fang1\n" + "[m1]сторона[/m]\n" +
		"\n" +
		"面\n" + "mian4\n" + "[m1]лицо[/m]\n" +
		"\n" +
		"方面\n" + "fang1mian4\n" + "[m1]сторона, аспект[/m]\n" +
		"\n" +
		"一方面\n" + "yi1fang1mian4\n" + "[m1]с одной стороны[/m]\n" +
		"\n" +
		"打响\n" + "da3xiang3\n" + "[m1]начать[/m]\n" +
		"\n" +
		// Words with spaced pinyin → build charPinyins for 重 and 要.
		"重量\n" + "zhong4 liang4\n" + "[m1]вес[/m]\n" +
		"\n" +
		"要求\n" + "yao1 qiu2\n" + "[m1]требовать[/m]\n" +
		"\n" +
		"必要\n" + "bi4 yao4\n" + "[m1]необходимый[/m]\n" +
		"\n" +
		// Word with UNSPACED untoned pinyin — needs charPinyins to split.
		"重要\n" + "zhongyao\n" + "[m1]важный[/m]\n" +
		"\n"

	tmpFile := t.TempDir() + "/test_bkrs.dump"
	if err := os.WriteFile(tmpFile, []byte(dump), 0644); err != nil {
		t.Fatal(err)
	}

	dict, err := LoadBKRS(tmpFile)
	if err != nil {
		t.Fatal("LoadBKRS:", err)
	}

	// Test 1: 方面 with un-spaced pinyin (2 chars, 1 syllable string)
	entry, err := dict.Lookup("方面")
	if err != nil {
		t.Fatal("Lookup 方面:", err)
	}
	if len(entry.CharPinyins) != 2 {
		t.Fatalf("方面: expected 2 char pinyins, got %d: %v", len(entry.CharPinyins), entry.CharPinyins)
	}
	if entry.CharPinyins[0] != "fang1" {
		t.Errorf("方面[0]: got %q, want fang1", entry.CharPinyins[0])
	}
	if entry.CharPinyins[1] != "mian4" {
		t.Errorf("方面[1]: got %q, want mian4", entry.CharPinyins[1])
	}

	// Test 2: 一方面 with un-spaced pinyin (3 chars)
	entry, err = dict.Lookup("一方面")
	if err != nil {
		t.Fatal("Lookup 一方面:", err)
	}
	if len(entry.CharPinyins) != 3 {
		t.Fatalf("一方面: expected 3 char pinyins, got %d: %v", len(entry.CharPinyins), entry.CharPinyins)
	}
	if entry.CharPinyins[0] != "yi1" {
		t.Errorf("一方面[0]: got %q, want yi1", entry.CharPinyins[0])
	}
	if entry.CharPinyins[1] != "fang1" {
		t.Errorf("一方面[1]: got %q, want fang1", entry.CharPinyins[1])
	}
	if entry.CharPinyins[2] != "mian4" {
		t.Errorf("一方面[2]: got %q, want mian4", entry.CharPinyins[2])
	}

	// Test 3: 打响 with un-spaced pinyin (2 chars)
	entry, err = dict.Lookup("打响")
	if err != nil {
		t.Fatal("Lookup 打响:", err)
	}
	if len(entry.CharPinyins) != 2 {
		t.Fatalf("打响: expected 2 char pinyins, got %d: %v", len(entry.CharPinyins), entry.CharPinyins)
	}
	if entry.CharPinyins[0] != "da3" {
		t.Errorf("打响[0]: got %q, want da3", entry.CharPinyins[0])
	}
	if entry.CharPinyins[1] != "xiang3" {
		t.Errorf("打响[1]: got %q, want xiang3", entry.CharPinyins[1])
	}

	// Test 4: 重要 with unspaced untoned pinyin — split via charMap from 重量+要求.
	entry, err = dict.Lookup("重要")
	if err != nil {
		t.Fatal("Lookup 重要:", err)
	}
	if len(entry.CharPinyins) != 2 {
		t.Fatalf("重要: expected 2 char pinyins, got %d: %v", len(entry.CharPinyins), entry.CharPinyins)
	}
	if entry.CharPinyins[0] != "zhong4" {
		t.Errorf("重要[0]: got %q, want zhong4", entry.CharPinyins[0])
	}
	// Both yao1 and yao4 are valid readings — split is what matters.
	if entry.CharPinyins[1] != "yao4" && entry.CharPinyins[1] != "yao1" {
		t.Errorf("重要[1]: got %q, want yao4 or yao1", entry.CharPinyins[1])
	}

	// Test 5: single char — should have 1 char pinyin
	entry, err = dict.Lookup("一")
	if err != nil {
		t.Fatal("Lookup 一:", err)
	}
	if len(entry.CharPinyins) != 1 {
		t.Fatalf("一: expected 1 char pinyin, got %d: %v", len(entry.CharPinyins), entry.CharPinyins)
	}
	if entry.CharPinyins[0] != "yi1" {
		t.Errorf("一[0]: got %q, want yi1", entry.CharPinyins[0])
	}
}

func TestSplitWordPinyin_CommaSeparatedReadings_SingleChar(t *testing.T) {
	// 度 has readings "du4, duo2" — should become "?".
	result := splitWordPinyin("度", "du4, duo2", nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 syllable, got %d: %v", len(result), result)
	}
	if result[0] != "?" {
		t.Errorf("got %q, want ? (ambiguous multi-reading)", result[0])
	}
}

func TestSplitWordPinyin_CommaSeparatedReadings_SingleCharNoSpace(t *testing.T) {
	result := splitWordPinyin("度", "du4,duo2", nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 syllable, got %d: %v", len(result), result)
	}
	if result[0] != "?" {
		t.Errorf("got %q, want ?", result[0])
	}
}

func TestCleanSyllable_Comma(t *testing.T) {
	if got := cleanSyllable("du4, duo2"); got != "?" {
		t.Errorf("comma reading: got %q, want ?", got)
	}
	if got := cleanSyllable("du4,duo2"); got != "?" {
		t.Errorf("comma no space: got %q, want ?", got)
	}
	if got := cleanSyllable("du4; duo2"); got != "?" {
		t.Errorf("semicolon: got %q, want ?", got)
	}
	if got := cleanSyllable("du4"); got != "du4" {
		t.Errorf("clean: got %q, want du4", got)
	}
	if got := cleanSyllable("zhong4"); got != "zhong4" {
		t.Errorf("clean: got %q, want zhong4", got)
	}
}

func TestSplitWordPinyin_MultiCharWithComma(t *testing.T) {
	// Two-char word where pinyin has comma → each comma-containing field becomes "?".
	result := splitWordPinyin("大度", "da4, dai4 du4", nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	// "da4," → "?"; "dai4" is the 2nd field of the first char's alternatives.
	// Fields: ["da4,", "dai4", "du4"]. 3 fields > 2 chars → result[0]="da4,", result[1]="dai4"
	if result[0] != "?" {
		t.Errorf("char 0: got %q, want ?", result[0])
	}
}

func TestSplitWordPinyin_UnspacedWithComma(t *testing.T) {
	// Unspaced pinyin with comma — should also be handled.
	result := splitWordPinyin("度", "du4,duo2", map[string][]string{})
	if len(result) != 1 {
		t.Fatalf("expected 1 syllable, got %d: %v", len(result), result)
	}
	if result[0] != "?" {
		t.Errorf("got %q, want ?", result[0])
	}
}

func TestSplitWordPinyin_Apostrophe_Separator(t *testing.T) {
	// 哈尔克岛 (ha1'er3ke4dao3) — apostrophe as syllable boundary.
	tests := []struct {
		name   string
		pinyin string
		want   []string
	}{
		{"ASCII apostrophe", "ha1'er3ke4dao3", []string{"ha1", "er3", "ke4", "dao3"}},
		{"smart apostrophe U+2019", "ha1’er3ke4dao3", []string{"ha1", "er3", "ke4", "dao3"}},
		{"no tone with apostrophe", "ha'er'ke'dao", []string{"ha", "er", "ke", "dao"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitWordPinyin("哈尔克岛", tt.pinyin, nil)
			if len(result) != 4 {
				t.Fatalf("expected 4 syllables, got %d: %v", len(result), result)
			}
			for i, want := range tt.want {
				if result[i] != want {
					t.Errorf("[%d]: got %q, want %q", i, result[i], want)
				}
			}
		})
	}
}

func TestSplitWordPinyin_Xian_Apostrophe(t *testing.T) {
	// 西安 (xi1'an1) — apostrophe prevents reading as "xian1".
	result := splitWordPinyin("西安", "xi1'an1", nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "xi1" {
		t.Errorf("[0]: got %q, want xi1", result[0])
	}
	if result[1] != "an1" {
		t.Errorf("[1]: got %q, want an1", result[1])
	}
}

func TestSplitWordPinyin_SmartApostropheUnspaced(t *testing.T) {
	// Unicode smart apostrophe between syllables.
	result := splitWordPinyin("西安", "xi1’an1", nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "xi1" {
		t.Errorf("[0]: got %q, want xi1", result[0])
	}
	if result[1] != "an1" {
		t.Errorf("[1]: got %q, want an1", result[1])
	}
}

func TestSplitWordPinyin_DashSeparated(t *testing.T) {
	// 科技新闻 (ke1-ji4-xin1-wen2) — dash as syllable separator.
	result := splitWordPinyin("科技新闻", "ke1-ji4-xin1-wen2", nil)
	if len(result) != 4 {
		t.Fatalf("expected 4 syllables, got %d: %v", len(result), result)
	}
	want := []string{"ke1", "ji4", "xin1", "wen2"}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

func TestSplitWordPinyin_DashNoTones(t *testing.T) {
	result := splitWordPinyin("科技新闻", "ke-ji-xin-wen", nil)
	if len(result) != 4 {
		t.Fatalf("expected 4 syllables, got %d: %v", len(result), result)
	}
	want := []string{"ke", "ji", "xin", "wen"}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

func TestSplitWordPinyin_MixedDashAndSpace(t *testing.T) {
	result := splitWordPinyin("科技新闻", "ke1-ji4 xin1-wen2", nil)
	if len(result) != 4 {
		t.Fatalf("expected 4 syllables, got %d: %v", len(result), result)
	}
	want := []string{"ke1", "ji4", "xin1", "wen2"}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

func TestSplitWordPinyin_LiAn_Unspaced(t *testing.T) {
	// 离岸 (li2an4) — must split as li2 + an4, not l + i2an4.
	charMap := map[string][]string{
		"离": {"li2"},
		"岸": {"an4"},
	}
	result := splitWordPinyin("离岸", "li2an4", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "li2" {
		t.Errorf("[0]: got %q, want li2", result[0])
	}
	if result[1] != "an4" {
		t.Errorf("[1]: got %q, want an4", result[1])
	}
}

func TestSplitWordPinyin_ZhongYao_Unspaced(t *testing.T) {
	// 重要 (zhongyao) — unspaced without tones, must split via charMap.
	charMap := map[string][]string{
		"重": {"zhong4", "chong2"},
		"要": {"yao4", "yao1"},
	}
	result := splitWordPinyin("重要", "zhongyao", charMap)
	if len(result) != 2 {
		t.Fatalf("expected 2 syllables, got %d: %v", len(result), result)
	}
	if result[0] != "zhong4" {
		t.Errorf("[0]: got %q, want zhong4", result[0])
	}
	// Both yao4 and yao1 are valid — split is what matters.
	if result[1] != "yao4" && result[1] != "yao1" {
		t.Errorf("[1]: got %q, want yao4 or yao1", result[1])
	}
}

func TestSplitWordPinyin_DiacriticVowels(t *testing.T) {
	// 他们 with diacritic pinyin (tāmen) — BKRS uses macrons, not tone numbers.
	charMap := map[string][]string{
		"他": {"tā"},
		"们": {"mén", "men"},
		"我": {"wǒ"},
	}

	tests := []struct {
		name   string
		pinyin string
		want   []string
	}{
		{"tā men", "tā men", []string{"tā", "men"}},
		// The segmenter preserves the source's own tone on each syllable.
		{"tāmén segmented", "tāmén", []string{"tā", "mén"}},
		{"tāmen segmented", "tāmen", []string{"tā", "men"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chars := []rune("他们")
			result := splitWordPinyin(string(chars), tt.pinyin, charMap)
			if len(result) != len(tt.want) {
				t.Fatalf("expected %d syllables, got %d: %v", len(tt.want), len(result), result)
			}
			for i, w := range tt.want {
				if result[i] != w {
					t.Errorf("[%d]: got %q, want %q", i, result[i], w)
				}
			}
		})
	}
}

func TestLookupPinyin_StripsComma(t *testing.T) {
	// Simulate: single-char entry with comma-separated readings.
	dump := "有\n" + "you3, you4\n" + "[m1]иметь[/m]\n\n"
	tmpFile := t.TempDir() + "/test.dump"
	os.WriteFile(tmpFile, []byte(dump), 0644)

	dict, err := LoadBKRS(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	// LookupPinyin should return just "you3", not "you3, you4".
	got := dict.LookupPinyin("有")
	if got != "you3" {
		t.Errorf("got %q, want you3", got)
	}

	// Lookup should still have full CharPinyins.
	entry, err := dict.Lookup("有")
	if err != nil {
		t.Fatal(err)
	}
	// Single char with comma readings → "?".
	if len(entry.CharPinyins) == 1 && entry.CharPinyins[0] != "?" {
		t.Errorf("CharPinyins[0]: got %q, want ? (ambiguous multi-reading)", entry.CharPinyins[0])
	}
}

func TestSplitWordPinyin_GroupedSyllables(t *testing.T) {
	// 美国政府 → BKRS pinyin "meiguo zhengfu" (grouped by sub-word).
	charMap := map[string][]string{
		"美": {"mei3"},
		"国": {"guo2"},
		"政": {"zheng4"},
		"府": {"fu3"},
	}

	result := splitWordPinyin("美国政府", "meiguo zhengfu", charMap)
	if len(result) != 4 {
		t.Fatalf("expected 4 syllables, got %d: %v", len(result), result)
	}
	want := []string{"mei3", "guo2", "zheng4", "fu3"}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

func TestCleanPinyin_StripsMarkup(t *testing.T) {
	tests := []struct{ in, want string }{
		{"xiàng; xiang; [c][i]в именах[/c]shàng", "xiàng; xiang; shàng"},
		{"zhuó, zhāo, zháo, zhe", "zhuó, zhāo, zháo, zhe"},
		{"de, dí, dì, dī", "de, dí, dì, dī"},
		{"  huán; hái; xuán  ", "huán; hái; xuán"},
	}
	for _, tt := range tests {
		got := cleanPinyin(tt.in)
		if got != tt.want {
			t.Errorf("cleanPinyin(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBKRSRecord_CleansPinyinMarkup(t *testing.T) {
	// 向 — pinyin line has BKRS markup embedded.
	entry := parseBKRSRecord("向",
		"xiàng; xiang; [c][i]в coбcтв. имeнax тakжe[/c] [c][/i][/c]shàng",
		"[m1]направление[/m]")
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Pinyin != "xiàng; xiang; shàng" {
		t.Errorf("Pinyin: got %q, want 'xiàng; xiang; shàng'", entry.Pinyin)
	}
}

func TestParseBKRSRecord_Typical(t *testing.T) {
	entry := parseBKRSRecord("方面", "fang1 mian4", "[m1]сторона, аспект[/m] [m2][p]перен.[/p]грань[/m]")

	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Simplified != "方面" {
		t.Errorf("Simplified: got %q, want 方面", entry.Simplified)
	}
	if entry.Pinyin != "fang1 mian4" {
		t.Errorf("Pinyin: got %q, want fang1 mian4", entry.Pinyin)
	}
	if len(entry.Senses) != 2 {
		t.Fatalf("expected 2 senses, got %d", len(entry.Senses))
	}
	if entry.Senses[0].Number != 1 {
		t.Errorf("Sense[0].Number: got %d, want 1", entry.Senses[0].Number)
	}
	if entry.Senses[1].Number != 2 {
		t.Errorf("Sense[1].Number: got %d, want 2", entry.Senses[1].Number)
	}
	if len(entry.Senses[1].Labels) != 1 || entry.Senses[1].Labels[0] != "перен." {
		t.Errorf("Sense[1].Labels: got %v, want [перен.]", entry.Senses[1].Labels)
	}
}

// TestSplitWordPinyin_MultiReadingChar is a unit-level regression for the
// whole-word-pinyin-leak bug: when a character has MULTIPLE readings it must
// still appear in the char map, so unspaced diacritic word pinyin splits
// per-character instead of duplicating the whole word onto every character.
func TestSplitWordPinyin_MultiReadingChar(t *testing.T) {
	// 拉 has several readings; before the fix it was absent from the char map,
	// so 土拉 ("tǔlā") collapsed to ["tǔlā", "tǔlā"].
	charMap := map[string][]string{
		"土": {"tǔ", "tù"},
		"拉": {"lā", "lá", "là", "lǎ"},
	}
	result := splitWordPinyin("土拉", "tǔlā", charMap)
	want := []string{"tǔ", "lā"}
	if len(result) != len(want) {
		t.Fatalf("expected %d syllables, got %d: %v", len(want), len(result), result)
	}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, result[i], w)
		}
	}
}

// TestLoadBKRS_MultiReadingCharMap is the end-to-end regression covering the
// reported cases (呵护 → he/hu, 他们 → ta/men). It exercises the full LoadBKRS
// char-map construction from single-character entries whose pinyin lists
// multiple comma-separated readings.
func TestLoadBKRS_MultiReadingCharMap(t *testing.T) {
	dump := "" +
		"呵\n hē, ā, kē\n[m1]дуть[/m]\n\n" +
		"护\n hù\n[m1]защищать[/m]\n\n" +
		"呵护\n hēhù\n[m1]оберегать[/m]\n\n" +
		"他\n tā\n[m1]он[/m]\n\n" +
		"们\n mén, men\n[m1]суффикс мн. ч.[/m]\n\n" +
		"他们\n tāmen\n[m1]они[/m]\n\n" +
		"拉\n lā, lá, là, lǎ\n[m1]тянуть[/m]\n\n" +
		"土\n tǔ\n[m1]земля[/m]\n\n" +
		"土拉\n tǔlā\n[m1]Тула[/m]\n\n"

	tmpFile := t.TempDir() + "/multi.dump"
	if err := os.WriteFile(tmpFile, []byte(dump), 0644); err != nil {
		t.Fatal(err)
	}
	dict, err := LoadBKRS(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		word string
		want []string
	}{
		{"呵护", []string{"hē", "hù"}},
		{"他们", []string{"tā", "men"}},
		{"土拉", []string{"tǔ", "lā"}},
	}
	for _, tc := range cases {
		entry, err := dict.Lookup(tc.word)
		if err != nil {
			t.Fatalf("%s: lookup failed: %v", tc.word, err)
		}
		if len(entry.CharPinyins) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.word, entry.CharPinyins, tc.want)
		}
		for i, w := range tc.want {
			cp := entry.CharPinyins[i]
			// 们 legitimately has two readings; accept either.
			if tc.word == "他们" && i == 1 {
				if cp != "men" && cp != "mén" {
					t.Errorf("%s[%d]: got %q, want men/mén", tc.word, i, cp)
				}
				continue
			}
			if cp != w {
				t.Errorf("%s[%d]: got %q, want %q", tc.word, i, cp, w)
			}
			// The core bug: a per-character syllable must never equal the
			// stripped whole-word pinyin.
			if cp == "hēhù" || cp == "tāmen" || cp == "tǔlā" {
				t.Errorf("%s[%d]: whole-word pinyin leaked onto character: %q", tc.word, i, cp)
			}
		}
	}
}
