package asr

import (
	"sync"

	"github.com/criradio/server/internal/models"
)

// MockTranscriber implements Transcriber for testing without whisper.cpp.
type MockTranscriber struct {
	mu           sync.Mutex
	segments     []*models.TranscriptSegment
	TranscribeFn func(pcm []float32, segmentID int) (*models.TranscriptSegment, error)
}

func NewMockTranscriber() *MockTranscriber {
	return &MockTranscriber{}
}

func (m *MockTranscriber) Transcribe(pcm []float32, segmentID int) (*models.TranscriptSegment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.TranscribeFn != nil {
		return m.TranscribeFn(pcm, segmentID)
	}

	// Default mock: return a simple segment
	seg := &models.TranscriptSegment{
		SegmentID:        segmentID,
		TimelineStartSec: float64(segmentID * 3),
		TimelineEndSec:   float64(segmentID*3 + 3),
		TextZh:           "测试文本",
		TextPinyin:       "cèshì wénběn",
		TextEn:           "test text",
		Words: []models.WordEntry{
			{Text: "测试", CharStart: 0, CharEnd: 2, StartSec: float64(segmentID*3) + 0.0, EndSec: float64(segmentID*3) + 0.8, Pinyin: "cèshì", Trans: "test"},
			{Text: "文本", CharStart: 2, CharEnd: 4, StartSec: float64(segmentID*3) + 0.8, EndSec: float64(segmentID*3) + 1.6, Pinyin: "wénběn", Trans: "text"},
		},
	}
	m.segments = append(m.segments, seg)
	return seg, nil
}

func (m *MockTranscriber) Close() error {
	return nil
}

// GetSegments returns all segments that were transcribed (for test inspection).
func (m *MockTranscriber) GetSegments() []*models.TranscriptSegment {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*models.TranscriptSegment, len(m.segments))
	copy(out, m.segments)
	return out
}
