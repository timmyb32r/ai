package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/ingest"
	"github.com/criradio/server/internal/logging"
	"github.com/criradio/server/internal/models"
	"github.com/criradio/server/internal/storage"
	"github.com/criradio/server/internal/tokenizer"
)

type Pipeline struct {
	Ingestor    ingest.Ingestor
	Transcriber asr.Transcriber
	Tokenizer   tokenizer.Tokenizer
	Dictionary  dictionary.Dictionary
	Store       storage.MetadataStore
	Logger      logging.Logger
	OutputDir   string
	HLSTime     int

	hlsStdin io.WriteCloser
	hlsCmd   *exec.Cmd
	hlsMu    sync.Mutex

	// Subtitled playlist: only segments with completed ASR.
	subtitledMu      sync.Mutex
	subtitledLastID  int // highest segment ID with completed ASR
	subtitledFirstID int // first segment ID still in the window

	asrCompleted atomic.Int64 // total segments transcribed by whisper
	epochBase  float64       // Unix epoch at pipeline start — base for monotonic timeline
}

func (p *Pipeline) Run(ctx context.Context) error {
	p.Logger.Info("pipeline", "starting")
	// Clean old metadata from previous run to prevent timeline mismatch
	os.RemoveAll(filepath.Join(p.OutputDir, "metadata"))
	os.MkdirAll(filepath.Join(p.OutputDir, "metadata"), 0o755)
	os.Remove(filepath.Join(p.OutputDir, "hls", "playlist.m3u8"))
	defer p.Logger.Info("pipeline", "stopped")

	hlsDir := filepath.Join(p.OutputDir, "hls")
	os.MkdirAll(hlsDir, 0o755)

	// Single continuous ffmpeg for gapless HLS encoding.
	hlsCmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostdin", "-nostats",
		"-f", "s16le", "-ar", "16000", "-ac", "1",
		"-i", "pipe:0",
		"-c:a", "libmp3lame", "-q:a", "2",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", p.HLSTime),
		"-hls_list_size", "3600",
		"-hls_flags", "delete_segments+program_date_time",
		"-hls_segment_filename", filepath.Join(hlsDir, "%09d.ts"),
		filepath.Join(hlsDir, "live.m3u8"),
	)
	stderrPipe, _ := hlsCmd.StderrPipe()
	stdin, err := hlsCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("hls stdin pipe: %w", err)
	}
	p.hlsStdin = stdin
	p.hlsCmd = hlsCmd

	if err := hlsCmd.Start(); err != nil {
		return fmt.Errorf("start hls ffmpeg: %w", err)
	}
	// Log ffmpeg stderr through our logger (adds timestamps to every line)
	go logStderr(stderrPipe, p.Logger, "ffmpeg-hls")
	p.Logger.Info("pipeline", "hls_encoder_started")

	pcmCh, err := p.Ingestor.Start(ctx)
	if err != nil {
		return err
	}

	asrQueue := make(chan models.PCMChunk, 256)
	// Parallel whisper workers — each processes one segment at a time.
	// Whisper small model + 2 threads/worker ≈ 3s per segment.
	// 4 workers × 2 threads = 8 threads total.
	// MUST be 8 — parallel whisper processes consume the ASR queue.
	// 8 workers × small model ≈ continuous subtitle output matching ingest rate.
	// DO NOT reduce — fewer workers = subtitles lag behind ingest.
	asrWorkers := 8
	for i := 0; i < asrWorkers; i++ {
		go p.asrWorker(ctx, asrQueue)
	}

	// Start with current time, then refine to HLS PROGRAM-DATE-TIME when available.
	p.epochBase = float64(time.Now().UnixMilli()) / 1000.0
	go func() {
		if base := p.waitForHLSTimeline(ctx, hlsDir, 30*time.Second); base > 0 {
			p.epochBase = base
			p.Logger.Info("pipeline", "timeline_base_refined", "epoch", base)
		}
	}()

	// Create empty subtitled playlist so ExoPlayer doesn't 404 on startup
	p.writeEmptyPlaylist(hlsDir)

	p.Logger.Info("pipeline", "running")
	go p.statsReporter(ctx)

	segmentID := 0
	for {
		select {
		case <-ctx.Done():
			close(asrQueue)
			return ctx.Err()
		case chunk, ok := <-pcmCh:
			if !ok {
				close(asrQueue)
				return nil
			}
			if chunk.Error != nil {
				p.Logger.Warn("pipeline", "pcm_error", "id", segmentID, "err", chunk.Error)
				continue
			}

			// Write PCM to continuous HLS encoder (real-time, gapless)
			t0 := time.Now()
			p.writePCMToHLS(chunk.Samples)
			hlsMs := time.Since(t0).Milliseconds()
			p.Logger.Info("pipeline", "hls_segment", "id", segmentID, "hls_ms", hlsMs)

			chunk.SegmentID = segmentID
			select {
			case asrQueue <- chunk:
			default:
				p.Logger.Warn("pipeline", "asr_queue_full", "id", segmentID)
			}
			segmentID++
		}
	}
}

