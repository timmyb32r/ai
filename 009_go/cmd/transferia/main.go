package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"transferia2-go/internal/clickhouse"
	"transferia2-go/internal/config"
	"transferia2-go/internal/metrics"
	"transferia2-go/internal/parser"
	"transferia2-go/internal/pipeline"
	"transferia2-go/internal/yds"
)

const (
	pqv1WireRevision = "offsets-5-6-7-token-first"
	buildRevision    = "partition-v3-buffer-v2-dlq-v2-metrics-v3"
)

func main() {
	if err := run(); err != nil {
		slog.Error("transfer failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath      = flag.String("config", os.Getenv("CONFIG_PATH"), "path to YAML config")
		totalWorkers    = flag.Uint("total-workers", 1, "number of process workers")
		workerIndex     = flag.Uint("worker-index", 0, "zero-based process worker index")
		pipelineWorkers = flag.Int("pipeline-workers", 0, "decode+parse workers per partition (0=auto)")
		cpuProfile      = flag.String("cpuprofile", "", "write CPU profile")
		heapProfile     = flag.String("memprofile", "", "write heap profile on exit")
	)
	flag.Parse()
	if *configPath == "" {
		return errors.New("--config or CONFIG_PATH is required")
	}
	if *totalWorkers == 0 || *workerIndex >= *totalWorkers {
		return fmt.Errorf("worker-index must be in [0, total-workers)")
	}
	stopCPU, err := startCPUProfile(*cpuProfile)
	if err != nil {
		return err
	}
	defer stopCPU()
	if *heapProfile != "" {
		defer writeHeapProfile(*heapProfile)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	token, err := cfg.Source.PQv1.Auth.AccessToken()
	if err != nil {
		return err
	}
	p, err := parser.New(cfg.Source.PQv1.Parser.JSONParser)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sink, err := clickhouse.New(ctx, *cfg.Sink.ClickHouse, p.Schema(), cfg.TableName())
	if err != nil {
		return err
	}
	defer sink.Close()
	if err := sink.EnsureTable(ctx, cfg.Source.PQv1.Parser.JSONParser, cfg.RecreateTables); err != nil {
		return err
	}
	dlqSink := sink.ForTable(p.DLQSchema(), cfg.TableName()+"_dlq")
	if err := dlqSink.EnsureDLQTable(ctx, cfg.RecreateTables); err != nil {
		return err
	}

	partitions := assignedPartitions(cfg.Source.PQv1.PartitionIDs, uint32(*totalWorkers), uint32(*workerIndex))
	if len(partitions) == 0 {
		slog.Info("no partitions assigned to worker", "worker_index", *workerIndex)
		return nil
	}
	workers := *pipelineWorkers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0) / len(partitions)
		if workers < 1 {
			workers = 1
		}
		if workers > 8 {
			workers = 8
		}
	}
	counters := make(map[int64]*metrics.Counters, len(partitions))
	for _, partition := range partitions {
		counters[partition] = &metrics.Counters{}
	}
	if cfg.Metrics.Enabled {
		metrics.StartReporter(
			ctx.Done(), time.Duration(cfg.Metrics.IntervalMS)*time.Millisecond,
			cfg.Metrics.PerPartition, counters,
		)
	}
	slog.Info("transferia2-go starting",
		"table", cfg.TableName(), "partitions", partitions, "pipeline_workers_per_partition", workers,
		"batch_size", cfg.Sink.ClickHouse.BatchSize, "max_linger_ms", cfg.Sink.ClickHouse.MaxLingerMS,
		"pqv1_wire", pqv1WireRevision, "build_revision", buildRevision,
	)

	g, gctx := errgroup.WithContext(ctx)
	for _, partition := range partitions {
		partition := partition
		g.Go(func() error {
			return runPartition(gctx, *cfg.Source.PQv1, token, partition, p, sink, dlqSink,
				cfg.Sink.ClickHouse.BatchSize,
				time.Duration(cfg.Sink.ClickHouse.MaxLingerMS)*time.Millisecond,
				workers, counters[partition])
		})
	}
	err = g.Wait()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func runPartition(
	ctx context.Context,
	cfg config.PQv1Config,
	token string,
	partition int64,
	p *parser.Parser,
	sink *clickhouse.Sink,
	dlqSink *clickhouse.Sink,
	batchSize int,
	linger time.Duration,
	workers int,
	counters *metrics.Counters,
) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		session, err := yds.Open(ctx, cfg, token, partition, counters)
		if err == nil {
			err = pipeline.Run(ctx, session, p, sink, dlqSink, batchSize, linger, workers, counters)
			_ = session.Close()
		}
		if errors.Is(err, context.Canceled) {
			return err
		}
		lastErr = err
		slog.Error("partition pipeline failed; reconnecting", "partition", partition, "attempt", attempt+1, "error", err)
		delay := time.Duration(1<<attempt) * time.Second
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("partition %d failed after retries: %w", partition, lastErr)
}

func assignedPartitions(ids []int64, total, index uint32) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		u := uint64(id)
		if id < 0 {
			u = uint64(-id)
		}
		if uint32(u%uint64(total)) == index {
			out = append(out, id)
		}
	}
	return out
}

func startCPUProfile(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { pprof.StopCPUProfile(); _ = f.Close() }, nil
}

func writeHeapProfile(path string) {
	f, err := os.Create(path)
	if err != nil {
		slog.Error("create heap profile", "error", err)
		return
	}
	defer f.Close()
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		slog.Error("write heap profile", "error", err)
	}
}
