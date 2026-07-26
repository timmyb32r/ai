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
	HasContent       bool        `json:"has_content"` // true when ASR produced non-empty text

	// RawTimestamps and RawTokens carry per-character timestamps from
	// sherpa-onnx. When non-empty, the pipeline uses them to compute
	// accurate per-word timestamps instead of proportional distribution.
	RawTimestamps []float64 `json:"-"`
	RawTokens     []string  `json:"-"`
	// PreComputedTokens carries tokens from a batch-level tokenizer
	// (e.g. HanLP on stitched text). When non-nil, processDownstream
	// uses these instead of calling the per-segment tokenizer.
	// PreComputedTokens carries tokens from a batch-level tokenizer
	// (e.g. HanLP on stitched text). When non-nil, processDownstream
	// uses these instead of calling the per-segment tokenizer.
	// Not serialized to JSON — internal pipeline use only.
	PreComputedTokens []Token `json:"-"`
}

// Token is a word segmented from Chinese text with rune-level positions.
type Token struct {
	Text      string // the word
	CharStart int    // index of first rune in the source text
	CharEnd   int    // index after last rune (exclusive)
}

// WordSense is one structured meaning within a dictionary entry.
type WordSense struct {
	Number int      `json:"number"` // meaning number (0 if unnumbered)
	Labels []string `json:"labels"` // grammatical/style labels
	Text   string   `json:"text"`   // translation text
	Notes  string   `json:"notes"`  // usage notes
}

// WordEntry represents a single Chinese word with timing, pronunciation, and meaning.
type WordEntry struct {
	Text       string   `json:"text"`
	CharStart  int      `json:"char_start"`
	CharEnd    int      `json:"char_end"`
	StartSec   float64  `json:"start_sec"`
	EndSec     float64  `json:"end_sec"`
	Pinyin     string   `json:"pinyin"`      // word-level pinyin
	CharPinyin []string `json:"char_pinyin"` // per-character pinyin syllables
	// CharPinyinUncertain is aligned with CharPinyin: true marks a syllable
	// filled in probabilistically (from Unihan frequency data) rather than
	// derived deterministically from the dictionary/word segmentation. Omitted
	// when nothing is uncertain; absent on the wire → treat all as certain.
	CharPinyinUncertain []bool      `json:"char_pinyin_uncertain,omitempty"`
	Trans               string      `json:"translation"`      // flat translation (backward compat)
	Senses              []WordSense `json:"senses,omitempty"` // structured senses (BKRS)
	// CedictMeanings are the CC-CEDICT English glosses for this word, shown as a
	// second dictionary tab. Populated when the word exists in CEDICT; empty
	// otherwise. Independent of Trans/Senses (which come from the primary dict).
	CedictMeanings []string `json:"cedict_meanings,omitempty"`
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
	Status                string  `json:"status"`
	ChannelURL            string  `json:"channel_url"`
	SegmentsTotal         int64   `json:"segments_total"`
	MetadataFiles         int     `json:"metadata_files"`
	LiveEdgeOffsetSec     float64 `json:"live_edge_offset_sec"`
	ClientsConnected      int     `json:"clients_connected"`
	OldestSegmentStartSec float64 `json:"oldest_segment_start_sec"`
	NewestSegmentEndSec   float64 `json:"newest_segment_end_sec"`
	AsrEngine             string  `json:"asr_engine"`
	AsrModel              string  `json:"asr_model"`
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