func (p *Pipeline) writePCMToHLS(samples []float32) {
	p.hlsMu.Lock()
	defer p.hlsMu.Unlock()
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := s
		if v > 1.0 { v = 1.0 }
		if v < -1.0 { v = -1.0 }
		val := int16(v * 32767)
		buf[i*2] = byte(val)
		buf[i*2+1] = byte(val >> 8)
	}
	p.hlsStdin.Write(buf)
}

func (p *Pipeline) asrWorker(ctx context.Context, queue <-chan models.PCMChunk) {
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-queue:
			if !ok { return }
			p.processASR(chunk)
		}
	}
}

func (p *Pipeline) processASR(chunk models.PCMChunk) {
	t0 := time.Now()

	asrStart := time.Now()
	segment, err := p.Transcriber.Transcribe(chunk.Samples, chunk.SegmentID)
	asrMs := time.Since(asrStart).Milliseconds()
	if err != nil {
		p.Logger.Error("pipeline", "asr_failed", "id", chunk.SegmentID, "err", err)
		segment = &models.TranscriptSegment{SegmentID: chunk.SegmentID, TextZh: ""}
	}
	// Monotonic timeline: epochBase + segmentID * HLSTime.
	// Guarantees non-overlapping, sequential segments.
	segment.TimelineStartSec = p.epochBase + float64(chunk.SegmentID)*float64(p.HLSTime)
	segment.TimelineEndSec = p.epochBase + float64(chunk.SegmentID+1)*float64(p.HLSTime)

	tokStart := time.Now()
	words := p.Tokenizer.Segment(segment.TextZh)
	tokenizeMs := time.Since(tokStart).Milliseconds()

	dictStart := time.Now()
	segDuration := segment.TimelineEndSec - segment.TimelineStartSec
	wordEntries := make([]models.WordEntry, 0, len(words))

	// When the transcriber provides per-character timestamps (sherpa-onnx),
	// use GSE's CharStart/CharEnd as direct indices into the timestamps array
	// for accurate per-word timing (matches 001_omc_cri's timestampWords).
	hasRawTimestamps := len(segment.RawTimestamps) > 0
	if hasRawTimestamps {
		for _, t := range words {
			entry, err := p.Dictionary.Lookup(t.Text)
			pinyin, trans := "", ""
			if err == nil {
				pinyin = entry.Pinyin
				if len(entry.Meanings) > 0 { trans = entry.Meanings[0] }
			}
			startSec := segment.TimelineStartSec
			endSec := segment.TimelineEndSec
			if t.CharStart < len(segment.RawTimestamps) {
				startSec = segment.TimelineStartSec + segment.RawTimestamps[t.CharStart]
			}
			if t.CharEnd < len(segment.RawTimestamps) {
				endSec = segment.TimelineStartSec + segment.RawTimestamps[t.CharEnd]
			} else if t.CharEnd == len([]rune(segment.TextZh)) && len(segment.RawTimestamps) > 0 {
				// Last word: estimate end from median inter-token gap
				last := segment.RawTimestamps[len(segment.RawTimestamps)-1]
				medianGap := estimateMedianGap(segment.RawTimestamps)
				endSec = segment.TimelineStartSec + last + medianGap
			}
			if endSec > segment.TimelineEndSec { endSec = segment.TimelineEndSec }
			wordEntries = append(wordEntries, models.WordEntry{
				Text: t.Text, CharStart: t.CharStart, CharEnd: t.CharEnd,
				StartSec: startSec, EndSec: endSec,
				Pinyin: pinyin, Trans: trans,
			})
		}
	} else {
		// Fallback: proportional character-count distribution (whisper path)
		totalChars := 0
		for _, t := range words { totalChars += t.CharEnd - t.CharStart }
		timeCursor := segment.TimelineStartSec
		for _, t := range words {
			entry, err := p.Dictionary.Lookup(t.Text)
			pinyin, trans := "", ""
			if err == nil {
				pinyin = entry.Pinyin
				if len(entry.Meanings) > 0 { trans = entry.Meanings[0] }
			}
			charFraction := float64(t.CharEnd-t.CharStart) / float64(totalChars)
			wordDuration := segDuration * charFraction
			if totalChars == 0 { wordDuration = segDuration / float64(len(words)) }
			wordEnd := timeCursor + wordDuration
			if wordEnd > segment.TimelineEndSec { wordEnd = segment.TimelineEndSec }
			wordEntries = append(wordEntries, models.WordEntry{
				Text: t.Text, CharStart: t.CharStart, CharEnd: t.CharEnd,
				StartSec: timeCursor, EndSec: wordEnd,
				Pinyin: pinyin, Trans: trans,
			})
			timeCursor = wordEnd
		}
	}
	segment.Words = wordEntries
	dictMs := time.Since(dictStart).Milliseconds()
	segment.TextPinyin = buildPinyinText(wordEntries)
	segment.TextEn = buildEnText(wordEntries)
	segment.TSFile = segmentFileName(chunk.SegmentID) + ".ts"

	storeStart := time.Now()
	if err := p.Store.Write(segment); err != nil {
		p.Logger.Error("pipeline", "store_failed", "id", chunk.SegmentID, "err", err)
		return
	}
	storeMs := time.Since(storeStart).Milliseconds()

	// Update subtitled playlist — only segments with completed ASR.
	// This GUARANTEES the invariant: no audio without subtitles.
	p.updateSubtitledPlaylist(chunk.SegmentID)
	p.asrCompleted.Add(1)

	totalMs := time.Since(t0).Milliseconds()
	p.Logger.Info("pipeline", "asr_done",
		"id", chunk.SegmentID, "asr_ms", asrMs, "tok_ms", tokenizeMs,
		"dict_ms", dictMs, "store_ms", storeMs, "total_ms", totalMs,
		"words", len(wordEntries),
	)
}

