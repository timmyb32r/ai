package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	pinyinlib "github.com/criradio/server/internal/pinyin"
	"github.com/criradio/server/internal/storage"
	"github.com/criradio/server/internal/tokenizer"
	"github.com/criradio/server/internal/unihan"
)

const defaultASRWorkers = 8

type batchJob struct {
	samples    []float32
	firstSegID int
	batchSize  int
}

type Pipeline struct {
	Ingestor    ingest.Ingestor
	Transcriber asr.Transcriber
	Tokenizer   tokenizer.Tokenizer
	Dictionary  dictionary.Dictionary
	Unihan      *unihan.Resolver      // optional: fills probable readings for single-char "?" words
	Cedict      dictionary.Dictionary // optional: word-level pinyin fallback for multi-char "?" words
	Store       storage.MetadataStore
	Logger      logging.Logger
	OutputDir   string
	HLSTime     int
	ASRBatchSize int

	batchQueue  chan batchJob
	resultCh    chan *models.TranscriptSegment
	storeMu     sync.Mutex
	nextStoreID int
	pendingSegs map[int]*models.TranscriptSegment

	// lastEmittedSeg — последний сегмент, прошедший processDownstream.
	// Используется dedupBoundary для межсегментной текстовой дедупликации.
	// Защищён storeMu (emitOrdered — единственный writer).
	lastEmittedSeg *models.TranscriptSegment

	hlsStdin io.WriteCloser
	hlsCmd   *exec.Cmd
	hlsMu    sync.Mutex

	// Subtitled playlist: only segments with completed ASR.
	subtitledMu      sync.Mutex
	subtitledLastID  int // highest segment ID with completed ASR
	subtitledFirstID int // first segment ID still in the window

	asrCompleted atomic.Int64 // total segments transcribed by whisper
	epochBase    float64      // Unix epoch at pipeline start — base for monotonic timeline

	emptyStreak     int64 // consecutive segments with empty ASR output
	emptyStreakAlarm int64 // set to 1 when alarm already fired (prevents log spam)
}

