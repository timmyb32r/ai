// Package models defines shared data types used across all server modules.
package models

import "fmt"

// TranscriptSegment is the complete processing result for one 3-second audio segment.
// TimelineStartSec and TimelineEndSec use Unix epoch seconds, directly derived from
// #EXT-X-PROGRAM-DATE-TIME in the HLS playlist — the single source of truth for time.
type TranscriptSegment struct {
	SegmentID        int         `json:"segment_id"`
	TimelineStartSec float64     `json:"timeline_start_sec"`
	TimelineEndSec   float64     `json:"timeline_end_sec"`
	TSFile           string      `json:"ts_file"`
	TextZh           string      `json:"text_zh"`
	TextPinyin       string      `json:"text_pinyin"`
	TextEn           string      `json:"text_en"`
	Words            []WordEntry `json:"words"`

	// RawTimestamps and RawTokens carry per-character timestamps from
	// sherpa-onnx. When non-empty, the pipeline uses them to compute
	// accurate per-word timestamps instead of proportional distribution.
	RawTimestamps []float64 `json:"-"`
	RawTokens     []string  `json:"-"`
}

// WordEntry represents a single Chinese word with timing, pronunciation, and meaning.
type WordEntry struct {
	Text      string  `json:"text"`
	CharStart int     `json:"char_start"`
	CharEnd   int     `json:"char_end"`
	StartSec  float64 `json:"start_sec"`
	EndSec    float64 `json:"end_sec"`
	Pinyin    string  `json:"pinyin"`
	Trans     string  `json:"translation"`
}

// SegmentIndex is the index.json mapping segment IDs to files and timeline positions.
type SegmentIndex struct {
	UpdatedAt string       `json:"updated_at"`
	Segments  []SegmentRef `json:"segments"`
}

// SegmentRef is a lightweight reference to a segment.
type SegmentRef struct {
	ID               int     `json:"id"`
	TimelineStartSec float64 `json:"timeline_start_sec"`
	TimelineEndSec   float64 `json:"timeline_end_sec"`
	TSFile           string  `json:"ts_file"`
	JSONFile         string  `json:"json_file"`
}

// PipelineStats holds per-segment timing breakdown for performance monitoring.
type PipelineStats struct {
	SegmentID  int   `json:"segment_id"`
	IngestMs   int64 `json:"ingest_ms"`
	ASRMs      int64 `json:"asr_ms"`
	TokenizeMs int64 `json:"tokenize_ms"`
	DictMs     int64 `json:"dict_ms"`
	StorageMs  int64 `json:"storage_ms"`
	TotalMs    int64 `json:"total_ms"`
}

// ServerStatus is the JSON response for GET /api/status.
type ServerStatus struct {
	Status              string  `json:"status"`
	ChannelURL          string  `json:"channel_url"`
	SegmentsTotal       int64   `json:"segments_total"`
	MetadataFiles       int     `json:"metadata_files"`
	LiveEdgeOffsetSec   float64 `json:"live_edge_offset_sec"`
	ClientsConnected    int     `json:"clients_connected"`
	OldestSegmentStartSec float64 `json:"oldest_segment_start_sec"`
	NewestSegmentEndSec   float64 `json:"newest_segment_end_sec"`
}

// SSESync is the initial sync event sent to new SSE connections.
type SSESync struct {
	Type             string  `json:"type"`
	TimelineStartSec float64 `json:"timeline_start_sec"`
	ServerTime       string  `json:"server_time"`
}

// SSESegment is the per-segment event sent through SSE.
type SSESegment struct {
	Type    string            `json:"type"`
	Segment TranscriptSegment `json:"segment"`
}

// PCMChunk is a 3-second audio chunk received from the ingest module.
type PCMChunk struct {
	SegmentID   int
	Samples     []float32 // PCM f32le, 16kHz mono, ~48000 samples per 3 seconds
	DurationSec float64
	Error       error
}

// Validate checks internal consistency.
func (s *TranscriptSegment) Validate() error {
	if s.TimelineStartSec >= s.TimelineEndSec {
		return &ValidationError{"timeline_start_sec must be less than timeline_end_sec"}
	}
	for i, w := range s.Words {
		if w.CharStart >= w.CharEnd {
			return &ValidationError{fmt.Sprintf("word[%d]: char_start must be less than char_end", i)}
		}
	}
	return nil
}

// ValidationError is returned for invalid segments.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }
