package metrics

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Counters mirrors the Rust source/parse/sink counter sets. Updates happen once
// per YDS message batch or ClickHouse flush, never once per parsed row.
type Counters struct {
	CompressedBytes     atomic.Uint64
	DecompressedBytes   atomic.Uint64
	Messages            atomic.Uint64
	DownloadBusyNanos   atomic.Uint64
	DecompressBusyNanos atomic.Uint64

	ParsedRows      atomic.Uint64
	ParseArrowBytes atomic.Uint64
	InvalidRows     atomic.Uint64
	ParseMessages   atomic.Uint64
	ParseBusyNanos  atomic.Uint64

	InsertedRows   atomic.Uint64
	SinkArrowBytes atomic.Uint64
	SinkFlushes    atomic.Uint64
	SinkMessages   atomic.Uint64
	SinkBusyNanos  atomic.Uint64
}

type snapshot struct {
	compressed, decompressed, messages                 uint64
	downloadBusy, decompressBusy                       uint64
	parsedRows, parseArrow, invalid, parseMessages     uint64
	parseBusy                                          uint64
	insertedRows, sinkArrow, sinkFlushes, sinkMessages uint64
	sinkBusy                                           uint64
}

func (c *Counters) snapshot() snapshot {
	return snapshot{
		compressed: c.CompressedBytes.Load(), decompressed: c.DecompressedBytes.Load(), messages: c.Messages.Load(),
		downloadBusy: c.DownloadBusyNanos.Load(), decompressBusy: c.DecompressBusyNanos.Load(),
		parsedRows: c.ParsedRows.Load(), parseArrow: c.ParseArrowBytes.Load(), invalid: c.InvalidRows.Load(),
		parseMessages: c.ParseMessages.Load(), parseBusy: c.ParseBusyNanos.Load(),
		insertedRows: c.InsertedRows.Load(), sinkArrow: c.SinkArrowBytes.Load(), sinkFlushes: c.SinkFlushes.Load(),
		sinkMessages: c.SinkMessages.Load(), sinkBusy: c.SinkBusyNanos.Load(),
	}
}

func delta(cur, prev snapshot) snapshot {
	return snapshot{
		compressed: cur.compressed - prev.compressed, decompressed: cur.decompressed - prev.decompressed,
		messages: cur.messages - prev.messages, downloadBusy: cur.downloadBusy - prev.downloadBusy,
		decompressBusy: cur.decompressBusy - prev.decompressBusy,
		parsedRows:     cur.parsedRows - prev.parsedRows, parseArrow: cur.parseArrow - prev.parseArrow,
		invalid: cur.invalid - prev.invalid, parseMessages: cur.parseMessages - prev.parseMessages,
		parseBusy:    cur.parseBusy - prev.parseBusy,
		insertedRows: cur.insertedRows - prev.insertedRows, sinkArrow: cur.sinkArrow - prev.sinkArrow,
		sinkFlushes: cur.sinkFlushes - prev.sinkFlushes, sinkMessages: cur.sinkMessages - prev.sinkMessages,
		sinkBusy: cur.sinkBusy - prev.sinkBusy,
	}
}

func (s *snapshot) add(v snapshot) {
	s.compressed += v.compressed
	s.decompressed += v.decompressed
	s.messages += v.messages
	s.downloadBusy += v.downloadBusy
	s.decompressBusy += v.decompressBusy
	s.parsedRows += v.parsedRows
	s.parseArrow += v.parseArrow
	s.invalid += v.invalid
	s.parseMessages += v.parseMessages
	s.parseBusy += v.parseBusy
	s.insertedRows += v.insertedRows
	s.sinkArrow += v.sinkArrow
	s.sinkFlushes += v.sinkFlushes
	s.sinkMessages += v.sinkMessages
	s.sinkBusy += v.sinkBusy
}

// StartReporter emits the same field order and spelling as Rust metrics so
// 005_rust/scripts/stats_avg.py can parse Go logs unchanged.
func StartReporter(done <-chan struct{}, interval time.Duration, perPartition bool, partitions map[int64]*Counters) {
	go func() {
		if interval <= 0 {
			interval = time.Millisecond
		}
		pids := make([]int64, 0, len(partitions))
		for pid := range partitions {
			pids = append(pids, pid)
		}
		sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		prev := make(map[int64]snapshot, len(partitions))
		proc := newProcessStats()
		var last time.Time
		primed := false
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				cpu, rss := proc.snapshot(now)
				if !primed {
					for _, pid := range pids {
						prev[pid] = partitions[pid].snapshot()
					}
					last = now
					primed = true
					continue
				}
				wall := uint64(now.Sub(last).Nanoseconds())
				last = now
				if wall == 0 {
					continue
				}
				if perPartition {
					for _, pid := range pids {
						cur := partitions[pid].snapshot()
						d := delta(cur, prev[pid])
						prev[pid] = cur
						slog.Info(formatLine(pid, d, wall, cpu, rss))
					}
					continue
				}
				var total snapshot
				for _, pid := range pids {
					cur := partitions[pid].snapshot()
					total.add(delta(cur, prev[pid]))
					prev[pid] = cur
				}
				if len(pids) != 0 {
					slog.Info(formatAggregateLine(total, wall*uint64(len(pids)), cpu, rss))
				}
			}
		}
	}()
}