func (p *Pipeline) Run(ctx context.Context) error {
	p.Logger.Info("pipeline", "starting")
	// Clean old metadata from previous runs to prevent stale segments
	// with high segment_IDs from polluting ReadLatest queries.
	// The cleanup loop (6h TTL) handles long-running stale data; this
	// is a fresh-start reset for clean segment_ID numbering.
	os.RemoveAll(filepath.Join(p.OutputDir, "metadata"))
	os.MkdirAll(filepath.Join(p.OutputDir, "metadata"), 0o755)
	os.Remove(filepath.Join(p.OutputDir, "hls", "playlist.m3u8"))
	defer func() {
		p.stopHLSEncoder()
		p.Logger.Info("pipeline", "stopped")
	}()

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
	// Reap process to prevent zombies on restart.
	go func() {
		if err := hlsCmd.Wait(); err != nil {
			p.Logger.Warn("pipeline", "hls_encoder_exited", "err", err)
		}
	}()
	// Log ffmpeg stderr through our logger (adds timestamps to every line)
	go logStderr(stderrPipe, p.Logger, "ffmpeg-hls")
	p.Logger.Info("pipeline", "hls_encoder_started")

	// Periodically clean up old metadata to bound disk usage.
	// 6h TTL = 2× the DVR window (3h at hls_list_size=3600 × 3s/segment).
	// 5min interval = cleanup runs ~2× per index flush cycle.
	p.Store.StartCleanupLoop(ctx, 6*time.Hour, 5*time.Minute)

	pcmCh, err := p.Ingestor.Start(ctx)
	if err != nil {
		return err
	}

	p.batchQueue = make(chan batchJob, 256)
	for i := 0; i < defaultASRWorkers; i++ {
		go p.batchWorker(ctx)
	}

	// Default ASR batch size when not set (backward compat with tests)
	if p.ASRBatchSize <= 0 {
		p.ASRBatchSize = 2
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
	p.resultCh = make(chan *models.TranscriptSegment, 256)
	p.pendingSegs = make(map[int]*models.TranscriptSegment)
	p.nextStoreID = 0
	go p.orderedCollector(ctx)
	go p.statsReporter(ctx)

	segmentID := 0
	var batchBuf []models.PCMChunk
	for {
		select {
		case <-ctx.Done():
			p.flushBatch(ctx, batchBuf)
			close(p.batchQueue)
			return ctx.Err()
		case chunk, ok := <-pcmCh:
			if !ok {
				p.flushBatch(ctx, batchBuf)
				close(p.batchQueue)
				return fmt.Errorf("ingest stream ended unexpectedly")
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
			segmentID++

			// Добавляем чанк в буфер
			batchBuf = append(batchBuf, chunk)

			// Для batch_size > 1: не отправляем батч, пока в буфере только segment 0.
			if p.ASRBatchSize > 1 && len(batchBuf) == 1 && batchBuf[0].SegmentID == 0 {
				continue
			}

			// Если буфер заполнен: создать BatchJob, сдвинуть окно
			if len(batchBuf) >= p.ASRBatchSize {
				p.submitBatch(ctx, batchBuf[:p.ASRBatchSize])
				// Sliding window: сдвиг на 1, оставляем последние batchSize-1 элементов
				batchBuf = batchBuf[1:]
			}
		}
	}
}

func (p *Pipeline) stopHLSEncoder() {
	p.hlsMu.Lock()
	defer p.hlsMu.Unlock()
	if p.hlsStdin != nil {
		p.hlsStdin.Close()
		p.hlsStdin = nil
	}
	if p.hlsCmd != nil && p.hlsCmd.Process != nil {
		p.hlsCmd.Process.Kill()
	}
	p.hlsCmd = nil
}

func (p *Pipeline) writePCMToHLS(samples []float32) {
	p.hlsMu.Lock()
	defer p.hlsMu.Unlock()
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := s
		if v > 1.0 {
			v = 1.0
		}
		if v < -1.0 {
			v = -1.0
		}
		val := int16(v * 32767)
		buf[i*2] = byte(val)
		buf[i*2+1] = byte(val >> 8)
	}
	if _, err := p.hlsStdin.Write(buf); err != nil {
		p.Logger.Warn("pipeline", "hls_write_error", "err", err)
	}
}

func (p *Pipeline) batchWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-p.batchQueue:
			if !ok {
				return
			}
			p.processBatch(job)
		}
	}
}

func (p *Pipeline) processBatch(job batchJob) {
	// Silence check on stitched PCM
	if rms := sampleRMS(job.samples); rms < 1e-6 {
		p.Logger.Warn("pipeline", "pcm_silence",
			"batch_first_id", job.firstSegID, "rms", rms,
		)
	}

	// ASR
	asrStart := time.Now()
	stitchedSeg, err := p.Transcriber.Transcribe(job.samples, job.firstSegID)
	asrMs := time.Since(asrStart).Milliseconds()

	if err != nil {
		p.Logger.Error("pipeline", "asr_failed",
			"batch_first_id", job.firstSegID, "err", err)
		stitchedSeg = &models.TranscriptSegment{
			SegmentID: job.firstSegID, TextZh: "",
		}
	}

	// Split batch result — извлекаем только первый сегмент
	seg := p.splitBatchResult(stitchedSeg, job)
	if seg == nil {
		return // пустой/ошибочный батч
	}

	// Empty streak tracking
	p.trackEmptyStreak(seg)

	// Отправить в ordered collector
	select {
	case p.resultCh <- seg:
	case <-time.After(5 * time.Second):
		p.Logger.Error("pipeline", "result_ch_full", "id", seg.SegmentID)
	}

	p.Logger.Info("pipeline", "batch_complete",
		"first_id", job.firstSegID, "batch_size", job.batchSize,
		"asr_ms", asrMs)
}

