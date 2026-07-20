package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/criradio/server/internal/models"
)

const (
	metadataDir = "metadata"
	indexFile   = "index.json"
)

// fsStore implements MetadataStore using the local filesystem.
type fsStore struct {
	outputDir string // root output directory (~/tmp/china_radio_international)
	metaDir   string // metadata subdirectory
	indexPath string // metadata/index.json path

	mu       sync.RWMutex
	watchers []chan models.SegmentRef

	// Throttle index.json writes: only flush every N segments to avoid
	// rewriting a multi-MB file on every 3-second segment.
	idxWriteInterval int
	idxWritesSince   int
}

// New creates a new filesystem-backed MetadataStore.
func New(outputDir string) (MetadataStore, error) {
	metaDir := filepath.Join(outputDir, metadataDir)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, err
	}

	return &fsStore{
		outputDir: outputDir,
		metaDir:   metaDir,
		indexPath: filepath.Join(metaDir, indexFile),
		watchers:  make([]chan models.SegmentRef, 0),
		// Write index.json every 100 segments instead of every segment.
		// At 3s/segment this is ~every 5 minutes, reducing disk writes
		// by 99% while keeping the index fresh enough for API consumers.
		idxWriteInterval: 100,
	}, nil
}

func (s *fsStore) Write(segment *models.TranscriptSegment) error {
	jsonFile := segmentFileName(segment.SegmentID)

	// Write the segment JSON file
	data, err := json.MarshalIndent(segment, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.metaDir, jsonFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	// Update index
	ref := models.SegmentRef{
		ID:               segment.SegmentID,
		TimelineStartSec: segment.TimelineStartSec,
		TimelineEndSec:   segment.TimelineEndSec,
		TSFile:           segment.TSFile,
		JSONFile:         jsonFile,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.updateIndex(ref); err != nil {
		return err
	}

	// Notify watchers
	for _, ch := range s.watchers {
		select {
		case ch <- ref:
		default:
			// Drop if watcher buffer is full (non-blocking)
		}
	}

	return nil
}

func (s *fsStore) Read(segmentID int) (*models.TranscriptSegment, error) {
	path := filepath.Join(s.metaDir, segmentFileName(segmentID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var seg models.TranscriptSegment
	if err := json.Unmarshal(data, &seg); err != nil {
		return nil, err
	}
	seg.HasContent = seg.TextZh != ""
	return &seg, nil
}

func (s *fsStore) ReadRange(startSec, endSec float64) ([]models.TranscriptSegment, error) {
	idx, err := s.ReadIndex()
	if err != nil {
		return nil, err
	}

	var segments []models.TranscriptSegment
	for _, ref := range idx.Segments {
		// Check overlap: segment overlaps with [startSec, endSec]
		if ref.TimelineStartSec < endSec && ref.TimelineEndSec > startSec {
			seg, err := s.Read(ref.ID)
			if err != nil {
				continue // skip unreadable segments
			}
			segments = append(segments, *seg)
		}
	}
	return segments, nil
}

// ReadLatest reads the N most recent segments by timeline (newest first).
// Used by the cold-start bulk endpoint to return everything the
// client needs in a single HTTP request.
//
// Uses timeline_start_sec (Unix epoch) rather than segment_id so that
// pipeline restarts (which reset segment_id to 0) don't return stale
// pre-restart metadata instead of fresh data.
func (s *fsStore) ReadLatest(n int) ([]models.TranscriptSegment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, err := s.readIndexLocked()
	if err != nil {
		return nil, err
	}
	if len(idx.Segments) == 0 {
		return nil, nil
	}

	// Sort a copy by timeline_start_sec descending so we always return
	// the most recent audio segments regardless of segment_id numbering.
	sorted := append([]models.SegmentRef(nil), idx.Segments...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TimelineStartSec > sorted[j].TimelineStartSec
	})

	end := n
	if end > len(sorted) {
		end = len(sorted)
	}

	result := make([]models.TranscriptSegment, 0, end)
	for _, ref := range sorted[:end] {
		seg, err := s.Read(ref.ID)
		if err != nil {
			continue
		}
		result = append(result, *seg)
	}
	return result, nil
}

func (s *fsStore) ReadIndex() (*models.SegmentIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If we have unflushed writes, the on-disk index.json is stale.
	// Rebuild it from individual segment files (always current).
	if s.idxWritesSince > 0 {
		s.rebuildIndex()
		s.idxWritesSince = 0
	}

	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &models.SegmentIndex{}, nil
		}
		return nil, err
	}

	var idx models.SegmentIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

func (s *fsStore) Cleanup(ttl time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-ttl)
	deleted := 0

	entries, err := os.ReadDir(s.metaDir)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == indexFile {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(s.metaDir, entry.Name())
			if err := os.Remove(path); err == nil {
				deleted++
			}
		}
	}

	// Rebuild index after cleanup
	if deleted > 0 {
		s.rebuildIndex()
	}

	return deleted, nil
}

