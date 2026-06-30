// Package config provides server configuration from environment variables with sensible defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all server configuration.
type Config struct {
	ChannelURL      string        // HLS radio stream URL
	OutputDir       string        // Output directory for HLS segments and metadata
	ModelPath       string        // Path to ASR model file/directory
	AsrEngine       string        // ASR engine: "whisper" or "sherpa-onnx"
	AsrModel        string        // Model codename from registry (e.g. "ggml-small", "sense-voice-2024")
	DictPath        string        // Path to CC-CEDICT dictionary (cedict_ts.u8)
	GSEDictDir      string        // Path to gse dictionary directory
	HLSTime         int           // Seconds per HLS segment (default: 3)
	HLSWindow       int           // Number of HLS segments to keep (default: 3600 = 3 hours)
	Delay           time.Duration // Processing delay from live edge (default: 180s)
	Addr            string        // HTTP listen address (default: :8080)
	LogLevel        string        // Log level: debug, info, warn, error (default: info)
	HTTPHeaders     string        // HTTP headers for ffmpeg (e.g. "Referer: ...\r\nUser-Agent: ...")
	FFmpegExtraArgs []string      // Extra ffmpeg arguments inserted before -i (space-separated in env)
}

// FromEnv reads configuration from environment variables with defaults.
func FromEnv() *Config {
	return &Config{
		ChannelURL:      envStr("CHANNEL_URL", "https://sk.cri.cn/905.m3u8"),
		OutputDir:       envStr("OUTPUT_DIR", "/tmp/china_radio_international"),
		ModelPath:       envStr("MODEL_PATH", "/opt/models/ggml-large-v3.bin"),
		AsrEngine:       envStr("ASR_ENGINE", "whisper"),
		AsrModel:        envStr("ASR_MODEL", "ggml-large"),
		DictPath:        envStr("CEDICT_PATH", "/opt/cedict_ts.u8"),
		GSEDictDir:      envStr("GSE_DICT_PATH", "/opt/gse-dict"),
		HLSTime:         envInt("HLS_TIME", 3),
		HLSWindow:       envInt("HLS_WINDOW", 3600),
		Delay:           envDuration("DELAY", 180*time.Second),
		Addr:            envStr("ADDR", ":8080"),
		LogLevel:        envStr("LOG_LEVEL", "info"),
		HTTPHeaders:     envStr("HTTP_HEADERS", ""),
		FFmpegExtraArgs: envStrSlice("FFMPEG_EXTRA_ARGS", nil),
	}
}

// Validate checks that all required configuration values are present and valid.
func (c *Config) Validate() error {
	if c.ChannelURL == "" {
		return fmt.Errorf("CHANNEL_URL is required")
	}
	if c.OutputDir == "" {
		return fmt.Errorf("OUTPUT_DIR is required")
	}
	if c.ModelPath == "" {
		return fmt.Errorf("MODEL_PATH is required")
	}
	if c.AsrEngine != "" && c.AsrEngine != "whisper" && c.AsrEngine != "sherpa-onnx" {
		return fmt.Errorf("ASR_ENGINE must be 'whisper' or 'sherpa-onnx', got %q", c.AsrEngine)
	}
	if c.DictPath == "" {
		return fmt.Errorf("CEDICT_PATH is required")
	}
	if c.GSEDictDir == "" {
		return fmt.Errorf("GSE_DICT_PATH is required")
	}
	if c.HLSTime < 1 || c.HLSTime > 10 {
		return fmt.Errorf("HLS_TIME must be between 1 and 10 seconds, got %d", c.HLSTime)
	}
	if c.HLSWindow < 1 {
		return fmt.Errorf("HLS_WINDOW must be positive, got %d", c.HLSWindow)
	}
	if c.Delay < 0 {
		return fmt.Errorf("DELAY must be non-negative, got %s", c.Delay)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of: debug, info, warn, error, got %s", c.LogLevel)
	}
	return nil
}

func envStr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func envDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// envStrSlice reads a space-separated or comma-separated environment variable
// and returns a slice of non-empty strings. Returns defaultVal if env is empty.
func envStrSlice(key string, defaultVal []string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultVal
	}
	// Support both space and comma as separator.
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ','
	})
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}
