// Package storage publishes immutable audio/metadata pairs with bounded retention.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/criradio/server/internal/models"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("segment not found")
	ErrSnapshotExpired = errors.New("snapshot expired or unknown")
	ErrBusy            = errors.New("capacity exceeded")
	ErrLowDisk         = errors.New("insufficient disk reserve")
	ErrClosed          = errors.New("storage closed")
	ErrInvalid         = errors.New("invalid request")
	ErrTooLarge        = errors.New("response exceeds 8 MiB; request a smaller page")
)

type Options struct {
	Retention    time.Duration
	MaxBytes     int64
	ReserveBytes uint64
	SnapshotTTL  time.Duration
	MaxSnapshots int
	Delay        time.Duration
}

type Store struct {
	lockFile     *os.File
	mu           sync.Mutex
	db           *sql.DB
	dir          string
	opts         Options
	closed       bool
	audioBytes   int64
	open         map[int]int
	watchers     map[uint64]chan models.TranscriptSegment
	watchID      uint64
	freeSpace    func() (uint64, uint64, error)
	beforeCommit func() error // deterministic fault injection for publication tests
}

type Stats struct {
	Total     int     `json:"segments_total"`
	Bytes     int64   `json:"storage_bytes"`
	Oldest    float64 `json:"oldest_segment_start_sec"`
	Newest    float64 `json:"newest_segment_end_sec"`
	Snapshots int     `json:"active_snapshots"`
}

type PlaylistEntry struct {
	Segment               models.TranscriptSegment
	Sequence              int64
	DiscontinuitySequence int64
}

type Page struct {
	Segments   []models.TranscriptSegment `json:"segments"`
	Count      int                        `json:"count"`
	Total      int                        `json:"total"`
	SnapshotID string                     `json:"snapshot_id,omitempty"`
	ExpiresAt  string                     `json:"snapshot_expires_at,omitempty"`
}

