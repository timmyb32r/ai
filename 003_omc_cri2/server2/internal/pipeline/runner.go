package pipeline

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/criradio/server/internal/asr"
	"github.com/criradio/server/internal/models"
)

type Archive interface {
	NextID(context.Context) (int, error)
	Publish(context.Context, *models.TranscriptSegment, string) error
	Writable(context.Context) error
	Maintain(context.Context) error
}
type Recognizer interface {
	Transcribe(context.Context, []float32) (*asr.Result, error)
}
type Enricher interface {
	Process(context.Context, *asr.Result, float64, float64) (*models.TranscriptSegment, error)
}
type Config struct {
	URL, FFmpeg, OutputDir         string
	SegmentSeconds                 int
	QueueSeconds                   int
	InferenceTimeout, StallTimeout time.Duration
}
type State struct {
	Status          string    `json:"status"`
	Error           string    `json:"last_error,omitempty"`
	PendingSeconds  float64   `json:"pending_seconds"`
	Published       uint64    `json:"published_total"`
	Dropped         uint64    `json:"dropped_total"`
	Restarts        uint64    `json:"ingest_restarts"`
	LastInput       time.Time `json:"last_input_at"`
	LastPublication time.Time `json:"last_publication_at"`
}
type Runner struct {
	Config Config
	Store  Archive
	ASR    Recognizer
	Text   Enricher
	Logger *slog.Logger
	mu     sync.Mutex
	state  State
}

func (r *Runner) Snapshot() State        { r.mu.Lock(); defer r.mu.Unlock(); return r.state }
func (r *Runner) update(fn func(*State)) { r.mu.Lock(); defer r.mu.Unlock(); fn(&r.state) }
func (r *Runner) problem(status string, err error) {
	r.update(func(s *State) { s.Status = status; s.Error = err.Error() })
}

type work struct {
	mediaSegment
	path       string
	pcm        []float32
	queued     time.Time
	generation string
}

// queue owns unfinished files and caps both audio duration and elapsed waiting.
// The worker leaves its current item at the head, so it counts towards the cap.
type workQueue struct {
	mu      sync.Mutex
	items   []*work
	seconds float64
	limit   float64
	notify  chan struct{}
	dropped func(uint64)
}

func (q *workQueue) drop() {
	w := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	q.seconds -= w.Duration
	_ = os.Remove(w.path)
	q.dropped(1)
}
func (q *workQueue) expire(now time.Time) {
	for len(q.items) > 0 && now.Sub(q.items[0].queued).Seconds() > q.limit {
		q.drop()
	}
}
func (q *workQueue) offer(w *work) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire(time.Now())
	for len(q.items) > 0 && q.seconds+w.Duration > q.limit {
		q.drop()
	}
	q.items = append(q.items, w)
	q.seconds += w.Duration
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
func (q *workQueue) head() (*work, float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire(time.Now())
	if len(q.items) == 0 {
		return nil, 0
	}
	return q.items[0], q.seconds
}
func (q *workQueue) finish(w *work, publish func() error) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expire(time.Now())
	if len(q.items) == 0 || q.items[0] != w {
		return false, nil
	}
	if err := publish(); err != nil {
		return false, err
	}
	q.items[0] = nil
	q.items = q.items[1:]
	q.seconds -= w.Duration
	_ = os.Remove(w.path)
	return true, nil
}