// submitBatch склеивает PCM-чанки и отправляет батч в очередь воркеров.
func (p *Pipeline) submitBatch(ctx context.Context, chunks []models.PCMChunk) {
	if len(chunks) == 0 {
		return
	}
	totalSamples := 0
	for _, c := range chunks {
		totalSamples += len(c.Samples)
	}
	stitched := make([]float32, 0, totalSamples)
	for _, c := range chunks {
		stitched = append(stitched, c.Samples...)
	}
	job := batchJob{
		samples:    stitched,
		firstSegID: chunks[0].SegmentID,
		batchSize:  len(chunks),
	}
	select {
	case p.batchQueue <- job:
	case <-ctx.Done():
		return
	}
}

// flushBatch обрабатывает остаток буфера при завершении ingest-потока.
func (p *Pipeline) flushBatch(ctx context.Context, buf []models.PCMChunk) {
	if len(buf) == 0 {
		return
	}
	p.submitBatch(ctx, buf)
}

// splitBatchResult разбивает результат ASR батча на индивидуальный сегмент.
// Для batch_size=1 возвращает as-is.
// Для batch_size>1: фильтрует слова/тайминги, относящиеся к первому сегменту.
func (p *Pipeline) splitBatchResult(
	stitched *models.TranscriptSegment,
	job batchJob,
) *models.TranscriptSegment {
	if job.batchSize <= 1 {
		stitched.SegmentID = job.firstSegID
		return stitched
	}

	// Граница первого сегмента в секундах от начала stitched audio.
	// guardSeconds исключает символы вблизи границы из kept-порции —
	// они будут правильно распознаны в СЛЕДУЮЩЕМ батче, где у них
	// есть полноценный левый контекст. Это предотвращает дубликаты
	// (включая ситуацию, когда ASR выдаёт РАЗНЫЕ иероглифы для одного
	// и того же слога на границе в соседних батчах).
	const guardSeconds = 0.3
	boundary := float64(p.HLSTime) - guardSeconds

	// --- Определяем текст для первого сегмента ---
	var keptText string
	var keptTimestamps []float64

	if len(stitched.RawTimestamps) > 0 {
		// === Sherpa-onnx path: character-level timestamps ===
		// Оставляем только символы с timestamp строго до boundary.
		// Символы в guard zone [boundary, HLSTime) отбрасываются —
		// они попадут в следующий батч где будут правильно распознаны.
		splitIdx := 0
		for splitIdx < len(stitched.RawTimestamps) && stitched.RawTimestamps[splitIdx] < boundary {
			splitIdx++
		}
		if splitIdx == 0 && len(stitched.RawTimestamps) > 0 {
			splitIdx = 1
		}

		chars := []rune(stitched.TextZh)
		keptText = string(chars[:min(splitIdx, len(chars))])
		keptTimestamps = stitched.RawTimestamps[:min(splitIdx, len(stitched.RawTimestamps))]
	} else {
		// === Whisper path: phrase-level timestamps ===
		// Оставляем только фразы, которые уверенно в первом сегменте
		// (StartSec < boundary). Фразы в guard zone отбрасываются —
		// они попадут в следующий батч.
		var textParts []string
		for _, w := range stitched.Words {
			if w.StartSec < boundary {
				textParts = append(textParts, w.Text)
			}
		}
		keptText = strings.Join(textParts, "")
	}

	if keptText == "" {
		return &models.TranscriptSegment{
			SegmentID:  job.firstSegID,
			TextZh:     "",
			HasContent: false,
		}
	}

	result := &models.TranscriptSegment{
		SegmentID:     job.firstSegID,
		TextZh:        keptText,
		RawTimestamps: keptTimestamps,
	}

	return result
}

// orderedCollector читает результаты из resultCh и передаёт их в emitOrdered
// для упорядоченной обработки.
func (p *Pipeline) orderedCollector(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case seg := <-p.resultCh:
			p.emitOrdered(seg)
		}
	}
}

