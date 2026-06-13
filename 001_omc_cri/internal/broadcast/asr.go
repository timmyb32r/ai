package broadcast

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"os"
	"time"

	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
)

// pcmBytesPerSec is the byte rate of the buffered PCM track (s16le 16 kHz
// mono): 16000 samples/s * 2 bytes/sample. It mirrors the ingestor's PCM
// format and is used both to size the ASR region and to write the WAV header.
const pcmBytesPerSec = 16000 * 2

// asrLiveMargin is how much of the most-recent PCM the ASR refuses to
// transcribe yet, so it only ever feeds settled audio to the recognizer.
const asrLiveMargin = 2.0

// asrWindowSec is the fixed transcription window length. The broadcast source
// (e.g. CRI 905) plays speech over a continuous music bed, so silence-based
// segmentation never finds a pause to cut on and produces no subtitles. Instead
// we transcribe fixed time windows: every asrWindowSec of settled audio becomes
// one subtitle line. This always makes progress regardless of background music,
// at the cost of occasionally cutting a sentence across a window boundary.
const asrWindowSec = 8.0

// asrPassInterval is the sleep between ASR passes. ASR is best-effort: if it
// lags, audio still flows and the affected span simply has no subtitle.
const asrPassInterval = 2 * time.Second

// windowTranscriber is the unexported injection seam over the concrete
// *chineseasr.Transcriber. NewASR still accepts the concrete type to keep the
// frozen signature, but stores it as this interface so asr_test can supply a
// fake without driving real ffmpeg/sherpa subprocesses. *chineseasr.Transcriber
// satisfies it via its Transcribe method (whole-wav -> one transcript), which is
// what fixed-window transcription needs.
type windowTranscriber interface {
	Transcribe(ctx context.Context, wavPath string) (*chineseasr.Result, error)
}

// ASR is the broadcast-side ASR driver. It pulls the not-yet-transcribed PCM
// region from the Buffer, writes a temp WAV, runs TranscribeSegments over it,
// offsets each segment's times onto the broadcast timeline, and stores the
// resulting SubtitleEvents back into the Buffer. It stays offline (only the
// file-based chineseasr path is used); the broadcast package imports the
// chineseasr root, which never imports broadcast (no cycle).
//
// The driver advances cursor only past finalized (silence-bounded) segments, so
// trailing not-yet-silence speech is re-processed on the next pass. ASR lag is
// tolerated: a TranscribeSegments error is logged and the pass retried.
type ASR struct {
	tr  windowTranscriber
	buf *Buffer

	// cursor is the timeline position (seconds since ingest start) up to which
	// PCM has been transcribed into subtitles. Only touched by Run/step (single
	// driver goroutine), so it needs no lock.
	cursor float64

	// tempDir is where intermediate WAV files are written (os.TempDir by
	// default). Overridable in tests.
	tempDir string

	// liveMargin / windowSec / sleep are knobs exposed for deterministic tests.
	liveMargin float64
	windowSec  float64
	sleep      func(ctx context.Context, d time.Duration)
}

// NewASR constructs the ASR driver over the given transcriber and buffer. The
// concrete *chineseasr.Transcriber is stored behind the unexported
// windowTranscriber interface so tests can inject a fake via setTranscriber.
func NewASR(transcriber *chineseasr.Transcriber, buf *Buffer) *ASR {
	return &ASR{
		tr:         transcriber,
		buf:        buf,
		tempDir:    os.TempDir(),
		liveMargin: asrLiveMargin,
		windowSec:  asrWindowSec,
		sleep:      asrSleep,
	}
}

// setTranscriber replaces the transcriber. Unexported test seam (asr_test.go);
// production wires the concrete transcriber via NewASR.
func (a *ASR) setTranscriber(t windowTranscriber) { a.tr = t }

// reset clears the ASR driver's per-epoch state so the next ingest epoch begins
// on a clean, zero-based timeline (Fix 2). The cursor is the only per-epoch
// state; left at epoch-1's high value it would skip the entire zero-based
// epoch-2 PCM (=> zero subtitles in epoch 2). It is called by the broadcast's
// start hook before the ASR goroutine launches, while no Run/step is in flight
// for this driver, so cursor needs no additional synchronisation.
func (a *ASR) reset() { a.cursor = 0 }

// asrSleep waits for d or until ctx is done, whichever is first.
func asrSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Run drives ASR passes until ctx is cancelled, transcribing newly-buffered PCM
// as it becomes available. Each pass calls step; a step error is logged and the
// loop continues (ASR is best-effort and must never crash the broadcast).
func (a *ASR) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.step(ctx); err != nil && ctx.Err() == nil {
			log.Printf("broadcast/asr: step: %v", err)
		}
		a.sleep(ctx, asrPassInterval)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// step performs one ASR pass over the currently un-transcribed, settled PCM
