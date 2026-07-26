package config

import (
	"os"
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	// Ensure env is clean
	os.Unsetenv("CHANNEL_URL")
	os.Unsetenv("OUTPUT_DIR")
	os.Unsetenv("MODEL_PATH")
	os.Unsetenv("CEDICT_PATH")
	os.Unsetenv("GSE_DICT_PATH")
	os.Unsetenv("DICT")
	os.Unsetenv("BKRS_PATH")
	os.Unsetenv("ASR_BATCH_SIZE")

	cfg := FromEnv()

	if cfg.ChannelURL != "https://sk.cri.cn/905.m3u8" {
		t.Errorf("default ChannelURL mismatch: %s", cfg.ChannelURL)
	}
	if cfg.HLSTime != 3 {
		t.Errorf("default HLSTime: got %d, want 3", cfg.HLSTime)
	}
	if cfg.HLSWindow != 3600 {
		t.Errorf("default HLSWindow: got %d, want 3600", cfg.HLSWindow)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("default Addr: got %s, want :8080", cfg.Addr)
	}
	if cfg.Dict != "bkrs" {
		t.Errorf("default Dict: got %s, want bkrs", cfg.Dict)
	}
	if cfg.BKRSPath != "/opt/dabkrs.gz" {
		t.Errorf("default BKRSPath: got %s, want /opt/dabkrs.gz", cfg.BKRSPath)
	}
	if cfg.ASRBatchSize != 2 {
		t.Errorf("default ASRBatchSize: got %d, want 2", cfg.ASRBatchSize)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *Config
		errMsg string
	}{
		{
			name: "valid config — bkrs",
			cfg: &Config{
				ChannelURL: "https://example.com/radio.m3u8",
				OutputDir:  "/tmp/test",
				ModelPath:  "/opt/model.bin",
				Dict:       "bkrs",
				BKRSPath:   "/opt/dabkrs.gz",
				GSEDictDir: "/opt/gse",
				HLSTime:    3,
				HLSWindow:  3600,
				Delay:      180 * time.Second,
				Addr:       ":8080",
				LogLevel:   "info",
				ASRBatchSize: 2,
			},
		},
		{
			name: "valid config — cedict",
			cfg: &Config{
				ChannelURL: "https://example.com/radio.m3u8",
				OutputDir:  "/tmp/test",
				ModelPath:  "/opt/model.bin",
				Dict:       "cedict",
				DictPath:   "/opt/cedict_ts.u8",
				GSEDictDir: "/opt/gse",
				HLSTime:    3,
				HLSWindow:  3600,
				Delay:      180 * time.Second,
				Addr:       ":8080",
				LogLevel:   "info",
				ASRBatchSize: 2,
			},
		},
		{
			name:   "empty ChannelURL",
			cfg:    &Config{ChannelURL: "", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "CHANNEL_URL",
		},
		{
			name:   "invalid HLSTime",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 0, HLSWindow: 100, LogLevel: "info"},
			errMsg: "HLS_TIME",
		},
		{
			name:   "invalid LogLevel",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 3, HLSWindow: 100, LogLevel: "verbose"},
			errMsg: "LOG_LEVEL",
		},
		{
			name:   "invalid DICT value",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "invalid", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "DICT",
		},
		{
			name:   "bkrs without BKRS_PATH",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "BKRS_PATH",
		},
		{
			name:   "cedict without CEDICT_PATH",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "cedict", DictPath: "", GSEDictDir: "/g", ASRBatchSize: 2, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "CEDICT_PATH",
		},
		{
			name:   "ASRBatchSize 0 (too small)",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 0, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "ASR_BATCH_SIZE",
		},
		{
			name:   "ASRBatchSize 9 (too large)",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 9, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "ASR_BATCH_SIZE",
		},
		{
			name:   "ASRBatchSize 1 (valid min)",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 1, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
		},
		{
			name:   "ASRBatchSize 8 (valid max)",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", Dict: "bkrs", BKRSPath: "/b", GSEDictDir: "/g", ASRBatchSize: 8, HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.errMsg == "" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.errMsg != "" && err == nil {
				t.Errorf("expected error containing %q, got nil", tt.errMsg)
			}
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	os.Setenv("HLS_TIME", "5")
	os.Setenv("LOG_LEVEL", "debug")
	defer os.Unsetenv("HLS_TIME")
	defer os.Unsetenv("LOG_LEVEL")

	cfg := FromEnv()
	if cfg.HLSTime != 5 {
		t.Errorf("HLSTime from env: got %d, want 5", cfg.HLSTime)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel from env: got %s, want debug", cfg.LogLevel)
	}
}

func TestASRBatchSizeFromEnv(t *testing.T) {
	os.Setenv("ASR_BATCH_SIZE", "5")
	defer os.Unsetenv("ASR_BATCH_SIZE")

	cfg := FromEnv()
	if cfg.ASRBatchSize != 5 {
		t.Errorf("ASRBatchSize from env: got %d, want 5", cfg.ASRBatchSize)
	}
}

func TestDictEnvVar(t *testing.T) {
	os.Setenv("DICT", "cedict")
	defer os.Unsetenv("DICT")

	cfg := FromEnv()
	if cfg.Dict != "cedict" {
		t.Errorf("Dict from env: got %s, want cedict", cfg.Dict)
	}
}