func (r *Runner) Run(parent context.Context) error {
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	if r.Config.SegmentSeconds < 1 || r.Config.SegmentSeconds > 10 || r.Config.QueueSeconds < 2*r.Config.SegmentSeconds || r.Config.InferenceTimeout <= 0 || r.Config.StallTimeout <= 0 {
		return fmt.Errorf("invalid pipeline bounds")
	}
	if r.Config.FFmpeg == "" {
		r.Config.FFmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	spool := filepath.Join(r.Config.OutputDir, "spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(spool)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > 4 && e.Name()[:4] == "run-" {
			if err = os.RemoveAll(filepath.Join(spool, e.Name())); err != nil {
				return err
			}
		}
	}
	root, err := os.MkdirTemp(spool, "run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	pending := filepath.Join(root, "pending")
	if err = os.Mkdir(pending, 0700); err != nil {
		return err
	}
	queue := &workQueue{limit: float64(r.Config.QueueSeconds), notify: make(chan struct{}, 1), dropped: func(n uint64) { r.update(func(s *State) { s.Dropped += n }) }}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); r.consume(ctx, queue) }()
	defer func() { cancel(); <-workerDone; r.update(func(s *State) { s.Status = "stopped" }) }()
	backoff := time.Second
	for ctx.Err() == nil {
		if err = r.Store.Maintain(ctx); err == nil {
			err = r.Store.Writable(ctx)
		}
		if err != nil {
			r.problem("storage_paused", err)
			if !sleep(ctx, 5*time.Second) {
				break
			}
			continue
		}
		r.update(func(s *State) { s.Status = "connecting"; s.Error = "" })
		started := time.Now()
		err = r.capture(ctx, root, pending, queue)
		if ctx.Err() != nil {
			break
		}
		r.problem("reconnecting", err)
		r.Logger.Warn("ingest generation ended", "error", err)
		r.update(func(s *State) { s.Restarts++ })
		if time.Since(started) > time.Minute {
			backoff = time.Second
		} else {
			backoff = min(30*time.Second, backoff*2)
		}
		if !sleep(ctx, backoff) {
			break
		}
	}
	return ctx.Err()
}

func (r *Runner) consume(ctx context.Context, q *workQueue) {
	previousEnd := 0.0
	generation := ""
	lastErrorLog := time.Time{}
	var prepared *work
	var seg *models.TranscriptSegment
	for ctx.Err() == nil {
		w, seconds := q.head()
		r.update(func(s *State) { s.PendingSeconds = seconds })
		if w == nil {
			select {
			case <-ctx.Done():
				return
			case <-q.notify:
			case <-time.After(time.Second):
			}
			continue
		}
		if err := r.Store.Writable(ctx); err != nil {
			r.problem("storage_paused", err)
			if !sleep(ctx, time.Second) {
				return
			}
			continue
		}
		if prepared != w {
			inferCtx, stop := context.WithTimeout(ctx, r.Config.InferenceTimeout)
			result, err := r.ASR.Transcribe(inferCtx, w.pcm)
			if err == nil {
				seg, err = r.Text.Process(inferCtx, result, w.Start, w.Duration)
			}
			stop()
			if err != nil {
				r.problem("asr_waiting", err)
				if time.Since(lastErrorLog) > 30*time.Second {
					r.Logger.Warn("waiting for successful subtitles", "error", err)
					lastErrorLog = time.Now()
				}
				if !sleep(ctx, time.Second) {
					return
				}
				continue
			}
			prepared = w
		}
		published, err := q.finish(w, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, err := r.Store.NextID(ctx)
			if err != nil {
				return err
			}
			seg.SegmentID = id
			seg.TSFile = fmt.Sprintf("%09d.ts", id)
			seg.Discontinuity = generation != w.generation || previousEnd == 0 || math.Abs(w.Start-previousEnd) > 0.002
			return r.Store.Publish(ctx, seg, w.path)
		})
		if err != nil {
			r.problem("storage_paused", err)
			if !sleep(ctx, time.Second) {
				return
			}
			continue
		}
		if published {
			previousEnd = w.Start + w.Duration
			generation = w.generation
			r.update(func(s *State) { s.Status = "running"; s.Error = ""; s.Published++; s.LastPublication = time.Now() })
		}
	}
}

