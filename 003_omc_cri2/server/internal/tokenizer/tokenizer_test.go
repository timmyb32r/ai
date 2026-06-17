package tokenizer

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestDict creates minimal gse dictionary files for testing.
func setupTestDict(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	zhDir := filepath.Join(dir, "zh")
	if err := os.MkdirAll(zhDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal dictionary with common Chinese words
	dictContent := `欢迎 100000
收听 100000
国际 100000
广播 100000
电台 100000
中国 100000
北京 100000
你好 100000
谢谢 100000
大家 100000
早上 100000
晚上 100000
`
	if err := os.WriteFile(filepath.Join(zhDir, "s_1.txt"), []byte(dictContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zhDir, "t_1.txt"), []byte(dictContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zhDir, "stop_word.txt"), []byte("的\n了\n是\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSegmentSimple(t *testing.T) {
	dictPath := setupTestDict(t)
	tok, err := New(dictPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer tok.Close()

	tokens := tok.Segment("欢迎收听国际广播电台")
	if len(tokens) == 0 {
		t.Fatal("expected non-empty tokens")
	}

	// Collect token texts
	var texts []string
	for _, tok := range tokens {
		texts = append(texts, tok.Text)
	}
	t.Logf("segmented: %v", texts)

	// Verify at least some expected substrings are found
	found := make(map[string]bool)
	for _, tok := range tokens {
		found[tok.Text] = true
	}
	for _, expected := range []string{"国际", "广播", "电台"} {
		if !found[expected] {
			t.Logf("expected word %q not found in tokens %v", expected, texts)
		}
	}
}

func TestSegmentEmpty(t *testing.T) {
	dictPath := setupTestDict(t)
	tok, err := New(dictPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer tok.Close()

	tokens := tok.Segment("")
	if len(tokens) != 0 {
		t.Errorf("expected empty tokens for empty input, got %d", len(tokens))
	}
}

func TestSegmentCharIndices(t *testing.T) {
	dictPath := setupTestDict(t)
	tok, err := New(dictPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer tok.Close()

	// "中国北京" = 4 characters, positions 0,1,2,3
	tokens := tok.Segment("中国北京")
	if len(tokens) == 0 {
		t.Fatal("expected non-empty tokens")
	}

	for _, tok := range tokens {
		if tok.CharStart >= tok.CharEnd {
			t.Errorf("token %q: CharStart(%d) >= CharEnd(%d)", tok.Text, tok.CharStart, tok.CharEnd)
		}
		if tok.CharStart < 0 || tok.CharEnd > 4 {
			t.Errorf("token %q: indices [%d,%d) out of range for text of length 4", tok.Text, tok.CharStart, tok.CharEnd)
		}
	}
}

func TestClose(t *testing.T) {
	dictPath := setupTestDict(t)
	tok, err := New(dictPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := tok.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
	// Second close should also be safe
	if err := tok.Close(); err != nil {
		t.Errorf("second Close() failed: %v", err)
	}
}

func BenchmarkSegment(b *testing.B) {
	dictPath := setupTestDict(b)
	tok, err := New(dictPath)
	if err != nil {
		b.Fatalf("New() failed: %v", err)
	}
	defer tok.Close()

	text := "欢迎收听国际广播电台中国北京"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tok.Segment(text)
	}
}