// updateSubtitledPlaylist writes playlist.m3u8 containing ONLY segments
// that have completed ASR. Uses live.m3u8 (ffmpeg's authoritative list of
// existing .ts files) to avoid referencing files that don't exist yet due
// to ffmpeg's buffered async I/O.
func (p *Pipeline) updateSubtitledPlaylist(latestCompletedID int) {
	p.subtitledMu.Lock()
	defer p.subtitledMu.Unlock()

	if latestCompletedID > p.subtitledLastID {
		p.subtitledLastID = latestCompletedID
	}

	hlsDir := filepath.Join(p.OutputDir, "hls")
	playlistPath := filepath.Join(hlsDir, "playlist.m3u8")

	// Read live.m3u8 to get the set of ts files ffmpeg has actually written.
	// This avoids referencing files that don't exist yet due to I/O buffering.
	existingFiles := readLivePlaylistSegments(filepath.Join(hlsDir, "live.m3u8"))

	// Keep last hour of subtitled segments
	window := p.HLSTime * 1200
	startID := p.subtitledLastID - window
	if startID < 0 { startID = 0 }

	f, err := os.Create(playlistPath)
	if err != nil { return }
	defer f.Close()

	fmt.Fprintf(f, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n",
		p.HLSTime, startID)

	count := 0
	for id := startID; id <= p.subtitledLastID; id++ {
		segFile := segmentFileName(id) + ".ts"
		// Only include if ffmpeg has written the file AND ASR is complete
		if existingFiles[segFile] {
			segTime := time.UnixMilli(int64((p.epochBase + float64(id)*float64(p.HLSTime)) * 1000)).UTC()
			fmt.Fprintf(f, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%.3f,\n%s\n",
				segTime.Format("2006-01-02T15:04:05.000Z"), float64(p.HLSTime), segFile)
			count++
		}
	}
	if count > 0 {
		p.subtitledFirstID = startID
	}
}

