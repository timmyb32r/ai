package config

import "testing"

func TestInvalidLimitsFailClosed(t *testing.T) {
	for _, tt := range []struct{ k, v string }{{"QUEUE_SECONDS", "301"}, {"HLS_TIME", "0"}, {"RETENTION", "1m"}, {"DELAY", "-1s"}, {"INFERENCE_TIMEOUT", "garbage"}, {"ASR_URL", "file:///tmp/model"}, {"MIN_FREE_BYTES", "-1"}} {
		t.Run(tt.k, func(t *testing.T) {
			t.Setenv(tt.k, tt.v)
			if _, e := Load(); e == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
}
