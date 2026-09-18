package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr, ChannelURL, OutputDir, ASRURL, HanLPURL, AssetsDir, FFmpeg string
	SegmentSeconds, QueueSeconds                                     int
	Delay, Retention, SnapshotTTL, InferenceTimeout, StallTimeout    time.Duration
	MaxBytes                                                         int64
	ReserveBytes                                                     uint64
}

func Load() (Config, error) {
	c := Config{Addr: env("ADDR", ":8080"), ChannelURL: env("CHANNEL_URL", "https://sk.cri.cn/905.m3u8"), OutputDir: env("OUTPUT_DIR", "/data"), ASRURL: env("ASR_URL", "http://asr:8766"), HanLPURL: env("HANLP_URL", "http://hanlp:8765"), AssetsDir: env("ASSETS_DIR", "/assets"), FFmpeg: env("FFMPEG_PATH", "ffmpeg"), SegmentSeconds: 3, QueueSeconds: 300, Delay: 180 * time.Second, Retention: 3 * time.Hour, SnapshotTTL: 10 * time.Minute, InferenceTimeout: 60 * time.Second, StallTimeout: 30 * time.Second, MaxBytes: 10 << 30, ReserveBytes: 10 << 30}
	for key, p := range map[string]*time.Duration{"DELAY": &c.Delay, "RETENTION": &c.Retention, "SNAPSHOT_TTL": &c.SnapshotTTL, "INFERENCE_TIMEOUT": &c.InferenceTimeout, "STALL_TIMEOUT": &c.StallTimeout} {
		if raw := os.Getenv(key); raw != "" {
			v, e := time.ParseDuration(raw)
			if e != nil {
				return c, fmt.Errorf("%s: %w", key, e)
			}
			*p = v
		}
	}
	for key, p := range map[string]*int{"HLS_TIME": &c.SegmentSeconds, "QUEUE_SECONDS": &c.QueueSeconds} {
		if raw := os.Getenv(key); raw != "" {
			v, e := strconv.Atoi(raw)
			if e != nil {
				return c, fmt.Errorf("%s: %w", key, e)
			}
			*p = v
		}
	}
	if raw := os.Getenv("MAX_DATA_BYTES"); raw != "" {
		v, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return c, e
		}
		c.MaxBytes = v
	}
	if raw := os.Getenv("MIN_FREE_BYTES"); raw != "" {
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil {
			return c, e
		}
		c.ReserveBytes = v
	}
	if c.SegmentSeconds < 1 || c.SegmentSeconds > 10 || c.QueueSeconds < 2*c.SegmentSeconds || c.QueueSeconds > 300 || c.Delay < 0 || c.Delay >= c.Retention || c.Retention > 24*time.Hour || c.SnapshotTTL <= 0 || c.SnapshotTTL > time.Hour || c.InferenceTimeout <= 0 || c.InferenceTimeout > 5*time.Minute || c.StallTimeout <= 0 || c.MaxBytes < 1<<20 {
		return c, fmt.Errorf("invalid resource or time bounds")
	}
	for name, raw := range map[string]string{"ASR_URL": c.ASRURL, "HANLP_URL": c.HanLPURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("invalid %s", name)
		}
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
