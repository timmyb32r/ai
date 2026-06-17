package broadcast

// SyncEvent is sent once per subscriber at stream start, communicating the
// absolute broadcast timeline position at which the audio stream begins for
// this subscriber. The client uses it to map its local MediaPlayer position
// (0-based) to the broadcast timeline.
type SyncEvent struct {
	AudioTimelineStart float64 `json:"audioTimelineStart"`
}

// WordBoundary describes one word within a subtitle's text, identified by its
// rune (character) offsets into TextZh. CharStart is the 0-based index of the
// first character of the word; CharEnd is the exclusive end (one past the last
// character), matching standard Go slice semantics for []rune(textZh).
//
// StartSec and EndSec are per-word timestamps (seconds) relative to the start
// of the subtitle window. They are populated from sherpa-onnx per-token
// timestamps aligned with gse word boundaries. When timestamps are unavailable
// (e.g. no sherpa-onnx output or legacy clients) both are zero.
type WordBoundary struct {
	CharStart int     `json:"charStart"`
	CharEnd   int     `json:"charEnd"`
	StartSec  float64 `json:"startSec"`
	EndSec    float64 `json:"endSec"`
	Pinyin    string  `json:"pinyin,omitempty"`
	English   string  `json:"en,omitempty"`
}

// SubtitleEvent is one timestamped subtitle line released over the SSE stream.
// Start and End are seconds on the broadcast timeline; TextZh is the
// Simplified-Chinese transcript for that span.
//
// The schema intentionally reserves room for future fields without breaking
// JSON consumers: Pinyin (romanization) and English (translation) are planned
// (plan "Follow-ups") and will be added as additional json-tagged fields, e.g.
//
//	Pinyin  string `json:"pinyin,omitempty"`
//	English string `json:"en,omitempty"`
type SubtitleEvent struct {
	Start   float64        `json:"start"`
	End     float64        `json:"end"`
	TextZh  string         `json:"text_zh"`
	Pinyin  string         `json:"pinyin,omitempty"`
	English string         `json:"en,omitempty"`
	Words   []WordBoundary `json:"words,omitempty"`
}

// LifecycleState is the ref-counted ingest lifecycle state. The zero value is
// Stopped.
type LifecycleState int

const (
	// Stopped means no ingest is running and no subscribers are attached.
	Stopped LifecycleState = iota
	// Starting means a first subscriber arrived and ingest is launching.
	Starting
	// Warming means ingest is running but the buffer has not yet filled to the
	// full delay; audio is served near-live and ramps toward the delay.
	Warming
	// Running means the buffer has filled and the broadcast head trails the
	// live edge by the configured delay.
	Running
	// Stopping means the last subscriber left and teardown is in progress
	// (after the linger window); a new subscribe can cancel it.
	Stopping
)

// String returns the lowercase name of the state, matching the strings used in
// the /v1/status payload (e.g. "warming", "running").
func (s LifecycleState) String() string {
	switch s {
	case Stopped:
		return "stopped"
	case Starting:
		return "starting"
	case Warming:
		return "warming"
	case Running:
		return "running"
	case Stopping:
		return "stopping"
	default:
		return "unknown"
	}
}

// JumpEvent is sent on the control channel when the server detects that a
// subscriber has fallen too far behind (delay > evictMargin) and must
// reconnect. The client should treat this as a signal to tear down the current
// connection and re-subscribe; the reconnect hands the client fresh burst
// data from the current broadcast head.
type JumpEvent struct {
	BroadcastHead float64 `json:"broadcastHead"`
	DelaySec      float64 `json:"delaySec"`
}

// Status is the JSON payload served by GET /v1/status. LiveEdgeOffsetSeconds is
// ingestHead − broadcastHead.
type Status struct {
	Channel               string  `json:"channel"`
	Listeners             int     `json:"listeners"`
	DelaySeconds          float64 `json:"delaySeconds"`
	State                 string  `json:"state"`
	LiveEdgeOffsetSeconds float64 `json:"liveEdgeOffsetSeconds"`
}
