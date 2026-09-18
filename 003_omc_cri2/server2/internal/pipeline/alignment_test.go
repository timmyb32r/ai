package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodedPCMOriginAndLaterWindows(t *testing.T) {
	r := newPCMRing(2)
	raw := make([]float32, sampleRate)
	for i := range raw {
		raw[i] = float32(i + 1)
	}
	r.append(raw)
	first, ok := encodedPCM(r, 0, 2000, false)
	if !ok || len(first) != 2000 {
		t.Fatalf("initial window: %t %d", ok, len(first))
	}
	for i := int64(0); i < mp3PrimingSamples; i++ {
		if first[i] != 0 {
			t.Fatalf("initial codec silence at %d", i)
		}
	}
	if first[mp3PrimingSamples] != raw[0] {
		t.Fatal("first sample not shifted to encoded origin")
	}
	later, ok := encodedPCM(r, 4000, 6000, false)
	if !ok || later[0] != raw[4000-mp3PrimingSamples] || later[len(later)-1] != raw[5999-mp3PrimingSamples] {
		t.Fatal("later segment applied a different delay")
	}
}
func TestEncodedPCMUnavailableAndFinalTail(t *testing.T) {
	r := newPCMRing(1)
	r.append([]float32{1, 2, 3})
	start := mp3PrimingSamples
	if _, ok := encodedPCM(r, start, start+4, false); ok {
		t.Fatal("nonfinal missing audio accepted")
	}
	out, ok := encodedPCM(r, start, start+5, true)
	if !ok || len(out) != 5 || out[0] != 1 || out[2] != 3 || out[3] != 0 || out[4] != 0 {
		t.Fatalf("final padding: %v %t", out, ok)
	}
	if _, ok := encodedPCM(r, start, start+3+mp3FrameSamples+1, true); ok {
		t.Fatal("manufactured unavailable lookahead")
	}
	if _, ok := encodedPCM(r, 0, math.MaxInt64, true); ok {
		t.Fatal("unbounded request accepted")
	}
	if _, ok := encodedPCM(r, -1, 3, false); ok {
		t.Fatal("negative media interval accepted")
	}
	r.append(make([]float32, sampleRate))
	if _, ok := encodedPCM(r, start, start+3, true); ok {
		t.Fatal("overwritten audio padded instead of rejected")
	}
}

