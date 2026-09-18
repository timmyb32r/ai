package models

import (
	"encoding/json"
	"testing"
)

func TestTranscriptSegmentSerialization(t *testing.T) {
	seg := TranscriptSegment{
		SegmentID:        1,
		TimelineStartSec: 1718557203.000,
		TimelineEndSec:   1718557206.000,
		TSFile:           "000000001.ts",
		TextZh:           "欢迎收听国际广播电台",
		TextPinyin:       "huānyíng shōutīng guójì guǎngbō diàntái",
		TextEn:           "Welcome to listen to China Radio International",
		Words: []WordEntry{
			{Text: "欢迎", CharStart: 0, CharEnd: 2, StartSec: 1718557203.000, EndSec: 1718557203.720, Pinyin: "huānyíng", Trans: "welcome"},
			{Text: "收听", CharStart: 2, CharEnd: 4, StartSec: 1718557203.720, EndSec: 1718557204.440, Pinyin: "shōutīng", Trans: "listen to"},
		},
	}

	// Marshal
	data, err := json.Marshal(seg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Unmarshal
	var restored TranscriptSegment
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.SegmentID != seg.SegmentID {
		t.Errorf("SegmentID: got %d, want %d", restored.SegmentID, seg.SegmentID)
	}
	if restored.TimelineStartSec != seg.TimelineStartSec {
		t.Errorf("TimelineStartSec: got %f, want %f", restored.TimelineStartSec, seg.TimelineStartSec)
	}
	if len(restored.Words) != len(seg.Words) {
		t.Fatalf("Words length: got %d, want %d", len(restored.Words), len(seg.Words))
	}
	if restored.Words[0].Text != "欢迎" {
		t.Errorf("Words[0].Text: got %s, want 欢迎", restored.Words[0].Text)
	}
}

func TestTranscriptSegmentValidation(t *testing.T) {
	tests := []struct {
		name string
		seg  TranscriptSegment
		ok   bool
	}{
		{
			name: "valid",
			seg: TranscriptSegment{
				SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
				Words: []WordEntry{{CharStart: 0, CharEnd: 2}},
			},
			ok: true,
		},
		{
			name: "inverted timeline",
			seg: TranscriptSegment{
				SegmentID: 1, TimelineStartSec: 5.0, TimelineEndSec: 3.0,
			},
			ok: false,
		},
		{
			name: "inverted char indices",
			seg: TranscriptSegment{
				SegmentID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0,
				Words: []WordEntry{{CharStart: 5, CharEnd: 2}},
			},
			ok: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.seg.Validate()
			if tt.ok && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestSegmentRefIntegrity(t *testing.T) {
	ref := SegmentRef{
		ID:               1,
		TimelineStartSec: 0.0,
		TimelineEndSec:   3.0,
		TSFile:           "000000001.ts",
		JSONFile:         "000000001.json",
	}
	if ref.TSFile == "" || ref.JSONFile == "" {
		t.Error("TSFile and JSONFile must not be empty")
	}
	if ref.TimelineStartSec >= ref.TimelineEndSec {
		t.Error("TimelineStartSec must be less than TimelineEndSec")
	}
}

func TestSegmentIndexSerialization(t *testing.T) {
	idx := SegmentIndex{
		UpdatedAt: "2026-06-16T18:00:00Z",
		Segments: []SegmentRef{
			{ID: 1, TimelineStartSec: 0.0, TimelineEndSec: 3.0, TSFile: "001.ts", JSONFile: "001.json"},
			{ID: 2, TimelineStartSec: 3.0, TimelineEndSec: 6.0, TSFile: "002.ts", JSONFile: "002.json"},
		},
	}

	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored SegmentIndex
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(restored.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(restored.Segments))
	}
}
