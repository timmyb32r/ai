package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Source         SourceConfig  `yaml:"source"`
	Sink           SinkConfig    `yaml:"sink"`
	Middlewares    []any         `yaml:"middlewares"`
	Metrics        MetricsConfig `yaml:"metrics"`
	RecreateTables bool          `yaml:"recreate_tables"`
	SinkBatchSize  int           `yaml:"sink_batch_size"`
}

type SourceConfig struct {
	PQv1 *PQv1Config `yaml:"pqv1"`
}

type SinkConfig struct {
	ClickHouse *ClickHouseConfig `yaml:"clickhouse"`
}

type PQv1Config struct {
	ConnectionString  string       `yaml:"connection_string"`
	DiscoveryEndpoint string       `yaml:"discovery_endpoint"`
	TopicPath         string       `yaml:"topic_path"`
	ConsumerName      string       `yaml:"consumer_name"`
	PartitionIDs      []int64      `yaml:"partition_ids"`
	Auth              AuthConfig   `yaml:"auth"`
	Parser            ParserConfig `yaml:"parser"`
}

type AuthConfig struct {
	Type      string `yaml:"type"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

type ParserConfig struct {
	TableNaming TableNaming      `yaml:"table_naming"`
	JSONParser  JSONParserConfig `yaml:"json_parser"`
}

type TableNaming struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
}

type JSONParserConfig struct {
	ChunkSplitter string         `yaml:"chunk_splitter"`
	OrderBy       []string       `yaml:"order_by"`
	Columns       []ColumnConfig `yaml:"columns"`
}

type ColumnConfig struct {
	JSONPath   string `yaml:"jsonpath"`
	ColumnName string `yaml:"column_name"`
	ArrowType  string `yaml:"arrow_type"`
	Nullable   bool   `yaml:"nullable"`
}

type ClickHouseConfig struct {
	ConnectionString string `yaml:"connection_string"`
	Database         string `yaml:"database"`
	BatchSize        int    `yaml:"batch_size"`
	MaxLingerMS      int    `yaml:"max_linger_ms"`
	MaxConnections   int    `yaml:"max_connections"`
	Username         string `yaml:"username"`
	Password         string `yaml:"password"`
	UseTLS           *bool  `yaml:"use_tls"`
	TLSDomain        string `yaml:"tls_domain"`
}

type MetricsConfig struct {
	Enabled      bool `yaml:"enabled"`
	IntervalMS   int  `yaml:"interval_ms"`
	PerPartition bool `yaml:"per_partition"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	expanded := os.ExpandEnv(string(raw))
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) defaults() {
	if c.SinkBatchSize <= 0 {
		c.SinkBatchSize = 10_000
	}
	if c.Sink.ClickHouse != nil {
		ch := c.Sink.ClickHouse
		if ch.Database == "" {
			ch.Database = "default"
		}
		if ch.Username == "" {
			ch.Username = "default"
		}
		if ch.BatchSize <= 0 {
			ch.BatchSize = c.SinkBatchSize
		}
		if ch.MaxLingerMS <= 0 {
			ch.MaxLingerMS = 500
		}
		if ch.MaxConnections <= 0 {
			ch.MaxConnections = 4
		}
	}
	if c.Metrics.IntervalMS <= 0 {
		c.Metrics.IntervalMS = 1000
	}
}

func (c *Config) Validate() error {
	if c.Source.PQv1 == nil {
		return errors.New("source.pqv1 is required; this build implements only pqv1 -> clickhouse")
	}
	if c.Sink.ClickHouse == nil {
		return errors.New("sink.clickhouse is required; this build implements only pqv1 -> clickhouse")
	}
	pq := c.Source.PQv1
	if pq.ConnectionString == "" && pq.DiscoveryEndpoint == "" {
		return errors.New("source.pqv1.connection_string or discovery_endpoint is required")
	}
	if pq.TopicPath == "" || pq.ConsumerName == "" {
		return errors.New("source.pqv1.topic_path and consumer_name are required")
	}
	if len(pq.PartitionIDs) == 0 {
		return errors.New("source.pqv1.partition_ids must contain at least one partition")
	}
	for _, partition := range pq.PartitionIDs {
		if partition < 0 {
			return fmt.Errorf("source.pqv1.partition_ids contains negative partition %d", partition)
		}
		if partition == math.MaxInt64 {
			return errors.New("source.pqv1.partition_ids contains a partition too large for PQv1 group id")
		}
	}
	if pq.Parser.TableNaming.Type != "from_config" && pq.Parser.TableNaming.Type != "from_topic" {
		return errors.New("parser.table_naming.type must be from_config or from_topic")
	}
	if pq.Parser.TableNaming.Type == "from_config" && pq.Parser.TableNaming.Name == "" {
		return errors.New("parser.table_naming.name is required for from_config")
	}
	if pq.Parser.JSONParser.ChunkSplitter != "new-line" {
		return errors.New("only parser.json_parser.chunk_splitter: new-line is supported")
	}
	if len(pq.Parser.JSONParser.Columns) == 0 {
		return errors.New("parser.json_parser.columns must not be empty")
	}
	for i, col := range pq.Parser.JSONParser.Columns {
		if !strings.HasPrefix(col.JSONPath, "$.") || strings.ContainsAny(col.JSONPath[2:], ".[\\]") {
			return fmt.Errorf("columns[%d].jsonpath %q: only root fields like $.id are supported", i, col.JSONPath)
		}
		if col.ColumnName == "" {
			return fmt.Errorf("columns[%d].column_name is required", i)
		}
	}
	if c.Sink.ClickHouse.ConnectionString == "" {
		return errors.New("sink.clickhouse.connection_string is required")
	}
	return nil
}

func (c *Config) TableName() string {
	if c.Source.PQv1.Parser.TableNaming.Type == "from_topic" {
		return c.Source.PQv1.TopicPath
	}
	return c.Source.PQv1.Parser.TableNaming.Name
}

func (a AuthConfig) AccessToken() (string, error) {
	if a.Type != "access_token" {
		return "", fmt.Errorf("pqv1 requires auth.type: access_token, got %q", a.Type)
	}
	if a.Token != "" {
		return strings.TrimSpace(a.Token), nil
	}
	if a.TokenFile == "" {
		return "", errors.New("auth.token or auth.token_file is required")
	}
	p := os.ExpandEnv(a.TokenFile)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read token_file %q: %w", p, err)
	}
	return strings.TrimSpace(string(b)), nil
}
