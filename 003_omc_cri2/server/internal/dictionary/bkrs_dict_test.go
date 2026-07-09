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
	if result[0] != "yi2" {
		t.Errorf("char 一: got %q, want yi2", result[0])
	}
	// Last syllable covers remaining chars
	if result[1] != "hui4" {
		t.Errorf("char 会: got %q, want hui4", result[1])
	}
	if result[2] != "hui4" {
		t.Errorf("char 儿: got %q, want hui4 (shares with 会)", result[2])
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
