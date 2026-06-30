// Package ingest captures the live HLS radio stream via ffmpeg.
package ingest

import (
	"context"

	"github.com/criradio/server/internal/models"
)

// Ingestor captures the radio stream and produces HLS segments + PCM chunks.
type Ingestor interface {
	// Start begins capturing. HLS segments are written to outputDir/hls/.
	// PCM chunks are sent to the returned channel.
	Start(ctx context.Context) (<-chan models.PCMChunk, error)
	// Stop gracefully stops ffmpeg.
	Stop() error
	// Stats returns current ingest statistics.
	Stats() Stats
}

// Stats provides ingest statistics.
type Stats struct {
	SegmentsIngested int64
	BytesWritten     int64
	Running          int64 // 0 = not running, 1 = running
}

// Config holds ingest configuration.
type Config struct {
	ChannelURL      string   // HLS radio stream URL
	OutputDir       string   // root output directory
	HLSTime         int      // seconds per HLS segment
	HLSWindow       int      // number of segments in DVR window
	FFmpegExtraArgs []string // extra arguments passed to ffmpeg before -i
	HTTPHeaders     string   // HTTP headers for ffmpeg (e.g. "Referer: ...\r\nUser-Agent: ...")
}