func Open(dir string, o Options) (*Store, error) {
	if o.Retention == 0 {
		o.Retention = 3 * time.Hour
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 10 << 30
	}
	if o.SnapshotTTL == 0 {
		o.SnapshotTTL = 10 * time.Minute
	}
	if o.MaxSnapshots == 0 {
		o.MaxSnapshots = 5
	}
	if o.Retention < 0 || o.MaxBytes < 0 || o.SnapshotTTL < 0 || o.MaxSnapshots < 1 || o.Delay < 0 {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(filepath.Join(dir, "hls"), 0750); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(abs, "archive.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("archive already in use: %w", err)
	}
	success := false
	defer func() {
		if !success {
			syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			lockFile.Close()
		}
	}()
	db, err := sql.Open("sqlite", filepath.Join(abs, "archive.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{lockFile: lockFile, db: db, dir: abs, opts: o, open: map[int]int{}, watchers: map[uint64]chan models.TranscriptSegment{}}
	s.freeSpace = func() (uint64, uint64, error) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(abs, &st); err != nil {
			return 0, 0, err
		}
		return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Ffree), nil
	}
	schema := []string{
		"PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000",
		"PRAGMA wal_autocheckpoint=256", "PRAGMA journal_size_limit=4194304",
		"CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value INTEGER NOT NULL)",
		"INSERT OR IGNORE INTO state VALUES ('next_id',0),('next_seq',0),('discontinuities',0)",
		"CREATE TABLE IF NOT EXISTS segments (id INTEGER PRIMARY KEY, seq INTEGER UNIQUE NOT NULL, start REAL NOT NULL, end REAL NOT NULL, file TEXT UNIQUE NOT NULL, data BLOB NOT NULL, bytes INTEGER NOT NULL, visible INTEGER NOT NULL DEFAULT 1, discontinuity INTEGER NOT NULL, discontinuities INTEGER NOT NULL)",
		"CREATE INDEX IF NOT EXISTS segments_time ON segments(visible,start,end)",
		"CREATE TABLE IF NOT EXISTS snapshots (id TEXT PRIMARY KEY, expires INTEGER NOT NULL, start REAL NOT NULL, end REAL NOT NULL)",
		"CREATE TABLE IF NOT EXISTS snapshot_items (snapshot TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE, position INTEGER NOT NULL, segment INTEGER NOT NULL REFERENCES segments(id), PRIMARY KEY(snapshot,position))",
		"CREATE INDEX IF NOT EXISTS snapshot_segment ON snapshot_items(segment)",
		"CREATE TABLE IF NOT EXISTS pending_delete(file TEXT PRIMARY KEY, bytes INTEGER NOT NULL)",
	}
	for _, q := range schema {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize archive: %w", err)
		}
	}
	if err = s.recover(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	success = true
	return s, nil
}

func (s *Store) check() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

// NextID durably reserves an identifier. Unused reservations are never reused.
func (s *Store) NextID(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return 0, err
	}
	var id int
	// This ceiling preserves the client's signed 32-bit identifier contract.
	err := s.db.QueryRowContext(ctx, "UPDATE state SET value=value+1 WHERE key='next_id' AND value<2147483647 RETURNING value-1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("segment ID space exhausted")
	}
	return id, err
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validate(seg *models.TranscriptSegment) error {
	if seg == nil || seg.SegmentID < 0 || seg.SegmentID >= math.MaxInt32 || !finite(seg.TimelineStartSec) || !finite(seg.TimelineEndSec) || seg.TimelineStartSec <= 0 || seg.TimelineEndSec <= seg.TimelineStartSec || seg.TimelineEndSec-seg.TimelineStartSec > 60 {
		return ErrInvalid
	}
	if seg.TSFile != fmt.Sprintf("%09d.ts", seg.SegmentID) {
		return fmt.Errorf("%w: noncanonical audio filename", ErrInvalid)
	}
	chars := len([]rune(seg.TextZh))
	for _, w := range seg.Words {
		if !finite(w.StartSec) || !finite(w.EndSec) || w.StartSec < seg.TimelineStartSec-0.001 || w.EndSec > seg.TimelineEndSec+0.001 || w.EndSec < w.StartSec || w.CharStart < 0 || w.CharEnd <= w.CharStart || w.CharEnd > chars {
			return fmt.Errorf("%w: inconsistent word timing or character range", ErrInvalid)
		}
	}
	return nil
}

// Publish makes the audio durable before committing metadata. No reader can see
// a database row referring to a file which has not been installed successfully.
func (s *Store) Publish(ctx context.Context, seg *models.TranscriptSegment, source string) error {
	if err := validate(seg); err != nil {
		return err
	}
	if seg.Words == nil {
		seg.Words = []models.WordEntry{}
	}
	seg.HasContent = seg.TextZh != ""
	data, err := json.Marshal(seg)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("%w: metadata exceeds 1 MiB", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return err
	}
	if err = s.writable(ctx); err != nil {
		return err
	}
	var next int
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM state WHERE key='next_id'").Scan(&next); err != nil {
		return err
	}
	if seg.SegmentID >= next {
		return fmt.Errorf("%w: ID must be reserved before publication", ErrInvalid)
	}
	var existing int
	err = s.db.QueryRowContext(ctx, "SELECT id FROM segments WHERE id=?", seg.SegmentID).Scan(&existing)
	if err == nil {
		return fmt.Errorf("%w: segment already published", ErrInvalid)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4<<20 {
		return fmt.Errorf("%w: audio file size/type", ErrInvalid)
	}
	free, _, err := s.freeSpace()
	if err != nil {
		return err
	}
	if free < s.opts.ReserveBytes+uint64(info.Size())+2<<20 {
		return ErrLowDisk
	}
	used, err := s.usage()
	if err != nil {
		return err
	}
	if used+info.Size()+int64(len(data))+(2<<20) > s.opts.MaxBytes {
		return ErrLowDisk
	}
	out, err := os.CreateTemp(filepath.Join(s.dir, "hls"), ".publish-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	copied, copyErr := io.Copy(out, io.LimitReader(in, (4<<20)+1))
	if copyErr == nil && copied != info.Size() {
		copyErr = fmt.Errorf("audio changed during publication")
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	final := filepath.Join(s.dir, "hls", seg.TSFile)
	if err = os.Rename(tmp, final); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(final)
		}
	}()
	if err = syncDir(filepath.Join(s.dir, "hls")); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq, discs int64
	if err = tx.QueryRowContext(ctx, "SELECT value FROM state WHERE key='next_seq'").Scan(&seq); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT value FROM state WHERE key='discontinuities'").Scan(&discs); err != nil {
		return err
	}
	var lastEnd float64
	err = tx.QueryRowContext(ctx, "SELECT end FROM segments ORDER BY seq DESC LIMIT 1").Scan(&lastEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if seg.TimelineStartSec < lastEnd-0.001 {
			return fmt.Errorf("%w: overlapping or reordered audio", ErrInvalid)
		}
		if seg.TimelineStartSec > lastEnd+0.001 {
			seg.Discontinuity = true
		}
	}
	if seg.Discontinuity {
		discs++
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO segments(id,seq,start,end,file,data,bytes,discontinuity,discontinuities) VALUES(?,?,?,?,?,?,?,?,?)", seg.SegmentID, seq, seg.TimelineStartSec, seg.TimelineEndSec, seg.TSFile, data, info.Size(), boolInt(seg.Discontinuity), discs); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE state SET value=value+1 WHERE key='next_seq'"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE state SET value=? WHERE key='discontinuities'", discs); err != nil {
		return err
	}
	if s.beforeCommit != nil {
		if err = s.beforeCommit(); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	s.audioBytes += info.Size()
	for id, ch := range s.watchers {
		select {
		case ch <- *seg:
		default:
			close(ch)
			delete(s.watchers, id)
		}
	}
	return nil
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// Writable rejects work before the free-space reserve or file capacity is used.
func (s *Store) Writable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	return s.writable(ctx)
}
func (s *Store) writable(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	free, inodes, err := s.freeSpace()
	if err != nil {
		return err
	}
	if free < s.opts.ReserveBytes+(2<<20) || inodes < 128 {
		return ErrLowDisk
	}
	used, err := s.usage()
	if err != nil {
		return err
	}
	if used+(2<<20) >= s.opts.MaxBytes {
		return ErrLowDisk
	}
	return nil
}

// Audio bytes are maintained incrementally. Only the bounded staging directories
// and SQLite files need filesystem accounting; the archive is never rescanned.
func (s *Store) usage() (int64, error) {
	total := s.audioBytes
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path != s.dir {
				return nil
			}
			return err
		}
		if path == filepath.Join(s.dir, "hls") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (s *Store) recover(ctx context.Context) error {
	// Snapshot lifetimes survive a process restart; expiration releases pins.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE expires<=?", time.Now().Unix()); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,file,bytes FROM segments")
	if err != nil {
		return err
	}
	type ref struct {
		id    int
		file  string
		bytes int64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.file, &r.bytes); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, r := range refs {
		stat, e := os.Stat(filepath.Join(s.dir, "hls", r.file))
		if e != nil || !stat.Mode().IsRegular() || stat.Size() != r.bytes {
			if e != nil && !os.IsNotExist(e) {
				return e
			}
			// A snapshot containing lost audio cannot honestly promise completeness.
			if _, e = s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id IN (SELECT snapshot FROM snapshot_items WHERE segment=?)", r.id); e != nil {
				return e
			}
			if _, e = s.db.ExecContext(ctx, "DELETE FROM segments WHERE id=?", r.id); e != nil {
				return e
			}
			continue
		}
		expected[r.file] = true
		s.audioBytes += r.bytes
	}
	files, err := os.ReadDir(filepath.Join(s.dir, "hls"))
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if !expected[file.Name()] {
			if err = os.Remove(filepath.Join(s.dir, "hls", file.Name())); err != nil {
				return err
			}
		}
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM pending_delete")
	return err
}

