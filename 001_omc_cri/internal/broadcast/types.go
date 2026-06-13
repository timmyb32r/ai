package broadcast

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
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	TextZh string  `json:"text_zh"`
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

// Status is the JSON payload served by GET /v1/status. LiveEdgeOffsetSeconds is
// ingestHead − broadcastHead.
type Status struct {
	Channel               string  `json:"channel"`
	Listeners             int     `json:"listeners"`
	DelaySeconds          float64 `json:"delaySeconds"`
	State                 string  `json:"state"`
	LiveEdgeOffsetSeconds float64 `json:"liveEdgeOffsetSeconds"`
}