// emitOrdered гарантирует, что сегменты сохраняются в порядке возрастания segment ID.
// Если сегмент пришёл не по порядку — помещается в pendingSegs до прихода ожидаемого.
func (p *Pipeline) emitOrdered(seg *models.TranscriptSegment) {
	p.storeMu.Lock()
	defer p.storeMu.Unlock()

	if seg.SegmentID == p.nextStoreID {
		// Наш черёд — дедупликация, затем downstream, затем drain pending
		p.dedupBoundary(seg)
		p.processDownstream(seg)
		p.nextStoreID++

		for {
			if next, ok := p.pendingSegs[p.nextStoreID]; ok {
				delete(p.pendingSegs, p.nextStoreID)
				p.dedupBoundary(next)
				p.processDownstream(next)
				p.nextStoreID++
			} else {
				break
			}
		}
	} else if seg.SegmentID > p.nextStoreID {
		// Пришёл будущий сегмент — в pending
		p.pendingSegs[seg.SegmentID] = seg
		if len(p.pendingSegs) > 100 {
			p.Logger.Error("pipeline", "pending_segs_overflow",
				"count", len(p.pendingSegs), "next_id", p.nextStoreID,
				"msg", "Pending segments exceeded 100 — possible segment loss or queue stall",
			)
		}
	} else {
		// seg.SegmentID < p.nextStoreID: уже обработан — игнорируем
		p.Logger.Warn("pipeline", "duplicate_or_stale_segment", "id", seg.SegmentID)
	}
}

// trackEmptyStreak отслеживает последовательность пустых результатов ASR.
func (p *Pipeline) trackEmptyStreak(segment *models.TranscriptSegment) {
	if segment.TextZh == "" {
		streak := atomic.AddInt64(&p.emptyStreak, 1)
		if streak >= 10 && atomic.CompareAndSwapInt64(&p.emptyStreakAlarm, 0, 1) {
			p.Logger.Error("pipeline", "asr_empty_streak_alarm",
				"streak", streak,
				"msg", "ASR has produced 10+ empty segments in a row",
			)
		}
	} else {
		atomic.StoreInt64(&p.emptyStreak, 0)
		atomic.StoreInt64(&p.emptyStreakAlarm, 0)
	}
}

// dedupBoundary выполняет текстовую дедупликацию на стыке сегментов.
// Сравнивает первые до 5 рун текущего сегмента с последними до 5 рунами
// предыдущего. При перекрытии >= 2 рун удаляет дубликат из начала сегмента.
//
// Это safety net поверх guard zone в splitBatchResult: guard zone
// предотвращает большинство дубликатов на границе, а текстовая дедупликация
// ловит оставшиеся (например, когда ASR всё же включил хвост в оба сегмента).
func (p *Pipeline) dedupBoundary(seg *models.TranscriptSegment) {
	// Сегмент 0: не с чем сравнивать, но запоминаем для следующего
	if seg.SegmentID == 0 {
		p.lastEmittedSeg = seg
		return
	}

	prevSeg := p.lastEmittedSeg
	if prevSeg == nil || prevSeg.TextZh == "" || seg.TextZh == "" {
		p.lastEmittedSeg = seg
		return
	}

	prevRunes := []rune(prevSeg.TextZh)
	curRunes := []rune(seg.TextZh)

	const windowSize = 5
	const minOverlap = 2

	prevTail := prevRunes
	if len(prevTail) > windowSize {
		prevTail = prevTail[len(prevTail)-windowSize:]
	}
	curHead := curRunes
	if len(curHead) > windowSize {
		curHead = curHead[:windowSize]
	}

	// Ищем максимальное перекрытие (от большего к меньшему)
	overlap := 0
	for l := min(len(prevTail), len(curHead)); l >= minOverlap; l-- {
		if string(prevTail[len(prevTail)-l:]) == string(curHead[:l]) {
			overlap = l
			break
		}
	}

	if overlap > 0 {
		seg.TextZh = string(curRunes[overlap:])

		// Корректируем RawTimestamps
		trimIdx := overlap
		if trimIdx > len(seg.RawTimestamps) {
			trimIdx = len(seg.RawTimestamps)
		}
		seg.RawTimestamps = seg.RawTimestamps[trimIdx:]

		// Words НЕ корректируем — processDownstream перезаписывает их
		// через токенизацию свежего TextZh.

		p.Logger.Info("pipeline", "boundary_dedup",
			"seg_id", seg.SegmentID,
			"overlap", overlap,
			"removed", string(curRunes[:overlap]),
		)
	}

	p.lastEmittedSeg = seg
}