func (s *Store) Read(ctx context.Context, id int) (*models.TranscriptSegment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.read(ctx, id)
}
func (s *Store) read(ctx context.Context, id int) (*models.TranscriptSegment, error) {
	var data []byte
	var disc int
	err := s.db.QueryRowContext(ctx, "SELECT data,discontinuity FROM segments WHERE id=?", id).Scan(&data, &disc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var seg models.TranscriptSegment
	if err = json.Unmarshal(data, &seg); err != nil {
		return nil, err
	}
	seg.Discontinuity = disc != 0
	return &seg, nil
}
func (s *Store) query(ctx context.Context, q string, args ...any) ([]models.TranscriptSegment, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []models.TranscriptSegment{}
	totalBytes := 0
	for rows.Next() {
		var data []byte
		var disc int
		if err = rows.Scan(&data, &disc); err != nil {
			return nil, err
		}
		totalBytes += len(data)
		if totalBytes > 8<<20 {
			return nil, ErrTooLarge
		}
		var seg models.TranscriptSegment
		if err = json.Unmarshal(data, &seg); err != nil {
			return nil, err
		}
		seg.Discontinuity = disc != 0
		result = append(result, seg)
	}
	return result, rows.Err()
}

func (s *Store) Latest(ctx context.Context, n int) ([]models.TranscriptSegment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	if n < 1 || n > 1000 {
		return nil, ErrInvalid
	}
	return s.query(ctx, "SELECT data,discontinuity FROM segments WHERE visible=1 ORDER BY seq DESC LIMIT ?", n)
}
func (s *Store) At(ctx context.Context, sec float64) (*models.TranscriptSegment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	if !finite(sec) {
		return nil, ErrInvalid
	}
	segs, err := s.query(ctx, "SELECT data,discontinuity FROM segments WHERE visible=1 AND start<=? AND end>? ORDER BY seq LIMIT 1", sec, sec)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, ErrNotFound
	}
	return &segs[0], nil
}

