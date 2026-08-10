package pipeline

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/klauspost/compress/zstd"

	"transferia2-go/internal/clickhouse"
	"transferia2-go/internal/metrics"
	"transferia2-go/internal/parser"
	"transferia2-go/internal/pqproto"
	"transferia2-go/internal/yds"
)

const channelSize = 32

type job struct {
	seq   uint64
	batch yds.RawBatch
}

type processed struct {
	seq          uint64
	record       arrow.RecordBatch
	dlq          arrow.RecordBatch
	cookie       *pqproto.CommitCookie
	invalidRows  int
	messageCount int
	err          error
}

type worker struct {
	parser    *parser.Parser
	ws        *parser.Workspace
	zstd      *zstd.Decoder
	gzip      gzip.Reader
	buffers   [][]byte
	messages  []parser.Message
	metrics   *metrics.Counters
	partition int64
}

func Run(
	ctx context.Context,
	session *yds.Session,
	p *parser.Parser,
	sink *clickhouse.Sink,
	dlqSink *clickhouse.Sink,
	batchSize int,
	maxLinger time.Duration,
	workerCount int,
	counters *metrics.Counters,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if workerCount < 1 {
		workerCount = 1
	}
	jobs := make(chan job, channelSize)
	results := make(chan processed, channelSize)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			decompressor, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if err != nil {
				results <- processed{err: err}
				workers.Done()
				return
			}
			w := worker{parser: p, ws: p.NewWorkspace(), zstd: decompressor, metrics: counters, partition: session.Partition()}
			defer func() { w.ws.Release(); w.zstd.Close(); workers.Done() }()
			for j := range jobs {
				out := w.process(j)
				select {
				case results <- out:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		var seq uint64
		defer close(jobs)
		for {
			select {
			case b, ok := <-session.Batches():
				if !ok {
					return
				}
				select {
				case jobs <- job{seq: seq, batch: b}:
					seq++
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	err := consumeOrdered(ctx, session, sink, dlqSink, results, batchSize, maxLinger, counters)
	cancel()
	for r := range results {
		releaseProcessed(r)
	}
	return err
}

func releaseProcessed(r processed) {
	if r.record != nil {
		r.record.Release()
	}
	if r.dlq != nil {
		r.dlq.Release()
	}
}

func (w *worker) process(j job) processed {
	n := len(j.batch.Messages)
	if cap(w.messages) < n {
		w.messages = make([]parser.Message, n)
	} else {
		w.messages = w.messages[:n]
	}
	w.buffers = resizeBuffers(w.buffers, n)
	decompressStart := time.Now()
	var decompressedBytes uint64
	for i := range j.batch.Messages {
		m := &j.batch.Messages[i]
		data, err := w.decompress(m, w.buffers[i])
		if err != nil {
			w.metrics.DecompressBusyNanos.Add(uint64(time.Since(decompressStart)))
			w.metrics.DecompressedBytes.Add(decompressedBytes)
			return processed{seq: j.seq, err: fmt.Errorf("decompress offset %d codec %s: %w", m.Offset, m.Codec, err)}
		}
		if m.Codec != pqproto.Codec_CODEC_RAW {
			w.buffers[i] = data[:0]
		}
		decompressedBytes += uint64(len(data))
		w.messages[i] = parser.Message{Data: data, Offset: m.Offset}
	}
	w.metrics.DecompressBusyNanos.Add(uint64(time.Since(decompressStart)))
	w.metrics.DecompressedBytes.Add(decompressedBytes)
	parseStart := time.Now()
	r, err := w.parser.Parse(w.messages, w.partition, w.ws)
	w.metrics.ParseBusyNanos.Add(uint64(time.Since(parseStart)))
	if err != nil {
		return processed{seq: j.seq, err: err}
	}
	w.metrics.ParsedRows.Add(uint64(r.Record.NumRows()))
	w.metrics.ParseArrowBytes.Add(recordBytes(r.Record))
	w.metrics.InvalidRows.Add(uint64(r.InvalidRows))
	w.metrics.ParseMessages.Add(uint64(n))
	return processed{
		seq: j.seq, record: r.Record, dlq: r.DLQ, cookie: j.batch.Cookie,
		invalidRows: r.InvalidRows, messageCount: n,
	}
}

// resizeBuffers preserves decompression buffers across batches. The slice may
// have spare capacity after append growth, so its length must always be reset
// before indexing it with every message in the current batch.
func resizeBuffers(buffers [][]byte, n int) [][]byte {
	if cap(buffers) >= n {
		return buffers[:n]
	}
	return slices.Grow(buffers, n-len(buffers))[:n]
}

func recordBatchesBytes(records []arrow.RecordBatch) uint64 {
	var total uint64
	for _, record := range records {
		total += recordBytes(record)
	}
	return total
}

// recordBytes is allocation-free for the flat Arrow schemas supported by this
// build. It mirrors Rust's per-column get_array_memory_size metric by summing
// the buffers retained by the record batch.
func recordBytes(record arrow.RecordBatch) uint64 {
	if record == nil {
		return 0
	}
	var total uint64
	for _, column := range record.Columns() {
		total += arrayDataBytes(column.Data())
	}
	return total
}

func arrayDataBytes(data arrow.ArrayData) uint64 {
	if data == nil {
		return 0
	}
	// Arrow returns a typed nil *array.Data through the ArrayData interface for
	// arrays without a dictionary. Such an interface itself compares non-nil.
	if concrete, ok := data.(*array.Data); ok && concrete == nil {
		return 0
	}
	var total uint64
	for _, buffer := range data.Buffers() {
		if buffer != nil {
			total += uint64(buffer.Len())
		}
	}
	for _, child := range data.Children() {
		total += arrayDataBytes(child)
	}
	if dictionary := data.Dictionary(); dictionary != nil {
		total += arrayDataBytes(dictionary)
	}
	return total
}

func (w *worker) decompress(m *yds.RawMessage, dst []byte) ([]byte, error) {
	switch m.Codec {
	case pqproto.Codec_CODEC_RAW:
		return m.Data, nil
	case pqproto.Codec_CODEC_GZIP:
		if err := w.gzip.Reset(bytes.NewReader(m.Data)); err != nil {
			return nil, err
		}
		defer w.gzip.Close()
		return readKnownSize(&w.gzip, dst, m.UncompressedSize)
	case pqproto.Codec_CODEC_ZSTD:
		if int(m.UncompressedSize) <= cap(dst) {
			dst = dst[:0]
		} else {
			dst = make([]byte, 0, int(m.UncompressedSize))
		}
		return w.zstd.DecodeAll(m.Data, dst)
	default:
		return nil, fmt.Errorf("unsupported codec %d", m.Codec)
	}
}

func readKnownSize(r io.Reader, dst []byte, size uint64) ([]byte, error) {
	if size == 0 {
		buf := bytes.NewBuffer(dst[:0])
		if _, err := buf.ReadFrom(r); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	n := int(size)
	if n < 0 {
		return nil, errors.New("uncompressed size overflow")
	}
	if cap(dst) < n {
		dst = make([]byte, n)
	} else {
		dst = dst[:n]
	}
	read, err := io.ReadFull(r, dst)
	if err != nil {
		return nil, err
	}
	var extra [1]byte
	if more, err := r.Read(extra[:]); more != 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return nil, errors.New("decompressed size differs from header")
	}
	return dst[:read], nil
}

func consumeOrdered(
	ctx context.Context,
	session *yds.Session,
	sink *clickhouse.Sink,
	dlqSink *clickhouse.Sink,
	results <-chan processed,
	batchSize int,
	maxLinger time.Duration,
	counters *metrics.Counters,
) error {
	pending := make(map[uint64]processed, channelSize)
	next := uint64(0)
	records := make([]arrow.RecordBatch, 0, 16)
	dlqRecords := make([]arrow.RecordBatch, 0, 4)
	cookies := make([]*pqproto.CommitCookie, 0, 16)
	rows := 0
	messages := 0
	timer := time.NewTimer(maxLinger)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	releaseAccumulated := func() {
		for _, rec := range records {
			rec.Release()
		}
		for _, rec := range dlqRecords {
			rec.Release()
		}
		records = records[:0]
		dlqRecords = dlqRecords[:0]
		cookies = cookies[:0]
		rows = 0
		messages = 0
	}
	defer func() {
		releaseAccumulated()
		for _, r := range pending {
			releaseProcessed(r)
		}
	}()
	flush := func() error {
		if len(records) == 0 && len(dlqRecords) == 0 {
			return nil
		}
		arrowBytes := recordBatchesBytes(records) + recordBatchesBytes(dlqRecords)
		writeStart := time.Now()
		if err := sink.Write(ctx, records); err != nil {
			releaseAccumulated()
			return err
		}
		if err := dlqSink.Write(ctx, dlqRecords); err != nil {
			releaseAccumulated()
			return err
		}
		counters.SinkBusyNanos.Add(uint64(time.Since(writeStart)))
		counters.InsertedRows.Add(uint64(rows))
		counters.SinkArrowBytes.Add(arrowBytes)
		counters.SinkFlushes.Add(1)
		counters.SinkMessages.Add(uint64(messages))
		if err := session.CommitMany(cookies); err != nil {
			releaseAccumulated()
			return fmt.Errorf("commit PQv1 cookies after ClickHouse insert: %w", err)
		}
		releaseAccumulated()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return nil
	}
	appendReady := func(r processed) error {
		if r.err != nil {
			return r.err
		}
		validRows := int(r.record.NumRows())
		wasEmpty := len(records) == 0 && len(dlqRecords) == 0
		if r.invalidRows != 0 {
			slog.Warn("invalid JSON rows routed to DLQ", "count", r.invalidRows, "sequence", r.seq)
			dlqRecords = append(dlqRecords, r.dlq)
		}
		if validRows == 0 {
			r.record.Release()
		} else {
			records = append(records, r.record)
		}
		if r.cookie != nil {
			cookies = append(cookies, r.cookie)
		}
		rows += validRows + r.invalidRows
		messages += r.messageCount
		if wasEmpty && (len(records) != 0 || len(dlqRecords) != 0) {
			timer.Reset(maxLinger)
		}
		if rows >= batchSize {
			return flush()
		}
		return nil
	}
	for {
		select {
		case r, ok := <-results:
			if !ok {
				for {
					r, exists := pending[next]
					if !exists {
						break
					}
					delete(pending, next)
					next++
					if err := appendReady(r); err != nil {
						return err
					}
				}
				return flush()
			}
			pending[r.seq] = r
			for {
				r, exists := pending[next]
				if !exists {
					break
				}
				delete(pending, next)
				next++
				if err := appendReady(r); err != nil {
					return err
				}
			}
		case <-timer.C:
			if err := flush(); err != nil {
				return err
			}
		case err := <-session.Errors():
			if err != nil {
				return err
			}
		case <-ctx.Done():
			// Flush already parsed rows before shutdown, then commit them.
			if err := flush(); err != nil {
				return err
			}
			return ctx.Err()
		}
	}
}