// readLivePlaylistSegments parses an HLS playlist and returns the set of
// .ts filenames referenced in it.
// estimateMedianGap computes the median gap between consecutive timestamps
// (in seconds). Used to estimate the end time of the last word when the
// raw timestamps array is one element short.
func estimateMedianGap(ts []float64) float64 {
	if len(ts) < 2 { return 0.5 } // default 500ms
	gaps := make([]float64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		gap := ts[i] - ts[i-1]
		if gap > 0 { gaps = append(gaps, gap) }
	}
	if len(gaps) == 0 { return 0.5 }
	sort.Float64s(gaps)
	return gaps[len(gaps)/2]
}

func readLivePlaylistSegments(path string) map[string]bool {
	files := make(map[string]bool)
	data, err := os.ReadFile(path)
	if err != nil {
		return files
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".ts") {
			files[line] = true
		}
	}
	return files
}

func (p *Pipeline) SubtitledSegmentsReady() int {
	p.subtitledMu.Lock()
	defer p.subtitledMu.Unlock()
	if p.subtitledLastID > 0 {
		return p.subtitledLastID - p.subtitledFirstID + 1
	}
	return 0
}

func buildPinyinText(words []models.WordEntry) string {
	var s string
	for i, w := range words {
		if i > 0 { s += " " }
		s += w.Pinyin
	}
	return s
}

func buildEnText(words []models.WordEntry) string {
	var s string
	for i, w := range words {
		if i > 0 { s += " " }
		s += w.Trans
	}
	return s
}

func segmentFileName(segmentID int) string { return fmt.Sprintf("%09d", segmentID) }

func (p *Pipeline) writeEmptyPlaylist(hlsDir string) {
	path := filepath.Join(hlsDir, "playlist.m3u8")
	if _, err := os.Stat(path); err == nil {
		return // already exists
	}
	f, err := os.Create(path)
	if err != nil { return }
	defer f.Close()
	fmt.Fprintf(f, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", p.HLSTime)
}

// waitForHLSTimeline reads the HLS playlist and extracts the first
// #EXT-X-PROGRAM-DATE-TIME as the authoritative timeline base.
func (p *Pipeline) waitForHLSTimeline(ctx context.Context, hlsDir string, timeout time.Duration) float64 {
	playlistPath := filepath.Join(hlsDir, "live.m3u8")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return float64(time.Now().UnixMilli()) / 1000.0
		default:
		}
		data, err := os.ReadFile(playlistPath)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:") {
				ts := strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
				t, err := time.Parse("2006-01-02T15:04:05.999Z", strings.TrimSpace(ts))
				if err != nil {
					t, err = time.Parse("2006-01-02T15:04:05Z", strings.TrimSpace(ts))
					if err != nil {
						continue
					}
				}
				return float64(t.UnixMilli()) / 1000.0
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Fallback: use current time
	return float64(time.Now().UnixMilli()) / 1000.0
}

// statsReporter logs ingest-vs-ASR progress every 5 seconds.
func (p *Pipeline) statsReporter(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastIngested, lastTranscribed int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ingested := p.Ingestor.Stats().SegmentsIngested
			transcribed := p.asrCompleted.Load()
			deltaIngest := ingested - lastIngested
			deltaTrans := transcribed - lastTranscribed
			lag := ingested - transcribed
			p.Logger.Info("pipeline", "stats",
				"ingested", ingested,
				"transcribed", transcribed,
				"lag", lag,
				"d_ingest", deltaIngest,
				"d_trans", deltaTrans,
			)
			lastIngested = ingested
			lastTranscribed = transcribed
		}
	}
}

// logStderr reads lines from an io.Reader and logs them through the logger.
// Every ffmpeg line gets a timestamp prefix.
func logStderr(r io.Reader, logger logging.Logger, module string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			logger.Info(module, "line", "msg", line)
		}
	}
}
