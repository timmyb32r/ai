package clickhouse

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestDLQMigrationAddsCanonicalColumns(t *testing.T) {
	ddl := dlqMigrateDDL(quoteIdent("logs_dlq"))
	for _, column := range []string{"raw_bytes", "error_message", "partition_id", "timestamp"} {
		if !strings.Contains(ddl, "ADD COLUMN IF NOT EXISTS "+quoteIdent(column)) {
			t.Errorf("migration does not add %q: %s", column, ddl)
		}
	}
}

func TestArrowStringInputEncoding(t *testing.T) {
	b := array.NewStringBuilder(memory.NewGoAllocator())
	b.Append("hello")
	b.AppendNull()
	b.Append("мир")
	a := b.NewStringArray()
	b.Release()
	defer a.Release()
	in := &arrowInput{arr: a, nullable: true}
	var got proto.Buffer
	in.EncodeColumn(&got)
	want := []byte{0, 1, 0, 5}
	want = append(want, "hello"...)
	want = append(want, 0, 6)
	want = append(want, "мир"...)
	if !bytes.Equal(got.Buf, want) {
		t.Fatalf("encoded=%v want=%v", got.Buf, want)
	}
}

func BenchmarkArrowStringInputEncode(b *testing.B) {
	builder := array.NewStringBuilder(memory.NewGoAllocator())
	builder.Reserve(10_000)
	for range 10_000 {
		builder.Append("representative-log-value")
	}
	a := builder.NewStringArray()
	builder.Release()
	defer a.Release()
	in := &arrowInput{arr: a}
	buf := proto.Buffer{Buf: make([]byte, 0, 300_000)}
	b.ReportAllocs()
	b.SetBytes(int64(len(a.ValueBytes())))
	for range b.N {
		buf.Reset()
		in.EncodeColumn(&buf)
	}
}
