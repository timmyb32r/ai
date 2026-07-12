package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all configuration for the service.
type Config struct {
	Addr           string // HTTP listen address (default ":8080")
	DeployMode     string // "local" or "ycloud"
	Engine         string // ASR engine (default "sherpa-onnx")
	ModelPath      string // Path to directory containing model files (e.g., /opt/models)
	ModelCodename  string // Model codename (e.g., "sense-voice-2024")
	SherpaOnnxPath string // Path to sherpa-onnx-offline binary
	YtDlpPath      string // Path to yt-dlp (default "yt-dlp")
	FFmpegPath     string // Path to ffmpeg (default "ffmpeg")
	ChunkDuration  int    // Seconds per chunk (default 30)
	MaxParallel    int    // Max parallel ASR workers (default 4)
	TempDir        string // Temp directory for downloads and chunks

	// Yandex Object Storage (ycloud mode only)
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
}

// FromEnv reads configuration from environment variables.
func FromEnv() *Config {
	return &Config{
		Addr:           envStr("ADDR", ":8080"),
		DeployMode:     envStr("DEPLOY_MODE", "local"),
		Engine:         envStr("ENGINE", "sherpa-onnx"),
		ModelPath:      envStr("MODEL_PATH", "/opt/models"),
		ModelCodename:  envStr("ASR_MODEL", "sense-voice-2024"),
		SherpaOnnxPath: envStr("SHERPA_ONNX_PATH", "sherpa-onnx-offline"),
		YtDlpPath:      envStr("YT_DLP_PATH", "yt-dlp"),
		FFmpegPath:     envStr("FFMPEG_PATH", "ffmpeg"),
		ChunkDuration:  envInt("CHUNK_DURATION", 30),
		MaxParallel:    envInt("MAX_PARALLEL", 4),
		TempDir:        envStr("TEMP_DIR", os.TempDir()),

		S3Endpoint:  envStr("S3_ENDPOINT", ""),
		S3Bucket:    envStr("S3_BUCKET", ""),
		S3AccessKey: envStr("S3_ACCESS_KEY", ""),
		S3SecretKey: envStr("S3_SECRET_KEY", ""),
	}
}

// Validate checks that required configuration values are present.
func (c *Config) Validate() error {
	if c.ModelPath == "" {
		return fmt.Errorf("MODEL_PATH is required")
	}
	if c.ModelCodename == "" {
		return fmt.Errorf("ASR_MODEL is required")
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