// processDownstream выполняет полную обработку одного сегмента:
// токенизация, словарь, pinyin, timestamps, store, playlist.
// Вызывается ТОЛЬКО из orderedCollector (гарантированный порядок).
func (p *Pipeline) processDownstream(segment *models.TranscriptSegment) {
	t0 := time.Now()

	// Monotonic timeline: epochBase + segmentID * HLSTime.
	segment.TimelineStartSec = p.epochBase + float64(segment.SegmentID)*float64(p.HLSTime)
	segment.TimelineEndSec = p.epochBase + float64(segment.SegmentID+1)*float64(p.HLSTime)

	tokStart := time.Now()
	words := p.Tokenizer.Segment(segment.TextZh)
	tokenizeMs := time.Since(tokStart).Milliseconds()

	dictStart := time.Now()
	segDuration := segment.TimelineEndSec - segment.TimelineStartSec
	wordEntries := make([]models.WordEntry, 0, len(words))

	hasRawTimestamps := len(segment.RawTimestamps) > 0
	if hasRawTimestamps {
		for _, t := range words {
			entry, err := p.Dictionary.Lookup(t.Text)
			pinyin, trans := "", ""
			var senses []models.WordSense
			var charPinyin []string
			if err == nil {
				pinyin = entry.Pinyin
				if len(entry.Meanings) > 0 {
					trans = entry.Meanings[0]
				}
				for _, s := range entry.Senses {
					senses = append(senses, models.WordSense{
						Number: s.Number, Labels: s.Labels,
						Text: s.Text, Notes: s.Notes,
					})
				}
				charPinyin = entry.CharPinyins
			} else {
				chars := []rune(t.Text)
				var parts []string
				for i, ch := range chars {
					readings := p.Dictionary.CharReadings(string(ch))
					switch len(readings) {
					case 0:
						charPinyin = append(charPinyin, "")
					case 1:
						charPinyin = append(charPinyin, readings[0])
						parts = append(parts, readings[0])
					default:
						if resolved := resolveByContext(i, chars, p.Dictionary); resolved != "" {
							charPinyin = append(charPinyin, resolved)
							parts = append(parts, resolved)
						} else {
							if cp := p.Dictionary.LookupPinyin(string(ch)); cp != "" && !strings.ContainsAny(cp, ",;") {
								charPinyin = append(charPinyin, cp)
								parts = append(parts, cp)
							} else {
								charPinyin = append(charPinyin, "?")
								parts = append(parts, "?")
							}
						}
					}
				}
				if len(parts) > 0 {
					pinyin = strings.Join(parts, " ")
				}
			}
			startSec := segment.TimelineStartSec
			endSec := segment.TimelineEndSec
			if t.CharStart < len(segment.RawTimestamps) {
				startSec = segment.TimelineStartSec + segment.RawTimestamps[t.CharStart]
			}
			if t.CharEnd < len(segment.RawTimestamps) {
				endSec = segment.TimelineStartSec + segment.RawTimestamps[t.CharEnd]
			} else if t.CharEnd == len([]rune(segment.TextZh)) && len(segment.RawTimestamps) > 0 {
				last := segment.RawTimestamps[len(segment.RawTimestamps)-1]
				medianGap := estimateMedianGap(segment.RawTimestamps)
				endSec = segment.TimelineStartSec + last + medianGap
			}
			if endSec > segment.TimelineEndSec {
				endSec = segment.TimelineEndSec
			}
			wordEntries = append(wordEntries, models.WordEntry{
				Text: t.Text, CharStart: t.CharStart, CharEnd: t.CharEnd,
				StartSec: startSec, EndSec: endSec,
				Pinyin: pinyin, CharPinyin: charPinyin, Trans: trans, Senses: senses,
			})
		}
	} else {
		// Fallback: proportional character-count distribution (whisper path)
		totalChars := 0
		for _, t := range words {
			totalChars += t.CharEnd - t.CharStart
		}
		timeCursor := segment.TimelineStartSec
		for _, t := range words {
			entry, err := p.Dictionary.Lookup(t.Text)
			pinyin, trans := "", ""
			var senses []models.WordSense
			var charPinyin []string
			if err == nil {
				pinyin = entry.Pinyin
				if len(entry.Meanings) > 0 {
					trans = entry.Meanings[0]
				}
				for _, s := range entry.Senses {
					senses = append(senses, models.WordSense{
						Number: s.Number, Labels: s.Labels,
						Text: s.Text, Notes: s.Notes,
					})
				}
				charPinyin = entry.CharPinyins
			} else {
				chars := []rune(t.Text)
				var parts []string
				for i, ch := range chars {
					readings := p.Dictionary.CharReadings(string(ch))
					switch len(readings) {
					case 0:
						charPinyin = append(charPinyin, "")
					case 1:
						charPinyin = append(charPinyin, readings[0])
						parts = append(parts, readings[0])
					default:
						if resolved := resolveByContext(i, chars, p.Dictionary); resolved != "" {
							charPinyin = append(charPinyin, resolved)
							parts = append(parts, resolved)
						} else {
							if cp := p.Dictionary.LookupPinyin(string(ch)); cp != "" && !strings.ContainsAny(cp, ",;") {
								charPinyin = append(charPinyin, cp)
								parts = append(parts, cp)
							} else {
								charPinyin = append(charPinyin, "?")
								parts = append(parts, "?")
							}
						}
					}
				}
				if len(parts) > 0 {
					pinyin = strings.Join(parts, " ")
				}
			}
			charFraction := float64(t.CharEnd-t.CharStart) / float64(totalChars)
			wordDuration := segDuration * charFraction
			if totalChars == 0 {
				wordDuration = segDuration / float64(len(words))
			}
			wordEnd := timeCursor + wordDuration
			if wordEnd > segment.TimelineEndSec {
				wordEnd = segment.TimelineEndSec
			}
			wordEntries = append(wordEntries, models.WordEntry{
				Text: t.Text, CharStart: t.CharStart, CharEnd: t.CharEnd,
				StartSec: timeCursor, EndSec: wordEnd,
				Pinyin: pinyin, CharPinyin: charPinyin, Trans: trans, Senses: senses,
			})
			timeCursor = wordEnd
		}
	}
	segment.Words = wordEntries
	p.fillProbableReadings(segment.Words)
	p.attachCedictMeanings(segment.Words)
	dictMs := time.Since(dictStart).Milliseconds()
	segment.TextPinyin = buildPinyinText(wordEntries)
	segment.TextEn = buildEnText(wordEntries)
	segment.TSFile = segmentFileName(segment.SegmentID) + ".ts"
	segment.HasContent = segment.TextZh != ""

	storeStart := time.Now()
	if err := p.Store.Write(segment); err != nil {
		p.Logger.Error("pipeline", "store_failed", "id", segment.SegmentID, "err", err)
		return
	}
	storeMs := time.Since(storeStart).Milliseconds()

	// Update subtitled playlist — only segments with completed ASR.
	p.updateSubtitledPlaylist(segment.SegmentID)
	p.asrCompleted.Add(1)

	totalMs := time.Since(t0).Milliseconds()
	logFn := p.Logger.Info
	if segment.TextZh == "" {
		logFn = p.Logger.Warn
	}
	logFn("pipeline", "asr_done",
		"id", segment.SegmentID, "asr_ms", 0, "tok_ms", tokenizeMs,
		"dict_ms", dictMs, "store_ms", storeMs, "total_ms", totalMs,
		"text_len", len([]rune(segment.TextZh)), "words", len(wordEntries),
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
	if startID < 0 {
		startID = 0
	}

	f, err := os.Create(playlistPath)
	if err != nil {
		return
	}
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
	if len(ts) < 2 {
		return 0.5
	} // default 500ms
	gaps := make([]float64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		gap := ts[i] - ts[i-1]
		if gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 0.5
	}
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
		if i > 0 {
			s += " "
		}
		s += w.Pinyin
	}
	return s
}