func (s *Store) Playlist(ctx context.Context) ([]models.TranscriptSegment, error) {
	entries, err := s.PlaylistView(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]models.TranscriptSegment, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Segment)
	}
	return result, nil
}
func (s *Store) PlaylistView(ctx context.Context) ([]PlaylistEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	cutoff := float64(time.Now().Add(-s.opts.Delay).UnixMilli()) / 1000
	rows, err := s.db.QueryContext(ctx, "SELECT id,start,end,file,seq,discontinuity,discontinuities FROM segments WHERE visible=1 AND end<=? ORDER BY seq", cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []PlaylistEntry{}
	for rows.Next() {
		var e PlaylistEntry
		var disc int
		if err = rows.Scan(&e.Segment.SegmentID, &e.Segment.TimelineStartSec, &e.Segment.TimelineEndSec, &e.Segment.TSFile, &e.Sequence, &disc, &e.DiscontinuitySequence); err != nil {
			return nil, err
		}
		e.Segment.Discontinuity = disc != 0
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SnapshotPage pins immutable segment identities so every page has a stable total.
func (s *Store) SnapshotPage(ctx context.Context, token string, start, end float64, limit, offset int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := Page{Segments: []models.TranscriptSegment{}}
	if e := s.check(); e != nil {
		return page, e
	}
	if !finite(start) || !finite(end) || end <= start || end-start > s.opts.Retention.Seconds()+0.001 || limit < 1 || limit > 1000 || offset < 0 {
		return page, ErrInvalid
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE expires<=?", time.Now().Unix()); err != nil {
		return page, err
	}
	var expires int64
	created := token == ""
	if token == "" {
		if err := s.writable(ctx); err != nil {
			return page, err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots").Scan(&count); err != nil {
			return page, err
		}
		if count >= s.opts.MaxSnapshots {
			return page, ErrBusy
		}
		var key [24]byte
		if _, err := rand.Read(key[:]); err != nil {
			return page, err
		}
		token = hex.EncodeToString(key[:])
		expires = time.Now().Add(s.opts.SnapshotTTL).Unix()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return page, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "INSERT INTO snapshots(id,expires,start,end) VALUES(?,?,?,?)", token, expires, start, end); err != nil {
			return page, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO snapshot_items(snapshot,position,segment) SELECT ?,ROW_NUMBER() OVER (ORDER BY seq)-1,id FROM segments WHERE visible=1 AND start<? AND end>?", token, end, start); err != nil {
			return page, err
		}
		if err = tx.Commit(); err != nil {
			return page, err
		}
	} else {
		var originalStart, originalEnd float64
		err := s.db.QueryRowContext(ctx, "SELECT expires,start,end FROM snapshots WHERE id=?", token).Scan(&expires, &originalStart, &originalEnd)
		if errors.Is(err, sql.ErrNoRows) {
			return page, ErrSnapshotExpired
		}
		if err != nil {
			return page, err
		}
		if start != originalStart || end != originalEnd {
			return page, fmt.Errorf("%w: snapshot range changed", ErrInvalid)
		}
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshot_items WHERE snapshot=?", token).Scan(&page.Total); err != nil {
		return page, err
	}
	result, err := s.query(ctx, "SELECT segments.data,segments.discontinuity FROM snapshot_items JOIN segments ON segment=segments.id WHERE snapshot=? ORDER BY position LIMIT ? OFFSET ?", token, limit, offset)
	if err != nil {
		if created {
			_, _ = s.db.ExecContext(context.Background(), "DELETE FROM snapshots WHERE id=?", token)
		}
		return page, err
	}
	page.Segments = result
	page.Count = len(result)
	page.SnapshotID = token
	page.ExpiresAt = time.Unix(expires, 0).UTC().Format(time.RFC3339)
	return page, nil
}
func (s *Store) ReleaseSnapshot(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	if len(token) != 48 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id=?", token)
	return err
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stats Stats
	if e := s.check(); e != nil {
		return stats, e
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(MIN(start),0),COALESCE(MAX(end),0) FROM segments WHERE visible=1").Scan(&stats.Total, &stats.Oldest, &stats.Newest); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots WHERE expires>?", time.Now().Unix()).Scan(&stats.Snapshots); err != nil {
		return stats, err
	}
	var err error
	stats.Bytes, err = s.usage()
	return stats, err
}

// Subscribe installs the watcher and reads history under the same publication
// lock. A slow subscriber is closed explicitly rather than losing events.
func (s *Store) Subscribe(ctx context.Context, history int) ([]models.TranscriptSegment, <-chan models.TranscriptSegment, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, nil, nil, e
	}
	if history < 0 || history > 100 {
		return nil, nil, nil, ErrInvalid
	}
	segs, err := s.query(ctx, "SELECT data,discontinuity FROM (SELECT * FROM segments WHERE visible=1 ORDER BY seq DESC LIMIT ?) ORDER BY seq", history)
	if err != nil {
		return nil, nil, nil, err
	}
	s.watchID++
	id := s.watchID
	ch := make(chan models.TranscriptSegment, 64)
	s.watchers[id] = ch
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.watchers[id]; ok {
			close(c)
			delete(s.watchers, id)
		}
	}
	return segs, ch, cancel, nil
}

