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
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *Config
		errMsg string
	}{
		{
			name: "valid config",
			cfg: &Config{
				ChannelURL: "https://example.com/radio.m3u8",
				OutputDir:  "/tmp/test",
				ModelPath:  "/opt/model.bin",
				DictPath:   "/opt/dict.u8",
				GSEDictDir: "/opt/gse",
				HLSTime:    3,
				HLSWindow:  3600,
				Delay:      180 * time.Second,
				Addr:       ":8080",
				LogLevel:   "info",
			},
		},
		{
			name:   "empty ChannelURL",
			cfg:    &Config{ChannelURL: "", OutputDir: "/tmp", ModelPath: "/m", DictPath: "/d", GSEDictDir: "/g", HLSTime: 3, HLSWindow: 100, LogLevel: "info"},
			errMsg: "CHANNEL_URL",
		},
		{
			name:   "invalid HLSTime",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", DictPath: "/d", GSEDictDir: "/g", HLSTime: 0, HLSWindow: 100, LogLevel: "info"},
			errMsg: "HLS_TIME",
		},
		{
			name:   "invalid LogLevel",
			cfg:    &Config{ChannelURL: "x", OutputDir: "/tmp", ModelPath: "/m", DictPath: "/d", GSEDictDir: "/g", HLSTime: 3, HLSWindow: 100, LogLevel: "verbose"},
			errMsg: "LOG_LEVEL",
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
	// Need to set required fields since FromEnv reads them with defaults.
	// We're testing that env vars override defaults.
	if cfg.HLSTime != 5 {
		t.Errorf("HLSTime from env: got %d, want 5", cfg.HLSTime)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel from env: got %s, want debug", cfg.LogLevel)
	}
}
