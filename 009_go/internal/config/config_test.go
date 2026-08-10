package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustBenchmarkYAMLCompatibility(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "config_bench_yds_json_parser_to_ch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	s = strings.Replace(s, "connection_string: \"\"", "connection_string: \"grpc://localhost:2135/Root\"", 1)
	s = strings.Replace(s, "connection_string: \"\"", "connection_string: \"localhost:9000\"", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TableName() != "logs" || len(cfg.Source.PQv1.Parser.JSONParser.Columns) != 12 {
		t.Fatalf("unexpected config: table=%q columns=%d", cfg.TableName(), len(cfg.Source.PQv1.Parser.JSONParser.Columns))
	}
}
