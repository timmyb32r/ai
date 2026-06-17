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

func (s *fsStore) ReadIndex() (*models.SegmentIndex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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
