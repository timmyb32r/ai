package engine

import (
	"reflect"
	"testing"
)

// silencedetect stderr samples mirror the real ffmpeg af_silencedetect output:
// each line is prefixed with a "[silencedetect @ 0x..]" tag, silence_start
// lines carry only a timestamp, and silence_end lines also carry a
// "| silence_duration" suffix.

func TestParseSilence_LeadingTrailingSpeech(t *testing.T) {
	// Speech [0,12.345], silence [12.345,14.567], speech [14.567, end).
	// The input opens with speech (first silence_start > 0) and ends with
	// trailing speech (last marker is a silence_end), so the tail is
	// open-ended.
	stderr := []byte(
		"ffmpeg version 6.0 Copyright (c) 2000-2023\n" +
			"[silencedetect @ 0x55a0b1c0d0e0] silence_start: 12.345\n" +
			"[silencedetect @ 0x55a0b1c0d0e0] silence_end: 14.567 | silence_duration: 2.222\n" +
			"size=N/A time=00:00:30.00 bitrate=N/A speed=...\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 0, End: 12.345},
		{Start: 14.567, End: 0}, // open-ended trailing speech
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}

func TestParseSilence_MultipleSilencesMiddleSpeech(t *testing.T) {
	// Three silences with speech between them; opens and closes with speech.
	//   speech [0, 1.0]
	//   silence [1.0, 2.0]
	//   speech  [2.0, 5.5]
	//   silence [5.5, 6.25]
	//   speech  [6.25, 9.0]
	//   silence [9.0, 10.0]
	//   speech  [10.0, end)  -> open-ended
	stderr := []byte(
		"[silencedetect @ 0xabc] silence_start: 1.0\n" +
			"[silencedetect @ 0xabc] silence_end: 2.0 | silence_duration: 1.0\n" +
			"[silencedetect @ 0xabc] silence_start: 5.5\n" +
			"[silencedetect @ 0xabc] silence_end: 6.25 | silence_duration: 0.75\n" +
			"[silencedetect @ 0xabc] silence_start: 9.0\n" +
			"[silencedetect @ 0xabc] silence_end: 10.0 | silence_duration: 1.0\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 0, End: 1.0},
		{Start: 2.0, End: 5.5},
		{Start: 6.25, End: 9.0},
		{Start: 10.0, End: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}

func TestParseSilence_LeadingSilence(t *testing.T) {
	// The input starts in silence (first silence_start == 0): there is NO
	// leading speech region before it.
	stderr := []byte(
		"[silencedetect @ 0xabc] silence_start: 0\n" +
			"[silencedetect @ 0xabc] silence_end: 3.5 | silence_duration: 3.5\n" +
			"[silencedetect @ 0xabc] silence_start: 8.0\n" +
			"[silencedetect @ 0xabc] silence_end: 9.0 | silence_duration: 1.0\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 3.5, End: 8.0},
		{Start: 9.0, End: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}

func TestParseSilence_OngoingSilenceTail(t *testing.T) {
	// A trailing silence_start with no matching silence_end: silence was still
	// ongoing when detection ended. It closes the preceding speech region and
	// contributes NO trailing speech region.
	stderr := []byte(
		"[silencedetect @ 0xabc] silence_start: 4.2\n" +
			"[silencedetect @ 0xabc] silence_end: 6.0 | silence_duration: 1.8\n" +
			"[silencedetect @ 0xabc] silence_start: 11.5\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 0, End: 4.2},
		{Start: 6.0, End: 11.5},
		// no open-ended tail: input ends in silence
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}

func TestParseSilence_NoSilence(t *testing.T) {
	// No silencedetect markers at all -> the whole input is one open-ended
	// speech region {0, 0}.
	stderr := []byte(
		"ffmpeg version 6.0\n" +
			"[Parsed_silencedetect_0 @ 0xabc] No silence detected of any kind\n" +
			"size=N/A time=00:00:05.00 bitrate=N/A speed=...\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 0, End: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}

func TestParseSilence_OngoingSilenceFromStart(t *testing.T) {
	// Edge: a single ongoing silence starting at 0 with no end -> no speech at
	// all. Returns nil (empty), not a zero-length region.
	stderr := []byte(
		"[silencedetect @ 0xabc] silence_start: 0\n",
	)

	got := ParseSilence(stderr)
	if len(got) != 0 {
		t.Fatalf("ParseSilence = %v, want no regions", got)
	}
}

func TestParseSilence_EmptyStderr(t *testing.T) {
	// Empty/garbage stderr with no markers behaves like "no silence": one
	// open-ended region.
	got := ParseSilence(nil)
	want := []SpeechRegion{{Start: 0, End: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence(nil) = %v, want %v", got, want)
	}
}

func TestParseSilence_MalformedTimestampSkipped(t *testing.T) {
	// A marker whose timestamp does not parse is ignored; the valid markers
	// still produce the expected complement.
	stderr := []byte(
		"[silencedetect @ 0xabc] silence_start: not-a-number\n" +
			"[silencedetect @ 0xabc] silence_start: 2.0\n" +
			"[silencedetect @ 0xabc] silence_end: 3.0 | silence_duration: 1.0\n",
	)

	got := ParseSilence(stderr)
	want := []SpeechRegion{
		{Start: 0, End: 2.0},
		{Start: 3.0, End: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSilence =\n  %v\nwant\n  %v", got, want)
	}
}