func (s *fsStore) Watch(ctx context.Context) (<-chan models.SegmentRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan models.SegmentRef, 64) // buffered for burst
	s.watchers = append(s.watchers, ch)

	// Remove channel on context done
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, w := range s.watchers {
			if w == ch {
				s.watchers = append(s.watchers[:i], s.watchers[i+1:]...)
				close(ch)
				break
			}
		}
	}()

	return ch, nil
}

func (s *fsStore) Stats() StorageStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, _ := os.ReadDir(s.metaDir)
	var ids []int
	fileCount := 0
	for _, e := range entries {
		if e.IsDir() || e.Name() == indexFile {
			continue
		}
		fileCount++
		if id := parseSegmentID(e.Name()); id >= 0 {
			ids = append(ids, id)
		}
	}

	sort.Ints(ids)
	stats := StorageStats{TotalFiles: fileCount}
	if len(ids) > 0 {
		stats.OldestID = ids[0]
		stats.NewestID = ids[len(ids)-1]
	}
	return stats
}

func (s *fsStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.watchers {
		close(ch)
	}
	s.watchers = nil
	return nil
}

// updateIndex reads the current index, adds/updates the given ref, and writes it back.
// Must be called with s.mu held (write lock).
//
// To avoid O(n²) disk I/O (rewriting a multi-MB file every 3 seconds),
// the index is only flushed to disk every [idxWriteInterval] segments.
// On crash, missing entries are recovered by rebuildIndex().
func (s *fsStore) updateIndex(ref models.SegmentRef) error {
	idx, err := s.readIndexLocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if idx == nil {
		idx = &models.SegmentIndex{}
	}

	// Update or append
	found := false
	for i, existing := range idx.Segments {
		if existing.ID == ref.ID {
			idx.Segments[i] = ref
			found = true
			break
		}
	}
	if !found {
		idx.Segments = append(idx.Segments, ref)
	}

	// Sort by ID
	sort.Slice(idx.Segments, func(i, j int) bool {
		return idx.Segments[i].ID < idx.Segments[j].ID
	})

	idx.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// Throttle: only write to disk every idxWriteInterval segments.
	s.idxWritesSince++
	if s.idxWritesSince < s.idxWriteInterval {
		return nil
	}
	s.idxWritesSince = 0

	return s.writeIndexLocked(idx)
}

// ForceFlush writes pending index state to disk immediately.
func (s *fsStore) ForceFlush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.idxWritesSince == 0 {
		return // nothing to flush
	}
	s.idxWritesSince = 0
	s.rebuildIndex()
}

// writeIndexLocked marshals and writes the index to disk.
// Must be called with s.mu held.
func (s *fsStore) writeIndexLocked(idx *models.SegmentIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath, data, 0o644)
}

// readIndexLocked reads index without additional locking.
func (s *fsStore) readIndexLocked() (*models.SegmentIndex, error) {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		return nil, err
	}
	var idx models.SegmentIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// rebuildIndex rebuilds the index from existing JSON files.
func (s *fsStore) rebuildIndex() {
	entries, _ := os.ReadDir(s.metaDir)
	var refs []models.SegmentRef
	for _, e := range entries {
		if e.IsDir() || e.Name() == indexFile {
			continue
		}
		seg, err := s.readSegmentFile(filepath.Join(s.metaDir, e.Name()))
		if err != nil {
			continue
		}
		refs = append(refs, models.SegmentRef{
			ID:               seg.SegmentID,
			TimelineStartSec: seg.TimelineStartSec,
			TimelineEndSec:   seg.TimelineEndSec,
			TSFile:           seg.TSFile,
			JSONFile:         e.Name(),
		})
	}

	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })

	idx := models.SegmentIndex{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Segments:  refs,
	}
	data, _ := json.MarshalIndent(idx, "", "  ")
	os.WriteFile(s.indexPath, data, 0o644)
}

// StartCleanupLoop periodically removes segment JSON files older than ttl
// and rebuilds the index. This bounds disk usage to ~ttl worth of segments.
//
// Typical values: ttl = 6*time.Hour (twice the DVR window), interval = 5*time.Minute.
func (s *fsStore) StartCleanupLoop(ctx context.Context, ttl, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				deleted, err := s.Cleanup(ttl)
				if err != nil {
					continue
				}
				if deleted > 0 {
					s.mu.Lock()
					// Flush any pending index updates before rebuild
					s.idxWritesSince = 0
					s.rebuildIndex()
					s.mu.Unlock()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *fsStore) readSegmentFile(path string) (*models.TranscriptSegment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var seg models.TranscriptSegment
	if err := json.Unmarshal(data, &seg); err != nil {
		return nil, err
	}
	return &seg, nil
}

func segmentFileName(segmentID int) string {
	return segmentIDToStr(segmentID) + ".json"
}

func segmentIDToStr(id int) string {
	// Zero-padded to 9 digits for sortability
	s := "000000000" + itoa(id)
	return s[len(s)-9:]
}

func parseSegmentID(name string) int {
	// name is like "000000001.json"
	id := 0
	digitCount := 0
	for i := 0; i < len(name) && name[i] >= '0' && name[i] <= '9'; i++ {
		id = id*10 + int(name[i]-'0')
		digitCount++
	}
	if digitCount == 0 {
		return -1
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