func buildEnText(words []models.WordEntry) string {
	var s string
	for i, w := range words {
		if i > 0 {
			s += " "
		}
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
	if err != nil {
		return
	}
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
// Includes goroutine count and ASR queue depth for remote hang diagnostics.
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
				"goroutines", runtime.NumGoroutine(),
				"asr_queue", fmt.Sprintf("%d/%d", len(p.batchQueue), cap(p.batchQueue)),
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

// resolveByContext picks the correct reading for a character by checking
// adjacent 2-char sub-words in the dictionary. If both "人方" and "方式"
// use fāng for 方, we can confidently pick fāng over páng.
// fillProbableReadings resolves per-character "?" readings that the primary
// dictionary could not produce (e.g. a BKRS entry with missing pinyin "_"):
//   - multi-character words: use CEDICT's word-level pinyin, which is the
//     context-correct reading (e.g. 天问 → tiān wèn), treated as certain;
//   - single-character words: use the most-probable Unihan reading, marked
//     uncertain because it is a frequency-based guess (e.g. 的 → de + "?").
//
// Anything still unresolved keeps its "?".
func (p *Pipeline) fillProbableReadings(words []models.WordEntry) {
	for i := range words {
		w := &words[i]
		if !hasUnknownReading(w.CharPinyin) {
			continue
		}
		chars := []rune(w.Text)
		// Prefer a whole-word CEDICT reading for multi-char words; if the word
		// is in neither BKRS nor CEDICT, fall back to per-character resolution.
		if len(chars) > 1 && p.fillFromCedict(w, chars) {
			continue
		}
		p.fillPerChar(w, chars)
	}
}

// attachCedictMeanings adds CC-CEDICT English glosses to each word (as a second
// dictionary source for the word popup). It never overwrites the primary
// (BKRS) translation — the two are shown side by side in the UI. No-op when
// CEDICT is not loaded (e.g. DICT=cedict mode, where CEDICT is already primary).
func (p *Pipeline) attachCedictMeanings(words []models.WordEntry) {
	if p.Cedict == nil {
		return
	}
	for i := range words {
		if entry, err := p.Cedict.Lookup(words[i].Text); err == nil {
			words[i].CedictMeanings = entry.Meanings
		}
	}
}

// hasUnknownReading reports whether any per-character syllable is the "?" marker.
func hasUnknownReading(charPinyin []string) bool {
	for _, s := range charPinyin {
		if s == "?" {
			return true
		}
	}
	return false
}

// fillFromCedict resolves a multi-character word's "?" readings using CEDICT's
// word-level pinyin (space-separated, one syllable per character). CEDICT gives
// the context-correct reading for the specific word (e.g. 天问 → "Tian1 wen4"),
// so the result is treated as certain (no uncertainty marker). Words missing
// from CEDICT — or whose pinyin does not align 1:1 with the characters — are
// left untouched.
func (p *Pipeline) fillFromCedict(w *models.WordEntry, chars []rune) bool {
	if p.Cedict == nil {
		return false
	}
	entry, err := p.Cedict.Lookup(w.Text)
	if err != nil {
		return false
	}
	fields := strings.Fields(entry.Pinyin)
	if len(fields) != len(chars) {
		return false
	}
	syllables := make([]string, len(fields))
	for i, f := range fields {
		syl := pinyinlib.NumberedToDiacritic(f)
		if !pinyinlib.IsValidHieroglyphPinyin(syl) {
			return false // don't emit anything unless the whole word is clean
		}
		syllables[i] = syl
	}
	if len(w.CharPinyin) != len(syllables) {
		w.CharPinyin = make([]string, len(syllables))
	}
	copy(w.CharPinyin, syllables)
	w.CharPinyinUncertain = nil // CEDICT word reading is authoritative
	w.Pinyin = strings.Join(syllables, " ")
	return true
}

// fillPerChar resolves each remaining "?" position independently — the general
// fallback when neither BKRS nor CEDICT yields a whole-word reading (e.g. 一状,
// which is in no dictionary as a word). Per character:
//   - exactly one dictionary reading → set it deterministically (certain);
//   - several readings → most-probable Unihan reading, marked uncertain
//     (e.g. 一 → yī with a trailing "?" in the UI);
//   - unknown to both → keep "?".
//
// It also covers lone single-character words (的 → de + "?").
func (p *Pipeline) fillPerChar(w *models.WordEntry, chars []rune) {
	if len(w.CharPinyin) != len(chars) {
		return
	}
	uncertain := w.CharPinyinUncertain
	changed := false
	for i, ch := range chars {
		if w.CharPinyin[i] != "?" {
			continue
		}
		var readings []string
		if p.Dictionary != nil {
			readings = p.Dictionary.CharReadings(string(ch))
		}
		if len(readings) == 1 {
			// Unambiguous character — deterministic, no uncertainty marker.
			w.CharPinyin[i] = readings[0]
			changed = true
			continue
		}
		// Ambiguous (>1) or unknown (0) — take the most-probable Unihan reading.
		if reading, ok := p.Unihan.Lookup(ch); ok {
			w.CharPinyin[i] = reading.Pinyin
			if uncertain == nil {
				uncertain = make([]bool, len(chars))
			}
			uncertain[i] = true
			changed = true
		}
	}
	if uncertain != nil {
		w.CharPinyinUncertain = uncertain
	}
	// Rebuild the word-level pinyin from the resolved syllables when the source
	// was missing/ambiguous, so the full-line romanisation is clean too.
	if changed && (w.Pinyin == "" || w.Pinyin == "_" || w.Pinyin == "?" || strings.ContainsAny(w.Pinyin, ",;?")) {
		w.Pinyin = strings.Join(w.CharPinyin, " ")
	}
}

// sampleRMS estimates RMS amplitude from a sparse sample of the PCM buffer.
// Returns 0.0 for truly silent (all-zero) audio.
func sampleRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	// Check first, middle, and last 1000 samples — enough to detect silence
	// without touching the entire 48000-sample buffer.
	indices := []int{0, len(samples) / 2, len(samples) - 1000}
	if indices[2] < 0 {
		indices[2] = 0
	}
	var sum float64
	count := 0
	for _, base := range indices {
		end := base + 1000
		if end > len(samples) {
			end = len(samples)
		}
		for i := base; i < end; i++ {
			v := float64(samples[i])
			sum += v * v
		}
		count += end - base
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count) // mean squared — sqrt not needed for threshold check
}

func resolveByContext(charIdx int, chars []rune, dict dictionary.Dictionary) string {
	target := string(chars[charIdx])
	readings := dict.CharReadings(target)
	if len(readings) <= 1 {
		return ""
	}
	// Try left+current window.
	if charIdx > 0 {
		sub := string(chars[charIdx-1 : charIdx+1])
		if entry, err := dict.Lookup(sub); err == nil && len(entry.CharPinyins) == 2 {
			for _, r := range readings {
				if entry.CharPinyins[1] == r {
					return r
				}
			}
		}
	}
	// Try current+right window.
	if charIdx < len(chars)-1 {
		sub := string(chars[charIdx : charIdx+2])
		if entry, err := dict.Lookup(sub); err == nil && len(entry.CharPinyins) == 2 {
			for _, r := range readings {
				if entry.CharPinyins[0] == r {
					return r
				}
			}
		}
	}
	return ""
}