type AudioFile struct {
	*os.File
	store *Store
	id    int
	once  sync.Once
}

func (a *AudioFile) Close() error {
	var err error
	a.once.Do(func() {
		err = a.File.Close()
		a.store.mu.Lock()
		defer a.store.mu.Unlock()
		a.store.open[a.id]--
		if a.store.open[a.id] == 0 {
			delete(a.store.open, a.id)
		}
	})
	return err
}
func (s *Store) OpenAudio(ctx context.Context, id int) (*AudioFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	var file string
	err := s.db.QueryRowContext(ctx, "SELECT file FROM segments WHERE id=?", id).Scan(&file)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dir, "hls", file))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.open[id]++
	return &AudioFile{File: f, store: s, id: id}, nil
}

// Maintain hides expired entries immediately, but keeps the bytes until snapshot
// and active-download pins are released. Under pressure it evicts oldest data.
func (s *Store) Maintain(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM snapshots WHERE expires<=?", time.Now().Unix()); err != nil {
		return err
	}
	if err := s.drainTrash(ctx); err != nil {
		return err
	}
	cutoff := float64(time.Now().Add(-s.opts.Retention).UnixMilli()) / 1000
	if _, err := s.db.ExecContext(ctx, "UPDATE segments SET visible=0 WHERE end<?", cutoff); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,file,bytes,visible,EXISTS(SELECT 1 FROM snapshot_items WHERE segment=segments.id) FROM segments ORDER BY seq")
	if err != nil {
		return err
	}
	type candidate struct {
		id              int
		file            string
		bytes           int64
		visible, pinned int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.file, &c.bytes, &c.visible, &c.pinned); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	used, err := s.usage()
	if err != nil {
		return err
	}
	free, inodes, err := s.freeSpace()
	if err != nil {
		return err
	}
	// Hysteresis avoids repeated stop/start and preserves space for SQLite writes.
	headroom := int64(8 << 20)
	if s.opts.MaxBytes/10 < headroom {
		headroom = s.opts.MaxBytes / 10
	}
	if headroom < 2<<20 {
		headroom = 2 << 20
	}
	pressure := func() bool {
		return used+headroom > s.opts.MaxBytes || free < s.opts.ReserveBytes+uint64(headroom) || inodes < 256
	}
	for _, c := range candidates {
		if c.visible != 0 && !pressure() {
			continue
		}
		if c.visible != 0 {
			if _, err = s.db.ExecContext(ctx, "UPDATE segments SET visible=0 WHERE id=?", c.id); err != nil {
				return err
			}
		}
		if c.pinned != 0 || s.open[c.id] > 0 {
			continue
		}
		// A durable unlink journal makes deletion retries safe after transient
		// filesystem failures or a crash between the row deletion and unlink.
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO pending_delete(file,bytes) VALUES(?,?)", c.file, c.bytes); e != nil {
			tx.Rollback()
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM segments WHERE id=?", c.id); e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if e = s.drainTrash(ctx); e != nil {
			return e
		}
		used -= c.bytes
		// Recheck physical capacity, rather than assuming unlink returned all
		// apparent bytes (sparse files, allocation blocks and open files differ).
		free, inodes, err = s.freeSpace()
		if err != nil {
			return err
		}
	}
	// Reclaim a bounded amount of freed database pages so quota recovery
	// also works when dictionaries, rather than audio, dominated the archive.
	if _, err = s.db.ExecContext(ctx, "PRAGMA incremental_vacuum(256)"); err != nil {
		return err
	}
	// Short reader transactions and a single connection keep the WAL bounded.
	if _, err = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return err
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, ch := range s.watchers {
		close(ch)
		delete(s.watchers, id)
	}
	err := s.db.Close()
	if s.lockFile != nil {
		_ = syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN)
		err = errors.Join(err, s.lockFile.Close())
	}
	return err
}

