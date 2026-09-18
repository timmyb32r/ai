package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/criradio/server/internal/models"
)

var bg = context.Background()

func testStore(t *testing.T, o Options) *Store {
	t.Helper()
	if o.MaxBytes == 0 {
		o.MaxBytes = 64 << 20
	}
	s, err := Open(t.TempDir(), o)
	if err != nil {
		t.Fatal(err)
	}
	s.freeSpace = func() (uint64, uint64, error) { return 1 << 30, 100000, nil }
	t.Cleanup(func() { s.Close() })
	return s
}
func fixture(t *testing.T, s *Store, start float64) *models.TranscriptSegment {
	t.Helper()
	id, err := s.NextID(bg)
	if err != nil {
		t.Fatal(err)
	}
	return &models.TranscriptSegment{SegmentID: id, TSFile: fmt.Sprintf("%09d.ts", id), TimelineStartSec: start, TimelineEndSec: start + 3, TextZh: "你好", Words: []models.WordEntry{{Text: "你好", CharStart: 0, CharEnd: 2, StartSec: start, EndSec: start + 2, Pinyin: "nǐ hǎo"}}}
}
func audio(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audio.ts")
	if e := os.WriteFile(p, []byte("test-transport-stream"), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func publish(t *testing.T, s *Store, start float64) *models.TranscriptSegment {
	t.Helper()
	seg := fixture(t, s, start)
	if err := s.Publish(bg, seg, audio(t)); err != nil {
		t.Fatal(err)
	}
	return seg
}
func TestRestartPreservesArchiveAndIDReservations(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Unix() - 60)
	first := publish(t, s, start)
	unused, e := s.NextID(bg)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := Open(s.dir, s.opts)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	next.freeSpace = s.freeSpace
	read, e := next.Read(bg, first.SegmentID)
	if e != nil || read.TextZh != first.TextZh {
		t.Fatalf("archive lost: %v %+v", e, read)
	}
	second := publish(t, next, start+3)
	if second.SegmentID != unused+1 {
		t.Fatalf("reused reserved ID: %d", second.SegmentID)
	}
	view, e := next.PlaylistView(bg)
	if e != nil {
		t.Fatal(e)
	}
	if len(view) != 2 || view[0].Sequence != 0 || view[1].Sequence != 1 {
		t.Fatalf("publication sequence tied to reserved gaps: %+v", view)
	}
}
func TestPublishFailureNeverAdvertisesAndCanRetry(t *testing.T) {
	s := testStore(t, Options{})
	seg := fixture(t, s, float64(time.Now().Unix()-30))
	src := audio(t)
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if e := s.Publish(bg, seg, src); !errors.Is(e, syscall.ENOSPC) {
		t.Fatalf("want ENOSPC, got %v", e)
	}
	if _, e := s.Read(bg, seg.SegmentID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("uncommitted visible: %v", e)
	}
	if _, e := os.Stat(filepath.Join(s.dir, "hls", seg.TSFile)); !os.IsNotExist(e) {
		t.Fatalf("failed publish left audio: %v", e)
	}
	s.beforeCommit = nil
	if e := s.Publish(bg, seg, src); e != nil {
		t.Fatal(e)
	}
	if e := s.Publish(bg, seg, src); !errors.Is(e, ErrInvalid) {
		t.Fatalf("republish overwritten: %v", e)
	}
}
func TestRecoverOrphansAndMissingPublishedAudio(t *testing.T) {
	s := testStore(t, Options{})
	first := publish(t, s, float64(time.Now().Unix()-30))
	p, e := s.SnapshotPage(bg, "", first.TimelineStartSec, first.TimelineEndSec, 10, 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	hls := filepath.Join(s.dir, "hls")
	if e = os.WriteFile(filepath.Join(hls, "000099999.ts"), []byte("orphan"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(hls, ".publish-crashed"), []byte("partial"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(hls, first.TSFile)); e != nil {
		t.Fatal(e)
	}
	next, e := Open(s.dir, s.opts)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e = next.Read(bg, first.SegmentID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("missing audio advertised: %v", e)
	}
	if _, e = next.SnapshotPage(bg, p.SnapshotID, first.TimelineStartSec, first.TimelineEndSec, 10, 0); !errors.Is(e, ErrSnapshotExpired) {
		t.Fatalf("damaged snapshot survived: %v", e)
	}
	entries, e := os.ReadDir(hls)
	if e != nil || len(entries) != 0 {
		t.Fatalf("orphans: %v %v", entries, e)
	}
	id, e := next.NextID(bg)
	if e != nil || id <= first.SegmentID {
		t.Fatalf("ID reused: %d %v", id, e)
	}
}
func TestSnapshotPaginationPinnedAcrossRetentionAndRelease(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Add(-4 * time.Hour).Unix())
	first := publish(t, s, start)
	second := publish(t, s, start+3)
	page, e := s.SnapshotPage(bg, "", start, start+9, 1, 0)
	if e != nil {
		t.Fatal(e)
	}
	if page.Total != 2 || page.Count != 1 || page.Segments[0].SegmentID != first.SegmentID {
		t.Fatalf("bad first page: %+v", page)
	}
	if e = s.Maintain(bg); e != nil {
		t.Fatal(e)
	}
	live, e := s.Range(bg, start, start+9, 100, 0)
	if e != nil || live.Total != 0 {
		t.Fatalf("expired archive visible: %+v %v", live, e)
	}
	page2, e := s.SnapshotPage(bg, page.SnapshotID, start, start+9, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if page2.Total != 2 || page2.Segments[0].SegmentID != second.SegmentID {
		t.Fatalf("snapshot shifted: %+v", page2)
	}
	f, e := s.OpenAudioSnapshot(bg, second.SegmentID, page.SnapshotID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ReleaseSnapshot(bg, page.SnapshotID); e != nil {
		t.Fatal(e)
	}
	if e = s.Maintain(bg); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read(bg, second.SegmentID); e != nil {
		t.Fatalf("active audio pin lost: %v", e)
	}
	f.Close()
	if e = s.Maintain(bg); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read(bg, second.SegmentID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("released data not reclaimed: %v", e)
	}
}
func TestSnapshotCapacityExpiryAndRangeBinding(t *testing.T) {
	s := testStore(t, Options{MaxSnapshots: 1})
	start := float64(time.Now().Unix() - 30)
	seg := publish(t, s, start)
	p, e := s.SnapshotPage(bg, "", start, start+3, 1, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SnapshotPage(bg, "", start, start+3, 1, 0); !errors.Is(e, ErrBusy) {
		t.Fatalf("snapshot capacity: %v", e)
	}
	if _, e = s.SnapshotPage(bg, p.SnapshotID, start, start+4, 1, 0); !errors.Is(e, ErrInvalid) {
		t.Fatalf("range mutation accepted: %v", e)
	}
	if _, e = s.db.Exec("UPDATE snapshots SET expires=?", time.Now().Unix()-1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.OpenAudioSnapshot(bg, seg.SegmentID, p.SnapshotID); !errors.Is(e, ErrSnapshotExpired) {
		t.Fatalf("expired download accepted: %v", e)
	}
	if _, e = s.SnapshotPage(bg, p.SnapshotID, start, start+3, 1, 0); !errors.Is(e, ErrSnapshotExpired) {
		t.Fatalf("expired page accepted: %v", e)
	}
	if _, e = s.SnapshotPage(bg, "", start, start+3, 1, 0); e != nil {
		t.Fatalf("expired pin retained: %v", e)
	}
}
func TestDiskReserveStopsWorkButPreservesPinnedData(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Unix() - 30)
	seg := publish(t, s, start)
	p, e := s.SnapshotPage(bg, "", start, start+3, 10, 0)
	if e != nil {
		t.Fatal(e)
	}
	s.freeSpace = func() (uint64, uint64, error) { return 1000, 100000, nil }
	if e = s.Writable(bg); !errors.Is(e, ErrLowDisk) {
		t.Fatalf("disk reserve not enforced: %v", e)
	}
	if _, e = s.SnapshotPage(bg, "", start, start+3, 10, 0); !errors.Is(e, ErrLowDisk) {
		t.Fatalf("new pin accepted: %v", e)
	}
	if e = s.Maintain(bg); e != nil {
		t.Fatal(e)
	}
	f, e := s.OpenAudioSnapshot(bg, seg.SegmentID, p.SnapshotID)
	if e != nil {
		t.Fatalf("existing pin evicted: %v", e)
	}
	f.Close()
	next := fixture(t, s, start+3)
	if e = s.Publish(bg, next, audio(t)); !errors.Is(e, ErrLowDisk) {
		t.Fatalf("lowdisk publish: %v", e)
	}
}
func TestLowInodesStopPublication(t *testing.T) {
	s := testStore(t, Options{})
	s.freeSpace = func() (uint64, uint64, error) { return 1 << 30, 4, nil }
	if e := s.Writable(bg); !errors.Is(e, ErrLowDisk) {
		t.Fatalf("low inode reserve ignored: %v", e)
	}
}
func TestIDExhaustionAndInvalidMetadata(t *testing.T) {
	s := testStore(t, Options{})
	seg := fixture(t, s, float64(time.Now().Unix()-10))
	cases := []func(*models.TranscriptSegment){
		func(s *models.TranscriptSegment) { s.TimelineStartSec = math.NaN() },
		func(s *models.TranscriptSegment) { s.TSFile = "../../outside.ts" },
		func(s *models.TranscriptSegment) { s.Words[0].EndSec = s.TimelineEndSec + 1 },
	}
	for _, bad := range cases {
		copy := *seg
		copy.Words = append([]models.WordEntry(nil), seg.Words...)
		bad(&copy)
		if e := s.Publish(bg, &copy, audio(t)); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid metadata accepted: %v", e)
		}
	}
	if _, e := s.db.Exec("UPDATE state SET value=? WHERE key='next_id'", math.MaxInt32); e != nil {
		t.Fatal(e)
	}
	if _, e := s.NextID(bg); e == nil {
		t.Fatal("ID overflow accepted")
	}
}
func TestPlaylistDelaysAudioAndCarriesRealDuration(t *testing.T) {
	s := testStore(t, Options{Delay: 20 * time.Second})
	start := float64(time.Now().Unix() - 60)
	first := fixture(t, s, start)
	first.TimelineEndSec = start + 3.024
	if e := s.Publish(bg, first, audio(t)); e != nil {
		t.Fatal(e)
	}
	second := fixture(t, s, start+10)
	if e := s.Publish(bg, second, audio(t)); e != nil {
		t.Fatal(e)
	}
	publish(t, s, float64(time.Now().Unix()-2))
	view, e := s.PlaylistView(bg)
	if e != nil {
		t.Fatal(e)
	}
	if len(view) != 2 || math.Abs(view[0].Segment.TimelineEndSec-start-3.024) > 0.0001 || !view[1].Segment.Discontinuity || view[1].DiscontinuitySequence != 1 {
		t.Fatalf("wrong delayed timeline: %+v", view)
	}
}
func TestSubscriptionHistoryHasNoGapAndSlowClientsClose(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Unix() - 400)
	publish(t, s, start)
	history, ch, cancel, e := s.Subscribe(bg, 20)
	if e != nil {
		t.Fatal(e)
	}
	defer cancel()
	if len(history) != 1 {
		t.Fatalf("history: %d", len(history))
	}
	for i := 1; i < 70; i++ {
		publish(t, s, start+float64(i*3))
	}
	count := 0
	for seg := range ch {
		count++
		if seg.SegmentID != count {
			t.Fatalf("event order %d vs %d", seg.SegmentID, count)
		}
	}
	if count != 64 {
		t.Fatalf("slow subscriber not bounded: %d", count)
	}
	if len(s.watchers) != 0 {
		t.Fatal("slow subscriber leaked")
	}
}
func TestConcurrentReadersAndPublication(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Unix() - 100)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(bg)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, _ = s.Latest(ctx, 3)
				_, _ = s.PlaylistView(ctx)
				_, _ = s.Stats(ctx)
			}
		}()
	}
	for i := 0; i < 12; i++ {
		publish(t, s, start+float64(3*i))
	}
	cancel()
	wg.Wait()
	list, e := s.Latest(bg, 100)
	if e != nil || len(list) != 12 {
		t.Fatalf("records lost: %d %v", len(list), e)
	}
}
func TestRepeatedAudioAndMetadataReadsDoNotLeakDescriptors(t *testing.T) {
	s := testStore(t, Options{})
	seg := publish(t, s, float64(time.Now().Unix()-10))
	count := func() int {
		for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
			if entries, e := os.ReadDir(dir); e == nil {
				return len(entries)
			}
		}
		t.Skip("no descriptor directory")
		return 0
	}
	before := count()
	for i := 0; i < 250; i++ {
		if _, e := s.Read(bg, seg.SegmentID); e != nil {
			t.Fatal(e)
		}
		f, e := s.OpenAudio(bg, seg.SegmentID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
	}
	if after := count(); after > before+2 {
		t.Fatalf("descriptor growth: %d -> %d", before, after)
	}
	if len(s.open) != 0 {
		t.Fatalf("download pins leaked: %+v", s.open)
	}
}
func TestRangePagesAreBoundedAndDoNotPin(t *testing.T) {
	s := testStore(t, Options{})
	start := float64(time.Now().Unix() - 30)
	for i := 0; i < 4; i++ {
		publish(t, s, start+float64(3*i))
	}
	p, e := s.Range(bg, start, start+12, 1, 2)
	if e != nil || p.Count != 1 || p.Total != 4 || p.SnapshotID != "" || p.Segments[0].SegmentID != 2 {
		t.Fatalf("bad page: %+v %v", p, e)
	}
	st, e := s.Stats(bg)
	if e != nil || st.Snapshots != 0 {
		t.Fatalf("ordinary read pinned: %+v %v", st, e)
	}
	for _, limit := range []int{0, 1001, math.MaxInt} {
		if _, e = s.Range(bg, start, start+12, limit, 0); !errors.Is(e, ErrInvalid) {
			t.Fatalf("unbounded page accepted: %d %v", limit, e)
		}
	}
}