func formatLine(pid int64, d snapshot, wallNS, cpu, rss uint64) string {
	return fmt.Sprintf("[stats p=%d] %s || %s || %s || cpu: %d%% rss: %s",
		pid, sourcePart(d, wallNS), parsePart(d, wallNS), sinkPart(d, wallNS), cpu, formatRSS(rss))
}

func formatAggregateLine(d snapshot, wallNS, cpu, rss uint64) string {
	return fmt.Sprintf("[stats] %s || %s || %s || cpu: %d%% rss: %s",
		sourcePart(d, wallNS), parsePart(d, wallNS), sinkPart(d, wallNS), cpu, formatRSS(rss))
}

func sourcePart(d snapshot, wallNS uint64) string {
	return fmt.Sprintf("yds: %d msg/s | comp %s | decomp %s | dl %d%% busy | decomp %d%% busy",
		rate(d.messages, wallNS), formatBytes(byteRate(d.compressed, wallNS)),
		formatBytes(byteRate(d.decompressed, wallNS)), percent(d.downloadBusy, wallNS),
		percent(d.decompressBusy, wallNS))
}

func parsePart(d snapshot, wallNS uint64) string {
	return fmt.Sprintf("parse: %d rows/s | %s arrow | %d dlq/s | %s | %d%% busy",
		rate(d.parsedRows, wallNS), formatBytes(byteRate(d.parseArrow, wallNS)), rate(d.invalid, wallNS),
		messageRate(d.parseMessages, wallNS), percent(d.parseBusy, wallNS))
}

func sinkPart(d snapshot, wallNS uint64) string {
	return fmt.Sprintf("sink: %d rows/s | %s arrow | %d flushes/s | %s | %d%% busy",
		rate(d.insertedRows, wallNS), formatBytes(byteRate(d.sinkArrow, wallNS)), rate(d.sinkFlushes, wallNS),
		messageRate(d.sinkMessages, wallNS), percent(d.sinkBusy, wallNS))
}

func messageRate(messages, wallNS uint64) string {
	if messages == 0 {
		return "msg/s: unknown (absent exactly_once_keys)"
	}
	return fmt.Sprintf("~%d msg/s", rate(messages, wallNS))
}

func rate(n, wallNS uint64) uint64 {
	if wallNS == 0 {
		return 0
	}
	return uint64(float64(n) * 1_000_000_000.0 / float64(wallNS))
}

func byteRate(n, wallNS uint64) float64 {
	if wallNS == 0 {
		return 0
	}
	return float64(n) * 1_000_000_000.0 / float64(wallNS)
}

func percent(busy, wallNS uint64) uint64 {
	if wallNS == 0 {
		return 0
	}
	return uint64(float64(busy) * 100.0 / float64(wallNS))
}

func formatBytes(bytesPerSecond float64) string {
	const (
		kib = 1024.0
		mib = 1024.0 * kib
		gib = 1024.0 * mib
	)
	switch {
	case bytesPerSecond >= gib:
		return fmt.Sprintf("%.1f GiB/s", bytesPerSecond/gib)
	case bytesPerSecond >= mib:
		return fmt.Sprintf("%.1f MiB/s", bytesPerSecond/mib)
	case bytesPerSecond >= kib:
		return fmt.Sprintf("%.1f KiB/s", bytesPerSecond/kib)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSecond)
	}
}

type processStats struct {
	prevTicks uint64
	prevWall  time.Time
}

func newProcessStats() processStats {
	return processStats{prevTicks: readProcessTicks(), prevWall: time.Now()}
}

func (p *processStats) snapshot(now time.Time) (cpuPercent, rssBytes uint64) {
	ticks := readProcessTicks()
	wall := now.Sub(p.prevWall).Nanoseconds()
	var deltaTicks uint64
	if ticks >= p.prevTicks {
		deltaTicks = ticks - p.prevTicks
	}
	p.prevTicks = ticks
	p.prevWall = now
	// Linux USER_HZ is 100 on the benchmark hosts. CPU is percent of one core.
	if wall > 0 {
		cpuPercent = uint64(float64(deltaTicks) * 1_000_000_000.0 / float64(wall))
	}
	return cpuPercent, readProcessRSS()
}

func readProcessTicks() uint64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 || end+2 >= len(b) {
		return 0
	}
	fields := strings.Fields(string(b[end+2:]))
	if len(fields) <= 12 {
		return 0
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	return utime + stime
}

func readProcessRSS() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			kib, _ := strconv.ParseUint(fields[1], 10, 64)
			return kib * 1024
		}
	}
	return 0
}

func formatRSS(bytes uint64) string {
	const (
		kib = uint64(1024)
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	case bytes >= mib:
		return fmt.Sprintf("%.0f MiB", float64(bytes)/float64(mib))
	case bytes >= kib:
		return fmt.Sprintf("%.0f KiB", float64(bytes)/float64(kib))
	case bytes > 0:
		return fmt.Sprintf("%d B", bytes)
	default:
		return "N/A"
	}
}
