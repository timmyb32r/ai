package clickhouse

import (
	"fmt"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// arrowInput exposes an Arrow array directly as a ClickHouse Native input
// column. No []interface{}, row materialization, or second column buffer is
// created. The only unavoidable copy is into the driver's network buffer.
type arrowInput struct {
	arr      arrow.Array
	nullable bool
}

func (c *arrowInput) set(a arrow.Array) { c.arr = a }

func (c *arrowInput) Rows() int {
	if c.arr == nil {
		return 0
	}
	return c.arr.Len()
}

func (c *arrowInput) Type() proto.ColumnType {
	var base proto.ColumnType
	switch c.arr.(type) {
	case nil, *array.String:
		base = proto.ColumnTypeString
	case *array.Int32:
		base = proto.ColumnTypeInt32
	case *array.Int64:
		base = proto.ColumnTypeInt64
	default:
		panic(fmt.Sprintf("unsupported Arrow array %T", c.arr))
	}
	if c.nullable {
		return proto.ColumnTypeNullable.Sub(base)
	}
	return base
}

func (c *arrowInput) EncodeColumn(b *proto.Buffer) {
	if c.nullable {
		c.encodeNulls(b)
	}
	switch a := c.arr.(type) {
	case *array.String:
		encodeStrings(b, a)
	case *array.Int32:
		proto.ColInt32(a.Int32Values()).EncodeColumn(b)
	case *array.Int64:
		proto.ColInt64(a.Int64Values()).EncodeColumn(b)
	}
}

func (c *arrowInput) WriteColumn(w *proto.Writer) {
	if c.nullable {
		w.ChainBuffer(c.encodeNulls)
	}
	switch a := c.arr.(type) {
	case *array.String:
		writeStrings(w, a)
	case *array.Int32:
		proto.ColInt32(a.Int32Values()).WriteColumn(w)
	case *array.Int64:
		proto.ColInt64(a.Int64Values()).WriteColumn(w)
	}
}

func (c *arrowInput) encodeNulls(b *proto.Buffer) {
	a := c.arr
	bitmap := a.NullBitmapBytes()
	if len(bitmap) == 0 {
		b.Buf = append(b.Buf, make([]byte, a.Len())...)
		return
	}
	offset := a.Data().Offset()
	for i := 0; i < a.Len(); i++ {
		valid := bitmap[(offset+i)>>3]&(1<<((offset+i)&7)) != 0
		if valid {
			b.Buf = append(b.Buf, 0)
		} else {
			b.Buf = append(b.Buf, 1)
		}
	}
}

func encodeStrings(b *proto.Buffer, a *array.String) {
	off := a.ValueOffsets()
	data := a.ValueBytes()
	base := off[0]
	for i := 0; i < a.Len(); i++ {
		start := off[i] - base
		end := off[i+1] - base
		b.PutUVarInt(uint64(end - start))
		b.PutRaw(data[start:end])
	}
}

func writeStrings(w *proto.Writer, a *array.String) {
	// One chained buffer avoids one net.Buffers entry per small log field.
	w.ChainBuffer(func(b *proto.Buffer) { encodeStrings(b, a) })
}
