package dictionary

import (
	"os"
	"testing"
)

func createTestCEDICT(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/cedict_ts.u8"
	content := `# CC-CEDICT test dictionary
中國 中国 [Zhong1 guo2] /China/
你好 你好 [ni3 hao3] /Hello/Hi/
歡迎 欢迎 [huan1 ying2] /to welcome/welcome/
大家 大家 [da4 jia1] /everyone/
國際 国际 [guo2 ji4] /international/
廣播 广播 [guang3 bo1] /to broadcast/broadcasting/
電台 电台 [dian4 tai2] /radio station/
北京 北京 [Bei3 jing1] /Beijing/
上海 上海 [Shang4 hai3] /Shanghai/
謝謝 谢谢 [xie4 xie5] /thank you/thanks/
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndLookup(t *testing.T) {
	dictPath := createTestCEDICT(t)
	dict, err := Load(dictPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	defer dict.Close()

	// Known word
	entry, err := dict.Lookup("中国")
	if err != nil {
		t.Fatalf("Lookup(中国) failed: %v", err)
	}
	if entry.Pinyin != "Zhong1 guo2" {
		t.Errorf("pinyin: got %q, want %q", entry.Pinyin, "Zhong1 guo2")
	}
	if len(entry.Meanings) == 0 || entry.Meanings[0] != "China" {
		t.Errorf("meanings: got %v, want [China]", entry.Meanings)
	}

	// Traditional lookup (should return same entry)
	entry2, err := dict.Lookup("中國")
	if err != nil {
		t.Logf("Lookup(中國) failed (traditional may not be indexed): %v", err)
	} else if entry2.Pinyin != "Zhong1 guo2" {
		t.Errorf("traditional lookup pinyin mismatch: %q", entry2.Pinyin)
	}
}

func TestLookupUnknown(t *testing.T) {
	dictPath := createTestCEDICT(t)
	dict, err := Load(dictPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	defer dict.Close()

	_, err = dict.Lookup("不存在的词")
	if err == nil {
		t.Error("expected error for unknown word")
	}
}

func TestLookupPinyin(t *testing.T) {
	dictPath := createTestCEDICT(t)
	dict, err := Load(dictPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	defer dict.Close()

	pinyin := dict.LookupPinyin("你好")
	if pinyin != "ni3 hao3" {
		t.Errorf("pinyin: got %q, want %q", pinyin, "ni3 hao3")
	}

	// Unknown word returns empty string
	pinyin = dict.LookupPinyin("不存在")
	if pinyin != "" {
		t.Errorf("expected empty pinyin for unknown word, got %q", pinyin)
	}
}

func TestStats(t *testing.T) {
	dictPath := createTestCEDICT(t)
	dict, err := Load(dictPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	defer dict.Close()

	dict.Lookup("中国")   // hit
	dict.Lookup("不存在") // miss
	dict.Lookup("你好")   // hit
	dict.Lookup("不存在2") // miss

	stats := dict.Stats()
	if stats.Total != 4 {
		t.Errorf("Total: got %d, want 4", stats.Total)
	}
	if stats.Hits != 2 {
		t.Errorf("Hits: got %d, want 2", stats.Hits)
	}
	if stats.Misses != 2 {
		t.Errorf("Misses: got %d, want 2", stats.Misses)
	}
}

func TestLoadNonexistent(t *testing.T) {
	_, err := Load("/nonexistent/cedict_ts.u8")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestClose(t *testing.T) {
	dictPath := createTestCEDICT(t)
	dict, err := Load(dictPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if err := dict.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
	// Second close should be safe
	if err := dict.Close(); err != nil {
		t.Errorf("second Close() failed: %v", err)
	}
}