// TestEncodedPCMMatchesActualHLS is deliberately an encoder/decoder test, not
// an assertion that repeats mp3PrimingSamples. Sixty seconds of distinct tone
// markers cross twenty variable-length HLS boundaries. Their measured centers
// are compared against compensated first/later windows. A wrong constant,
// wrong sign or per-segment accumulation fails this check.
//
// CRI_ALIGNMENT_FFMPEG can point to a wrapper invoking the deployed FFmpeg
// image; otherwise the available local ffmpeg is used. No model or network is
// involved in this synthetic fixture.
func TestEncodedPCMMatchesActualHLS(t *testing.T) {
	ffmpeg := os.Getenv("CRI_ALIGNMENT_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg not available for codec integration test")
		}
	}
	dir := t.TempDir()
	const seconds = 63
	const pulseLength = 1600
	raw := make([]float32, seconds*sampleRate)
	var markers []int
	// Markers remain away from frame/segment boundaries so the centroid measures
	// codec delay rather than a truncated transient.
	for sec := 1; sec < seconds; sec += 3 {
		start := sec * sampleRate
		markers = append(markers, start)
		for i := 0; i < pulseLength; i++ {
			envelope := math.Pow(math.Sin(math.Pi*float64(i)/pulseLength), 2)
			raw[start+i] = float32(0.6 * math.Sin(2*math.Pi*997*float64(i)/sampleRate) * envelope)
		}
	}
	input := filepath.Join(dir, "input.f32")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = binary.Write(f, binary.LittleEndian, raw); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, e := cmd.Output()
		if e != nil {
			t.Fatalf("ffmpeg failed: %v\n%s", e, stderr.String())
		}
		return out
	}
	rawBytes := run("-hide_banner", "-loglevel", "error", "-y",
		"-f", "f32le", "-ar", "16000", "-ac", "1", "-i", input,
		"-filter_complex", "[0:a:0]aresample=16000,asetpts=N/SR/TB,asplit=2[pcm][audio]",
		"-map", "[pcm]", "-c:a", "pcm_s16le", "-ac", "1", "-f", "s16le", "pipe:1",
		"-map", "[audio]", "-c:a", "libmp3lame", "-ac", "1", "-ar", "16000", "-b:a", "64k",
		"-f", "hls", "-hls_time", "3", "-hls_list_size", "100",
		"-hls_flags", "program_date_time+temp_file",
		"-hls_segment_filename", filepath.Join(dir, "%09d.ts"), filepath.Join(dir, "ingest.m3u8"))
	if len(rawBytes)%2 != 0 {
		t.Fatal("odd PCM byte count")
	}
	captured := make([]float32, len(rawBytes)/2)
	for i := range captured {
		captured[i] = float32(int16(binary.LittleEndian.Uint16(rawBytes[2*i:]))) / 32768
	}
	decodedBytes := run("-hide_banner", "-loglevel", "error", "-i", filepath.Join(dir, "ingest.m3u8"), "-map", "0:a:0", "-ar", "16000", "-ac", "1", "-f", "f32le", "pipe:1")
	if len(decodedBytes)%4 != 0 {
		t.Fatal("odd decoded float count")
	}
	decoded := make([]float32, len(decodedBytes)/4)
	for i := range decoded {
		decoded[i] = math.Float32frombits(binary.LittleEndian.Uint32(decodedBytes[4*i:]))
	}
	segments, err := readMediaPlaylist(filepath.Join(dir, "ingest.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 20 {
		t.Fatalf("fixture did not span many HLS segments: %d", len(segments))
	}
	r := newPCMRing(seconds + 2)
	r.append(captured)
	var maxError, firstDelay, lastDelay float64
	for markerIndex, marker := range markers {
		rawCenter := energyCenter(t, captured, marker-500, marker+3500)
		decodedCenter := energyCenter(t, decoded, marker-500, marker+3500)
		measuredDelay := decodedCenter - rawCenter
		if markerIndex == 0 {
			firstDelay = measuredDelay
		}
		lastDelay = measuredDelay
		// Locate the real playlist interval containing this marker, rather than
		// assuming each segment has exactly 3*16000 samples.
		var selected *mediaSegment
		for i := range segments {
			begin := int64(math.Round((segments[i].Start - segments[0].Start) * sampleRate))
			end := begin + int64(math.Round(segments[i].Duration*sampleRate))
			if decodedCenter >= float64(begin) && decodedCenter < float64(end) {
				selected = &segments[i]
				break
			}
		}
		if selected == nil {
			t.Fatalf("marker %d absent from playlist", markerIndex)
		}
		begin := int64(math.Round((selected.Start - segments[0].Start) * sampleRate))
		end := begin + int64(math.Round(selected.Duration*sampleRate))
		aligned, ok := encodedPCM(r, begin, end, false)
		if !ok {
			t.Fatalf("encoded window unavailable: %d..%d", begin, end)
		}
		center := energyCenter(t, aligned, 0, len(aligned)) + float64(begin)
		delta := math.Abs(center - decodedCenter)
		maxError = math.Max(maxError, delta)
		// Much stronger than the requested single MP3-frame allowance. This leaves
		// room for codec-filter energy changes while catching a 529-sample offset.
		if delta > 16 {
			t.Fatalf("marker %d drift: compensated %.3f decoded %.3f, error %.3f samples", markerIndex, center, decodedCenter, delta)
		}
	}
	if math.Abs(lastDelay-firstDelay) > 16 {
		t.Fatalf("accumulating drift: first %.3f last %.3f", firstDelay, lastDelay)
	}
	// Exact declared last media span is allowed despite codec final-frame padding.
	last := segments[len(segments)-1]
	begin := int64(math.Round((last.Start - segments[0].Start) * sampleRate))
	end := begin + int64(math.Round(last.Duration*sampleRate))
	final, ok := encodedPCM(r, begin, end, true)
	if !ok || int64(len(final)) != end-begin {
		t.Fatalf("final encoded tail: %d..%d got %d/%t", begin, end, len(final), ok)
	}
	version := run("-version")
	line := strings.SplitN(string(version), "\n", 2)[0]
	t.Logf("%s; %d segments, %d markers; measured delay first %.3f / last %.3f samples; maximum compensated error %.3f samples (%.3f ms)", line, len(segments), len(markers), firstDelay, lastDelay, maxError, maxError*1000/sampleRate)
}

func energyCenter(t *testing.T, samples []float32, start, end int) float64 {
	t.Helper()
	if start < 0 || end > len(samples) || end <= start {
		t.Fatal(fmt.Sprintf("invalid energy window %d..%d/%d", start, end, len(samples)))
	}
	energy, weighted := 0.0, 0.0
	for i := start; i < end; i++ {
		e := float64(samples[i]) * float64(samples[i])
		energy += e
		weighted += float64(i) * e
	}
	if energy < 0.01 {
		t.Fatal("missing marker energy")
	}
	return weighted / energy
}
