package engine

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// SpeechRegion is a contiguous span of non-silence audio, in seconds, derived
// as the complement of the silence intervals reported by ffmpeg's
// silencedetect filter. Start and End are offsets from the beginning of the
// analyzed input.
//
// Open-ended convention: a region whose right edge is the (unknown) end of the
// input is reported with End == 0. That is, End == 0 means "until the end of
// the input" — the region is NOT silence-terminated, so its true end is
// unknown from the silencedetect stderr alone. Concretely this occurs for two
// shapes:
//   - trailing speech: the last silence_end has no following silence_start, so
//     speech runs from that silence_end to the end of the input;
//   - leading-only speech: there is no silence at all, so the whole input is
//     one open-ended region {Start: 0, End: 0}.
//
// Callers that know the true clip length (e.g. TranscribeSegments leaving the
// tail for its next pass) treat End == 0 as "not yet terminated" and skip /
// re-process it. A bounded region always has End > Start > = 0.
type SpeechRegion struct {
	// Start is the region start, in seconds.
	Start float64
	// End is the region end, in seconds. End == 0 means the region runs to the
	// (unknown) end of the input — i.e. it is not silence-terminated.
	End float64
}

// silencePrefixStart and silencePrefixEnd are the substrings that mark the two
// silencedetect markers within a stderr line. ffmpeg prefixes each line with a
// "[silencedetect @ 0x..]" tag, so we locate the marker by substring rather
// than anchoring to the line start.
const (
	silenceStartMarker = "silence_start:"
	silenceEndMarker   = "silence_end:"
)

// ParseSilence parses the stderr of an ffmpeg run that used the silencedetect
// audio filter and returns the speech regions (the complement of the detected
// silence intervals over [0, +inf)).
//
// silencedetect logs lines of the form
//
//	[silencedetect @ 0x55..] silence_start: 12.345
//	[silencedetect @ 0x55..] silence_end: 14.567 | silence_duration: 2.222
//
// The detected SILENCE intervals are [silence_start, silence_end]; the speech
// regions are their complement:
//   - from 0 to the first silence_start (leading speech, omitted if the input
//     starts in silence, i.e. first silence_start == 0);
//   - between each silence_end and the next silence_start;
//   - after the last silence_end to the end of the input (trailing speech),
//     reported open-ended as End == 0 per the SpeechRegion convention.
//
// A trailing silence_start with no matching silence_end (silence still ongoing
// when detection ended) is handled gracefully: it closes the preceding speech
// region and contributes no further region. Lines that do not carry a marker,
// or whose timestamp does not parse, are ignored. ParseSilence never returns a
// zero-length region (End <= Start with End != 0 is dropped).
func ParseSilence(stderr []byte) []SpeechRegion {
	type interval struct {
		start float64
		end   float64
		// endSet is false for an ongoing (never-terminated) silence.
		endSet bool
	}

	var silences []interval
	// pending tracks an open silence_start awaiting its silence_end.
	var pending *interval

	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	const maxBuf = 1024 * 1024
	scanner.Buffer(make([]byte, 64*1024), maxBuf)

	for scanner.Scan() {
		line := scanner.Text()

		// silence_end also contains "silence_duration"; check the end marker
		// first since a single line never carries both start and end markers.
		if idx := strings.Index(line, silenceEndMarker); idx >= 0 {
			val, ok := parseLeadingFloat(line[idx+len(silenceEndMarker):])
			if !ok {
				continue
			}
			if pending != nil {
				pending.end = val
				pending.endSet = true
				silences = append(silences, *pending)
				pending = nil
			}
			// A silence_end with no matching silence_start is malformed noise;
			// drop it.
			continue
		}

		if idx := strings.Index(line, silenceStartMarker); idx >= 0 {
			val, ok := parseLeadingFloat(line[idx+len(silenceStartMarker):])
			if !ok {
				continue
			}
			// A new silence_start while one is pending (no intervening
			// silence_end) means the previous silence never closed: keep the
			// earlier one as an ongoing silence and start tracking the new one.
			if pending != nil {
				silences = append(silences, *pending) // endSet == false
			}
			pending = &interval{start: val}
		}
	}
	// A still-pending silence_start at EOF is an ongoing (never-terminated)
	// silence; record it so it closes the trailing speech region.
	if pending != nil {
		silences = append(silences, *pending) // endSet == false
	}

	// Build the complement. cursor is the start of the current speech region.
	var regions []SpeechRegion
	cursor := 0.0
	for _, s := range silences {
		// Speech from cursor up to this silence's start, if non-empty.
		if s.start > cursor {
			regions = append(regions, SpeechRegion{Start: cursor, End: s.start})
		}
		if !s.endSet {
			// Ongoing silence to the end of input: no trailing speech follows,
			// and there is nothing after it.
			return regions
		}
		// Next speech region starts at the silence end. Advance the cursor,
		// never moving it backwards (overlapping/duplicate markers are benign).
		if s.end > cursor {
			cursor = s.end
		}
	}
	// Trailing speech after the last (terminated) silence runs to the unknown
	// end of input: open-ended End == 0 per the SpeechRegion convention.
	regions = append(regions, SpeechRegion{Start: cursor, End: 0})
	return regions
}

// parseLeadingFloat extracts the first whitespace-delimited token from s and
// parses it as a float. It tolerates the leading space ffmpeg writes after the
// marker colon (e.g. " 12.345 | silence_duration: ...") by trimming and taking
// the first field. It returns ok == false when no float can be parsed.
func parseLeadingFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// The value is the first field; anything after (e.g. "| silence_duration")
	// is ignored. Guard against a string that TrimSpace left non-empty but Fields
	// still splits into nothing (e.g. only Unicode separators) before indexing.
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	field := f[0]
	v, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