func TestExclusiveArchiveWriter(t *testing.T) {
	s := testStore(t, Options{})
	second, e := Open(s.dir, s.opts)
	if e == nil {
		second.Close()
		t.Fatal("second writer acquired same archive")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	second, e = Open(s.dir, s.opts)
	if e != nil {
		t.Fatal(e)
	}
	second.Close()
}
func TestFailedUnlinkRetriesWithoutLosingAccounting(t *testing.T) {
	s := testStore(t, Options{})
	seg := publish(t, s, float64(time.Now().Add(-4*time.Hour).Unix()))
	path := filepath.Join(s.dir, "hls", seg.TSFile)
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	blocker := filepath.Join(path, "blocker")
	if e := os.WriteFile(blocker, []byte("block"), 0600); e != nil {
		t.Fatal(e)
	}
	before := s.audioBytes
	if e := s.Maintain(bg); e == nil {
		t.Fatal("unlink failure not surfaced")
	}
	if s.audioBytes != before {
		t.Fatal("unremoved file disappeared from quota")
	}
	if _, e := s.Read(bg, seg.SegmentID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("tombstoned file still public: %v", e)
	}
	if e := os.Remove(blocker); e != nil {
		t.Fatal(e)
	}
	if e := s.Maintain(bg); e != nil {
		t.Fatal(e)
	}
	if s.audioBytes != 0 {
		t.Fatalf("retry did not reconcile bytes: %d", s.audioBytes)
	}
	var count int
	if e := s.db.QueryRow("SELECT COUNT(*) FROM pending_delete").Scan(&count); e != nil || count != 0 {
		t.Fatalf("unlink journal retained: %d %v", count, e)
	}
}
