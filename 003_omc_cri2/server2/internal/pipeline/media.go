package pipeline

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sampleRate = 16000

// pcmRing is indexed by sample number rather than arrival time. Appending never
// blocks on inference; old samples are overwritten at the configured bound.
type pcmRing struct {
	mu    sync.Mutex
	data  []float32
	total int64
	last  time.Time
}

func newPCMRing(seconds int) *pcmRing {
	return &pcmRing{data: make([]float32, seconds*sampleRate), last: time.Now()}
}
func (r *pcmRing) append(samples []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range samples {
		r.data[r.total%int64(len(r.data))] = s
		r.total++
	}
	r.last = time.Now()
}
func (r *pcmRing) read(start, end int64) ([]float32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if start < 0 || end < start || end > r.total || start < r.total-int64(len(r.data)) {
		return nil, false
	}
	out := make([]float32, int(end-start))
	for i := range out {
		out[i] = r.data[(start+int64(i))%int64(len(r.data))]
	}
	return out, true
}
func (r *pcmRing) state() (int64, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total, r.last
}

type mediaSegment struct {
	Number   int
	File     string
	Start    float64
	Duration float64
	Offset   float64
}

// The private playlist is atomically replaced by ffmpeg. It is intentionally
// small (pending window only); public playlists are generated from committed DB rows.
func readMediaPlaylist(path string) ([]mediaSegment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1024*1024)
	var out []mediaSegment
	var start, duration float64
	haveStart := false
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:") {
			raw := strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
			var t time.Time
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"} {
				t, err = time.Parse(layout, raw)
				if err == nil {
					break
				}
			}
			if err != nil {
				return nil, fmt.Errorf("invalid HLS date: %w", err)
			}
			start = float64(t.UnixNano()) / 1e9
			haveStart = true
		} else if strings.HasPrefix(line, "#EXTINF:") {
			raw := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			duration, err = strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 30 {
				return nil, fmt.Errorf("invalid HLS duration")
			}
		} else if line != "" && !strings.HasPrefix(line, "#") {
			if !haveStart || duration == 0 || filepath.Base(line) != line || !strings.HasSuffix(line, ".ts") {
				return nil, fmt.Errorf("invalid HLS segment")
			}
			n, e := strconv.Atoi(strings.TrimSuffix(line, ".ts"))
			if e != nil || n < 0 {
				return nil, fmt.Errorf("invalid HLS sequence")
			}
			if len(out) > 0 && (n <= out[len(out)-1].Number || start < out[len(out)-1].Start) {
				return nil, fmt.Errorf("nonmonotonic HLS playlist")
			}
			out = append(out, mediaSegment{Number: n, File: line, Start: start, Duration: duration})
			start += duration
			duration = 0
		}
	}
	return out, scan.Err()
}

type tailWriter struct {
	mu   sync.Mutex
	data []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	const limit = 8192
	if n >= limit {
		w.data = append(w.data[:0], p[n-limit:]...)
	} else {
		if len(w.data)+n > limit {
			w.data = append(w.data[:0], w.data[len(w.data)+n-limit:]...)
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}
func (w *tailWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return string(w.data) }
