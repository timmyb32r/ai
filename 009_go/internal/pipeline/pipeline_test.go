package pipeline

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/klauspost/compress/zstd"

	"transferia2-go/internal/metrics"
	"transferia2-go/internal/pqproto"
	"transferia2-go/internal/yds"
)

func TestArrayDataBytesHandlesTypedNilDictionary(t *testing.T) {
	builder := array.NewStringBuilder(memory.NewGoAllocator())
	builder.Append("hello")
	values := builder.NewStringArray()
	builder.Release()
	defer values.Release()

	if got := arrayDataBytes(values.Data()); got == 0 {
		t.Fatal("string Arrow array unexpectedly has zero buffer bytes")
	}
}

func TestDecompressCodecs(t *testing.T) {
	w := worker{metrics: &metrics.Counters{}}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	w.zstd = dec
	want := bytes.Repeat([]byte("hello-лог-"), 100)

	var gz bytes.Buffer
	gzw := gzip.NewWriter(&gz)
	_, _ = gzw.Write(want)
	_ = gzw.Close()
	got, err := w.decompress(&yds.RawMessage{Data: gz.Bytes(), Codec: pqproto.Codec_CODEC_GZIP, UncompressedSize: uint64(len(want))}, nil)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("gzip: len=%d err=%v", len(got), err)
	}

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	zipped := enc.EncodeAll(want, nil)
	enc.Close()
	got, err = w.decompress(&yds.RawMessage{Data: zipped, Codec: pqproto.Codec_CODEC_ZSTD, UncompressedSize: uint64(len(want))}, got[:0])
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("zstd: len=%d err=%v", len(got), err)
	}

	got, err = w.decompress(&yds.RawMessage{Data: want, Codec: pqproto.Codec_CODEC_RAW}, nil)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("raw: len=%d err=%v", len(got), err)
	}
}

func TestResizeBuffersUsesSpareCapacity(t *testing.T) {
	buffers := make([][]byte, 12_690, 16_384)
	buffers[0] = []byte("reused")

	buffers = resizeBuffers(buffers, 13_000)
	if len(buffers) != 13_000 {
		t.Fatalf("len = %d, want 13000", len(buffers))
	}
	if string(buffers[0]) != "reused" {
		t.Fatal("existing decompression buffer was not preserved")
	}

	buffers = resizeBuffers(buffers, 20_000)
	if len(buffers) != 20_000 {
		t.Fatalf("grown len = %d, want 20000", len(buffers))
	}
}