func ParseFileID(file, suffix string) (int, error) {
	if !strings.HasSuffix(file, suffix) {
		return 0, ErrInvalid
	}
	digits := strings.TrimSuffix(file, suffix)
	if len(digits) < 9 || len(digits) > 10 {
		return 0, ErrInvalid
	}
	id, err := strconv.ParseInt(digits, 10, 32)
	if err != nil || id < 0 || fmt.Sprintf("%09d%s", id, suffix) != file {
		return 0, ErrInvalid
	}
	return int(id), nil
}

// Visible returns completed metadata in chronological publication order.
func (s *Store) Visible(ctx context.Context) ([]models.TranscriptSegment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,start,end,file FROM segments WHERE visible=1 ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []models.TranscriptSegment{}
	for rows.Next() {
		var seg models.TranscriptSegment
		if err = rows.Scan(&seg.SegmentID, &seg.TimelineStartSec, &seg.TimelineEndSec, &seg.TSFile); err != nil {
			return nil, err
		}
		result = append(result, seg)
	}
	return result, rows.Err()
}

// Range performs bounded SQL pagination without pinning any files.
func (s *Store) Range(ctx context.Context, start, end float64, limit, offset int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Page{Segments: []models.TranscriptSegment{}}
	if e := s.check(); e != nil {
		return p, e
	}
	if !finite(start) || !finite(end) || end <= start || end-start > s.opts.Retention.Seconds()+0.001 || limit < 1 || limit > 1000 || offset < 0 {
		return p, ErrInvalid
	}
	if e := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE visible=1 AND start<? AND end>?", end, start).Scan(&p.Total); e != nil {
		return p, e
	}
	var e error
	p.Segments, e = s.query(ctx, "SELECT data,discontinuity FROM segments WHERE visible=1 AND start<? AND end>? ORDER BY seq LIMIT ? OFFSET ?", end, start, limit, offset)
	p.Count = len(p.Segments)
	return p, e
}

func (s *Store) OpenAudioSnapshot(ctx context.Context, id int, token string) (*AudioFile, error) {
	if token == "" {
		return s.OpenAudio(ctx, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return nil, e
	}
	if len(token) != 48 {
		return nil, ErrSnapshotExpired
	}
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT expires FROM snapshots WHERE id=?", token).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) || expires <= time.Now().Unix() {
		return nil, ErrSnapshotExpired
	}
	if err != nil {
		return nil, err
	}
	var file string
	err = s.db.QueryRowContext(ctx, "SELECT file FROM snapshot_items JOIN segments ON segment=segments.id WHERE snapshot=? AND segment=?", token, id).Scan(&file)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dir, "hls", file))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.open[id]++
	return &AudioFile{File: f, store: s, id: id}, nil
}

func (s *Store) drainTrash(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT file,bytes FROM pending_delete")
	if err != nil {
		return err
	}
	type entry struct {
		file  string
		bytes int64
	}
	var pending []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.file, &e.bytes); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range pending {
		if err = os.Remove(filepath.Join(s.dir, "hls", e.file)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if _, err = s.db.ExecContext(ctx, "DELETE FROM pending_delete WHERE file=?", e.file); err != nil {
			return err
		}
		s.audioBytes -= e.bytes
	}
	return nil
}
