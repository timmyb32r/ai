package parser

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"

	"transferia2-go/internal/config"
)

func testParser(t *testing.T) *Parser {
	t.Helper()
	p, err := New(config.JSONParserConfig{ChunkSplitter: "new-line", Columns: []config.ColumnConfig{
		{JSONPath: "$.id", ColumnName: "id", ArrowType: "Utf8"},
		{JSONPath: "$.job_index", ColumnName: "job_index", ArrowType: "Int32", Nullable: true},
		{JSONPath: "$.msg", ColumnName: "msg", ArrowType: "Utf8", Nullable: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseJSONLines(t *testing.T) {
	p := testParser(t)
	w := p.NewWorkspace()
	defer w.Release()
	r, err := p.Parse([]Message{{Data: []byte("{\"id\":\"a\",\"job_index\":42,\"msg\":\"hi\\nмир\",\"ignored\":{\"x\":[1,2]}}\n{\"id\":\"b\"}\n")}}, 0, w)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Record.Release()
	if r.InputRows != 2 || r.InvalidRows != 0 || r.Record.NumRows() != 2 {
		t.Fatalf("unexpected result: %+v", r)
	}
	ids := r.Record.Column(0).(*array.String)
	if ids.Value(0) != "a" || ids.Value(1) != "b" {
		t.Fatalf("ids: %q %q", ids.Value(0), ids.Value(1))
	}
	jobs := r.Record.Column(1).(*array.Int32)
	if jobs.Value(0) != 42 || !jobs.IsNull(1) {
		t.Fatalf("job_index: %v null=%v", jobs.Value(0), jobs.IsNull(1))
	}
	if got := r.Record.Column(2).(*array.String).Value(0); got != "hi\nмир" {
		t.Fatalf("msg=%q", got)
	}
}

func TestInvalidRowsAreAtomic(t *testing.T) {
	p := testParser(t)
	w := p.NewWorkspace()
	defer w.Release()
	r, err := p.Parse([]Message{{Data: []byte("{\"job_index\":1}\n{\"id\":\"ok\",\"job_index\":2147483648}\n{\"id\":\"bad\",\"x\":[invalid]}\n{\"id\":\"trailing\",}\n{\"id\":\"yes\",\"job_index\":2}\n")}}, 7, w)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Record.Release()
	defer r.DLQ.Release()
	if r.InvalidRows != 4 || r.Record.NumRows() != 1 {
		t.Fatalf("invalid=%d rows=%d", r.InvalidRows, r.Record.NumRows())
	}
	if got := r.Record.Column(0).(*array.String).Value(0); got != "yes" {
		t.Fatalf("id=%q", got)
	}
	if r.DLQ.NumRows() != 4 || r.DLQ.Column(2).(*array.Int64).Value(0) != 7 {
		t.Fatalf("bad DLQ")
	}
}

func BenchmarkParseBenchmarkShape(b *testing.B) {
	cfg := config.JSONParserConfig{ChunkSplitter: "new-line"}
	for _, name := range []string{"id", "ts", "task_id", "level", "msg", "caller", "error", "runtime", "host", "src_type", "dst_type"} {
		cfg.Columns = append(cfg.Columns, config.ColumnConfig{JSONPath: "$." + name, ColumnName: name, ArrowType: "Utf8", Nullable: name != "id" && name != "ts"})
	}
	cfg.Columns = append(cfg.Columns, config.ColumnConfig{JSONPath: "$.job_index", ColumnName: "job_index", ArrowType: "Int32", Nullable: true})
	p, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	w := p.NewWorkspace()
	defer w.Release()
	line := []byte("{\"id\":\"01JXYZ\",\"ts\":\"2026-08-10T12:34:56Z\",\"task_id\":\"t1\",\"level\":\"info\",\"msg\":\"hello world\",\"caller\":\"worker.go:42\",\"runtime\":\"go\",\"host\":\"sas-1\",\"job_index\":7,\"src_type\":\"yds\",\"dst_type\":\"ch\"}\n")
	payload := make([]byte, 0, len(line)*1000)
	for range 1000 {
		payload = append(payload, line...)
	}
	messages := []Message{{Data: payload}}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		r, err := p.Parse(messages, 0, w)
		if err != nil {
			b.Fatal(err)
		}
		r.Record.Release()
	}
}
