// Package storage manages metadata files on the filesystem.
// The ingest pipeline writes TranscriptSegment JSON files here;
// the HTTP API reads and watches them.
package storage

import (
	"context"
	"time"

	"github.com/criradio/server/internal/models"
)

// MetadataStore persists and retrieves transcript metadata.
type MetadataStore interface {
	// Write saves a TranscriptSegment to metadata/{segment_id}.json
	// and updates metadata/index.json.
	Write(segment *models.TranscriptSegment) error

	// Read reads a segment by its ID.
	Read(segmentID int) (*models.TranscriptSegment, error)

	// ReadRange reads all segments whose timeline overlaps [startSec, endSec].
	ReadRange(startSec, endSec float64) ([]models.TranscriptSegment, error)

	// ReadIndex reads the current index.json.
	ReadIndex() (*models.SegmentIndex, error)

	// Cleanup removes metadata files older than ttl.
	Cleanup(ttl time.Duration) (deleted int, err error)

	// Watch returns a channel that receives a SegmentRef for each new metadata file.
	// Used by the SSE handler to push new segments to clients.
	Watch(ctx context.Context) (<-chan models.SegmentRef, error)

	// Stats returns current storage statistics.
	Stats() StorageStats

	// Close releases resources (e.g., stops the watcher).
	Close() error
}

// StorageStats provides summary information about the metadata store.
type StorageStats struct {
	TotalFiles int
	OldestID   int
	NewestID   int
}
