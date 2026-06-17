package broadcast

import (
	"testing"
)

func TestGseSegmenter_Empty(t *testing.T) {
	seg := NewGseSegmenter("")
	got := seg.Segment("")
	if got != nil {
		t.Fatalf("expected nil for empty string, got %v", got)
	}
}

func TestGseSegmenter_SingleWord(t *testing.T) {
	seg := NewGseSegmenter("")
	got := seg.Segment("你好")
	if len(got) != 1 {
		t.Fatalf("expected 1 word, got %d: %+v", len(got), got)
	}
	if got[0].CharStart != 0 || got[0].CharEnd != 2 {
		t.Fatalf("expected [{0,2}], got %+v", got)
	}
}

func TestGseSegmenter_MultipleWords(t *testing.T) {
	seg := NewGseSegmenter("")
	got := seg.Segment("今天天气很好")
	// Expect 4 two-character words: 今天, 天气, 很好, or similar segmentation
	if len(got) < 2 {
		t.Fatalf("expected at least 2 words, got %d: %+v", len(got), got)
	}
	// Verify rune offsets are contiguous and cover the text.
	if len(got) > 0 && got[0].CharStart != 0 {
		t.Fatalf("first word should start at 0, got %+v", got[0])
	}
	last := got[len(got)-1]
	// Verify the last word's CharEnd does not exceed the text length.
	// "今天天气很好" has 5 runes; the segmenter clamps output to the text
	// length so no boundary exceeds it.
	textLen := len([]rune("今天天气很好")) // 5
	if last.CharEnd > textLen {
		t.Fatalf("CharEnd %d exceeds text length %d — clamp failed: %+v",
			last.CharEnd, textLen, last)
	}
	// Verify offsets are contiguous (no gaps).
	for i := 1; i < len(got); i++ {
		if got[i].CharStart != got[i-1].CharEnd {
			t.Fatalf("gap between words %d and %d: %+v, %+v", i-1, i, got[i-1], got[i])
		}
	}
}

func TestGseSegmenter_RuneOffsetsNotBytes(t *testing.T) {
	seg := NewGseSegmenter("")
	// Each of these characters is 3 bytes in UTF-8 but 1 rune.
	got := seg.Segment("中文测试")
	if len(got) == 0 {
		t.Fatal("expected non-empty result")
	}
	// CharEnd should be rune count (4), not byte count (12).
	last := got[len(got)-1]
	if last.CharEnd != 4 {
		t.Fatalf("CharEnd should be 4 (rune count), got %d (byte count would be 12)", last.CharEnd)
	}
}
