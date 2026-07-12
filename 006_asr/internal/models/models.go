package models

import "time"

// JobStatus represents the current state of a transcription job.
type JobStatus string

const (
	StatusPending      JobStatus = "pending"
	StatusExtracting   JobStatus = "extracting"
	StatusTranscribing JobStatus = "transcribing"
	StatusDone         JobStatus = "done"
	StatusError        JobStatus = "error"
)

// Job represents a single transcription request.
type Job struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Status      JobStatus `json:"status"`
	Progress    float64   `json:"progress"`     // 0.0 to 1.0
	Stage       string    `json:"stage"`         // e.g. "extracting", "transcribing 3/12", "merging"
	Error       string    `json:"error,omitempty"`
	SRTContent  string    `json:"-"`             // Final SRT text, not serialized to status API
	DurationSec float64   `json:"duration_sec"`  // Audio duration in seconds
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ChunkInfo describes one PCM chunk to process.
type ChunkInfo struct {
	Index    int       // 0-based chunk index
	Samples  []float32 // PCM f32le, 16kHz mono
	StartSec float64   // Absolute start time in the original audio
	EndSec   float64   // Absolute end time
}

// TranscriptResult is the ASR output for one chunk.
type TranscriptResult struct {
	Text        string    // Recognized Chinese text
	Timestamps  []float64 // Per-token timestamps (seconds relative to chunk start)
	Tokens      []string  // Raw token strings from sherpa-onnx
	ChunkOffset float64   // Seconds to add to Timestamps to get absolute time
}