func (r *Runner) capture(parent context.Context, root, pending string, q *workQueue) error {
	// Only capture is cancelled on an upstream stall. Completed queued work belongs
	// to Run and survives this generation's process and directory cleanup.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	dir, err := os.MkdirTemp(root, "capture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	window := r.Config.QueueSeconds/r.Config.SegmentSeconds + 4
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-rw_timeout", "15000000", "-re", "-i", r.Config.URL,
		"-filter_complex", "[0:a:0]aresample=16000,asetpts=N/SR/TB,asplit=2[pcm][audio]",
		"-map", "[pcm]", "-c:a", "pcm_s16le", "-ac", "1", "-f", "s16le", "pipe:1",
		"-map", "[audio]", "-c:a", "libmp3lame", "-ac", "1", "-ar", "16000", "-b:a", "64k", "-f", "hls",
		"-hls_time", strconv.Itoa(r.Config.SegmentSeconds), "-hls_list_size", strconv.Itoa(window), "-hls_delete_threshold", "1",
		"-hls_flags", "delete_segments+program_date_time+temp_file", "-hls_segment_filename", filepath.Join(dir, "%09d.ts"), filepath.Join(dir, "ingest.m3u8")}
	cmd := exec.CommandContext(ctx, r.Config.FFmpeg, args...)
	cmd.WaitDelay = 3 * time.Second
	tail := &tailWriter{}
	cmd.Stderr = tail
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		stdout.Close()
		return err
	}
	ring := newPCMRing(r.Config.QueueSeconds + 30)
	readerDone := make(chan struct{})
	var readErr error
	go func() {
		defer close(readerDone)
		buf := make([]byte, 8192)
		for {
			n, e := io.ReadFull(stdout, buf)
			if n%2 != 0 {
				readErr = fmt.Errorf("unaligned PCM stream")
				return
			}
			if n > 0 {
				samples := make([]float32, n/2)
				for i := range samples {
					samples[i] = float32(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / 32768
				}
				ring.append(samples)
			}
			if e != nil {
				readErr = e
				return
			}
		}
	}()
	var waitOnce sync.Once
	var waitErr error
	reap := func() error {
		waitOnce.Do(func() {
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case waitErr = <-exited:
			case <-timer.C:
				cancel()
				stdout.Close()
				waitErr = <-exited
			}
		})
		return waitErr
	}
	defer func() { cancel(); stdout.Close(); _ = reap(); <-readerDone }()
	epoch := 0.0
	next := 0
	maintenance := time.Now()
	collect := func(final bool) error {
		total, last := ring.state()
		r.update(func(s *State) { s.LastInput = last })
		segs, e := readMediaPlaylist(filepath.Join(dir, "ingest.m3u8"))
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if len(segs) == 0 {
			return nil
		}
		if epoch == 0 {
			if segs[0].Number != 0 {
				return fmt.Errorf("missed initial media anchor")
			}
			epoch = segs[0].Start
		}
		for _, s := range segs {
			if s.Number < next {
				continue
			}
			s.Offset = s.Start - epoch
			if s.Number > next {
				r.update(func(st *State) { st.Dropped += uint64(s.Number - next) })
				next = s.Number
			}
			begin := int64(math.Round(s.Offset * sampleRate))
			end := int64(math.Round((s.Offset + s.Duration + float64(r.Config.SegmentSeconds)) * sampleRate))
			if final {
				end = int64(math.Round((s.Offset + s.Duration) * sampleRate))
			}
			if begin < total-int64(r.Config.QueueSeconds*sampleRate) {
				r.update(func(st *State) { st.Dropped++ })
				next = s.Number + 1
				continue
			}
			if end-begin < sampleRate/10 {
				break
			}
			pcm, ok := encodedPCM(ring, begin, end, final)
			if !ok {
				break
			}
			target := filepath.Join(pending, filepath.Base(dir)+"-"+s.File)
			if err := os.Rename(filepath.Join(dir, s.File), target); err != nil {
				return err
			}
			q.offer(&work{mediaSegment: s, path: target, pcm: pcm, queued: time.Now(), generation: filepath.Base(dir)})
			next = s.Number + 1
		}
		return nil
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-parent.Done():
			return parent.Err()
		case <-readerDone:
			// EOF closes stdout before ffmpeg finishes the final playlist. Wait reaps
			// the producer, then collect its completed tail without discarding backlog.
			_ = reap()
			if e := collect(true); e != nil {
				return e
			}
			return fmt.Errorf("ffmpeg PCM ended: %w (%s)", readErr, tail.String())
		case <-tick.C:
		}
		_, last := ring.state()
		if time.Since(last) > r.Config.StallTimeout {
			cancel()
			stdout.Close()
			_ = reap()
			<-readerDone
			if e := collect(true); e != nil {
				return e
			}
			return fmt.Errorf("upstream PCM stalled")
		}
		if time.Since(maintenance) > 5*time.Second {
			if err = r.Store.Maintain(parent); err != nil {
				return err
			}
			if err = r.Store.Writable(parent); err != nil {
				return err
			}
			maintenance = time.Now()
		}
		if err = collect(false); err != nil {
			return err
		}
	}
}
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
