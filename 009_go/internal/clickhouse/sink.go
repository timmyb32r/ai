package clickhouse

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	ch "github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/chpool"
	"github.com/ClickHouse/ch-go/proto"
	"github.com/apache/arrow-go/v18/arrow"

	"transferia2-go/internal/config"
)

type Sink struct {
	pool   *chpool.Pool
	schema *arrow.Schema
	inputs sync.Pool
	table  string
	owner  bool
}

func New(ctx context.Context, cfg config.ClickHouseConfig, schema *arrow.Schema, table string) (*Sink, error) {
	address, host, err := parseAddress(cfg.ConnectionString)
	if err != nil {
		return nil, err
	}
	useTLS := true
	if cfg.UseTLS != nil {
		useTLS = *cfg.UseTLS
	}
	var tlsCfg *tls.Config
	if useTLS {
		serverName := cfg.TLSDomain
		if serverName == "" {
			serverName = host
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	}
	p, err := chpool.Dial(ctx, chpool.Options{
		ClientOptions: ch.Options{
			Address:     address,
			Database:    cfg.Database,
			User:        cfg.Username,
			Password:    cfg.Password,
			Compression: ch.CompressionLZ4,
			TLS:         tlsCfg,
			DialTimeout: 10 * time.Second,
			ReadTimeout: ch.NoTimeout,
			ClientName:  "transferia2-go/0.1",
		},
		MaxConns: int32(cfg.MaxConnections),
		MinConns: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("connect ClickHouse %s: %w", address, err)
	}
	s := newTableSink(p, schema, table)
	s.owner = true
	return s, nil
}

func newTableSink(p *chpool.Pool, schema *arrow.Schema, table string) *Sink {
	s := &Sink{pool: p, schema: schema, table: table}
	s.inputs.New = func() any {
		input := make(proto.Input, schema.NumFields())
		for i, f := range schema.Fields() {
			input[i] = proto.InputColumn{Name: f.Name, Data: &arrowInput{nullable: f.Nullable}}
		}
		return input
	}
	return s
}

func (s *Sink) ForTable(schema *arrow.Schema, table string) *Sink {
	return newTableSink(s.pool, schema, table)
}

func (s *Sink) Close() {
	if s.owner {
		s.pool.Close()
	}
}

func (s *Sink) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Sink) EnsureTable(ctx context.Context, cfg config.JSONParserConfig, recreate bool) error {
	qTable := quoteIdent(s.table)
	if recreate {
		if err := s.pool.Do(ctx, ch.Query{Body: "DROP TABLE IF EXISTS " + qTable}); err != nil {
			return err
		}
	}
	defs := make([]string, len(cfg.Columns))
	for i, c := range cfg.Columns {
		var typ string
		switch c.ArrowType {
		case "Utf8", "String":
			typ = "String"
		case "Int32", "int32":
			typ = "Int32"
		default:
			return fmt.Errorf("unsupported ClickHouse mapping for %q", c.ArrowType)
		}
		if c.Nullable {
			typ = "Nullable(" + typ + ")"
		}
		defs[i] = quoteIdent(c.ColumnName) + " " + typ
	}
	order := "tuple()"
	if len(cfg.OrderBy) > 0 {
		parts := make([]string, len(cfg.OrderBy))
		for i, name := range cfg.OrderBy {
			parts[i] = quoteIdent(name)
		}
		order = strings.Join(parts, ",")
	}
	ddl := "CREATE TABLE IF NOT EXISTS " + qTable + " (" + strings.Join(defs, ",") + ") ENGINE = MergeTree ORDER BY (" + order + ")"
	if err := s.pool.Do(ctx, ch.Query{Body: ddl}); err != nil {
		return fmt.Errorf("create table %q: %w", s.table, err)
	}
	return nil
}

func (s *Sink) EnsureDLQTable(ctx context.Context, recreate bool) error {
	qTable := quoteIdent(s.table)
	if recreate {
		if err := s.pool.Do(ctx, ch.Query{Body: "DROP TABLE IF EXISTS " + qTable}); err != nil {
			return err
		}
	}
	if err := s.pool.Do(ctx, ch.Query{Body: dlqCreateDDL(qTable)}); err != nil {
		return fmt.Errorf("create DLQ table %q: %w", s.table, err)
	}
	// CREATE IF NOT EXISTS does not reconcile an older table schema. Add the
	// canonical at-least-once DLQ columns idempotently so an existing benchmark
	// table cannot fail the pipeline on its first invalid row.
	if err := s.pool.Do(ctx, ch.Query{Body: dlqMigrateDDL(qTable)}); err != nil {
		return fmt.Errorf("migrate DLQ table %q: %w", s.table, err)
	}
	return nil
}

func dlqCreateDDL(qTable string) string {
	return "CREATE TABLE IF NOT EXISTS " + qTable + " (" +
		quoteIdent("raw_bytes") + " String," +
		quoteIdent("error_message") + " String," +
		quoteIdent("partition_id") + " Int64," +
		quoteIdent("timestamp") + " String) ENGINE = MergeTree ORDER BY tuple()"
}

func dlqMigrateDDL(qTable string) string {
	return "ALTER TABLE " + qTable +
		" ADD COLUMN IF NOT EXISTS " + quoteIdent("raw_bytes") + " String," +
		" ADD COLUMN IF NOT EXISTS " + quoteIdent("error_message") + " String," +
		" ADD COLUMN IF NOT EXISTS " + quoteIdent("partition_id") + " Int64," +
		" ADD COLUMN IF NOT EXISTS " + quoteIdent("timestamp") + " String"
}

// Write sends all records as blocks of one INSERT query. Arrow owns the
// backing memory until Do returns; adapters only swap array references.
func (s *Sink) Write(ctx context.Context, records []arrow.RecordBatch) error {
	if len(records) == 0 {
		return nil
	}
	input := s.inputs.Get().(proto.Input)
	defer func() {
		for i := range input {
			input[i].Data.(*arrowInput).set(nil)
		}
		s.inputs.Put(input)
	}()
	idx := 0
	setRecord := func(rec arrow.RecordBatch) {
		for i := range input {
			input[i].Data.(*arrowInput).set(rec.Column(i))
		}
	}
	setRecord(records[0])
	err := s.pool.Do(ctx, ch.Query{
		Body:  input.Into(s.table),
		Input: input,
		OnInput: func(context.Context) error {
			idx++
			if idx >= len(records) {
				for i := range input {
					input[i].Data.(*arrowInput).set(nil)
				}
				return io.EOF
			}
			setRecord(records[idx])
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("insert into %q: %w", s.table, err)
	}
	return nil
}

func parseAddress(s string) (address, host string, err error) {
	address = s
	if strings.Contains(s, "://") {
		u, e := url.Parse(s)
		if e != nil {
			return "", "", e
		}
		address = u.Host
	}
	host, _, err = net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("ClickHouse connection_string must be host:port: %w", err)
	}
	return address, host, nil
}

func quoteIdent(s string) string {
	// ClickHouse accepts ANSI double-quoted identifiers. strconv handles every
	// control byte and prevents config-derived SQL injection.
	return strconv.Quote(s)
}