// region [cursor, regionEnd):
//
//   - pull all PCM from cursor onward; the byte length gives the available
//     extent, from which we trim the live margin so only settled audio is fed;
//   - if there isn't at least minRegion seconds of new PCM, do nothing yet;
//   - write the region to a temp WAV (44-byte header + s16le bytes) and run the
//     segmenter; offset each finalized (End != 0) segment's Start/End by the
//     region's start ts and AppendSubtitle it;
//   - advance cursor to the End of the last finalized segment so trailing
//     not-yet-silence speech is re-processed next pass (if no finalized segment
//     came back, cursor is left in place to retry once more audio settles).
//
// A TranscribeSegments error is returned (Run logs it); the temp WAV is always
// removed.
func (a *ASR) step(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Pull only the LEADING CONTIGUOUS run of PCM at or after the cursor, stopping
	// at the first timeline discontinuity (Fix 3). Bounding the pass to one
	// contiguous run keeps a reconnect gap from mis-anchoring every later
	// subtitle: ReadPCMRange would concatenate across the hole, so PCM after the
	// gap would be offset as if it immediately followed the pre-gap audio. The run
	// ends at the gap; once the cursor advances past this run, the next pass picks
	// up the post-gap PCM with its own correct anchor.
	pcm, _, ok := a.buf.ReadContiguousPCMFrom(a.cursor, pcmBytesPerSec)
	if !ok || len(pcm) == 0 {
		return nil
	}
	regionStart, haveStart := a.firstPCMTsAtOrAfter(a.cursor)
	if !haveStart {
		return nil
	}

	availSec := float64(len(pcm)) / float64(pcmBytesPerSec)
	// Need a full window of settled audio (plus the live margin) before
	// transcribing; otherwise wait for more to accumulate.
	if availSec-a.liveMargin < a.windowSec {
		return nil
	}

	// Take exactly one fixed window, rounded down to a whole sample (2 bytes).
	windowBytes := int(a.windowSec * float64(pcmBytesPerSec))
	windowBytes -= windowBytes % 2
	if windowBytes <= 0 || windowBytes > len(pcm) {
		return nil
	}
	region := pcm[:windowBytes]

	wavPath, err := writeTempWAV(a.tempDir, region)
	if err != nil {
		return err
	}
	defer os.Remove(wavPath) //nolint:errcheck

	res, err := a.tr.Transcribe(ctx, wavPath)
	if err != nil {
		// A music-only / no-speech window yields ErrEmptyTranscript: emit no
		// subtitle but still advance the cursor so we never re-scan it.
		if errors.Is(err, chineseasr.ErrEmptyTranscript) {
			a.cursor = regionStart + a.windowSec
			return nil
		}
		return err
	}

	text := ""
	if res != nil {
		text = res.Text
	}
	dbg("asr.window start=%.2f win=%.1fs text=%q", regionStart, a.windowSec, text)
	if text != "" {
		a.buf.AppendSubtitle(SubtitleEvent{
			Start:  regionStart,
			End:    regionStart + a.windowSec,
			TextZh: text,
		})
	}
	// Always advance by exactly one window so the un-transcribed region never
	// grows unbounded (the silence-based approach stalled forever on a
	// continuous music bed).
	a.cursor = regionStart + a.windowSec
	return nil
}

// firstPCMTsAtOrAfter returns the timeline ts of the earliest buffered PCM
// entry whose tsSec >= fromSec (the actual anchor of the region ReadPCMRange
// returns from fromSec), and whether such an entry exists. Same-package access
// to the buffer's PCM track under its mutex.
func (a *ASR) firstPCMTsAtOrAfter(fromSec float64) (float64, bool) {
	a.buf.mu.Lock()
	defer a.buf.mu.Unlock()
	for _, e := range a.buf.pcm {
		if e.tsSec >= fromSec {
			return e.tsSec, true
		}
	}
	return 0, false
}

// writeTempWAV writes pcm (s16le 16 kHz mono) to a new temp file in dir with a
// canonical 44-byte WAV header, returning its path. The caller removes it.
//
// Exported-for-test helper kept small and pure so asr_test can assert the
// header without touching the ASR loop.
func writeTempWAV(dir string, pcm []byte) (string, error) {
	f, err := os.CreateTemp(dir, "broadcast-asr-*.wav")
	if err != nil {
		return "", err
	}
	path := f.Name()
	header := wavHeader(len(pcm))
	if _, err := f.Write(header); err != nil {
		f.Close()
		os.Remove(path) //nolint:errcheck
		return "", err
	}
	if _, err := f.Write(pcm); err != nil {
		f.Close()
		os.Remove(path) //nolint:errcheck
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path) //nolint:errcheck
		return "", err
	}
	return path, nil
}

// wavHeader builds the 44-byte canonical WAV header for s16le 16 kHz mono PCM
// of dataLen bytes. Exported-for-test (lowercase, same package) so asr_test can
// assert the RIFF/fmt/data fields.
func wavHeader(dataLen int) []byte {
	const (
		sampleRate    = 16000
		numChannels   = 1
		bitsPerSample = 16
	)
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], uint32(36+dataLen)) // ChunkSize
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16) // Subchunk1Size (PCM)
	binary.LittleEndian.PutUint16(h[20:22], 1)  // AudioFormat = PCM
	binary.LittleEndian.PutUint16(h[22:24], numChannels)
	binary.LittleEndian.PutUint32(h[24:28], sampleRate)
	binary.LittleEndian.PutUint32(h[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(h[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(h[34:36], bitsPerSample)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], uint32(dataLen)) // Subchunk2Size
	return h
}
