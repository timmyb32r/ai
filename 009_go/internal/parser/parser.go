package parser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"transferia2-go/internal/config"
)

type Message struct {
	Data   []byte
	Offset uint64
}

type Result struct {
	Record      arrow.RecordBatch
	DLQ         arrow.RecordBatch
	InputRows   int
	InvalidRows int
}

type valueKind uint8

const (
	valueMissing valueKind = iota
	valueString
	valueNumber
	valueNull
	valueOther
)

type token struct {
	kind    valueKind
	raw     []byte
	escaped bool
	i64     int64
}

type columnKind uint8

const (
	kindString columnKind = iota
	kindInt32
)

type column struct {
	key      []byte
	name     string
	kind     columnKind
	nullable bool
}

type Parser struct {
	columns   []column
	schema    *arrow.Schema
	dlqSchema *arrow.Schema
	mem       memory.Allocator
}

type Workspace struct {
	builder    *array.RecordBuilder
	dlqBuilder *array.RecordBuilder
	tokens     []token
	scratch    []byte
}

func New(cfg config.JSONParserConfig) (*Parser, error) {
	cols := make([]column, len(cfg.Columns))
	fields := make([]arrow.Field, len(cfg.Columns))
	for i, c := range cfg.Columns {
		var k columnKind
		var dt arrow.DataType
		switch c.ArrowType {
		case "Utf8", "String":
			k, dt = kindString, arrow.BinaryTypes.String
		case "Int32", "int32":
			k, dt = kindInt32, arrow.PrimitiveTypes.Int32
		default:
			return nil, fmt.Errorf("column %q: unsupported arrow_type %q in optimized build (supported: Utf8, Int32)", c.ColumnName, c.ArrowType)
		}
		cols[i] = column{
			key:      []byte(c.JSONPath[2:]),
			name:     c.ColumnName,
			kind:     k,
			nullable: c.Nullable,
		}
		fields[i] = arrow.Field{Name: c.ColumnName, Type: dt, Nullable: c.Nullable}
	}
	dlqSchema := arrow.NewSchema([]arrow.Field{
		{Name: "raw_bytes", Type: arrow.BinaryTypes.String},
		{Name: "error_message", Type: arrow.BinaryTypes.String},
		{Name: "partition_id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "timestamp", Type: arrow.BinaryTypes.String},
	}, nil)
	return &Parser{columns: cols, schema: arrow.NewSchema(fields, nil), dlqSchema: dlqSchema, mem: memory.NewGoAllocator()}, nil
}

func (p *Parser) Schema() *arrow.Schema { return p.schema }

func (p *Parser) DLQSchema() *arrow.Schema { return p.dlqSchema }

func (p *Parser) NewWorkspace() *Workspace {
	return &Workspace{
		builder: array.NewRecordBuilder(p.mem, p.schema),
		tokens:  make([]token, len(p.columns)),
		scratch: make([]byte, 0, 256),
	}
}

func (w *Workspace) Release() {
	if w.builder != nil {
		w.builder.Release()
		w.builder = nil
	}
	if w.dlqBuilder != nil {
		w.dlqBuilder.Release()
		w.dlqBuilder = nil
	}
}

func (p *Parser) Parse(messages []Message, partitionID int64, w *Workspace) (Result, error) {
	rows := 0
	for i := range messages {
		rows += countNonEmptyLines(messages[i].Data)
	}
	w.builder.Reserve(rows)
	valid := 0
	invalid := 0
	for i := range messages {
		data := messages[i].Data
		for len(data) > 0 {
			line, rest, ok := nextLine(data)
			data = rest
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				if !ok {
					break
				}
				continue
			}
			if err := p.scanAndValidate(line, w); err != nil {
				invalid++
				p.appendDLQ(w, line, err, partitionID)
				if !ok {
					break
				}
				continue
			}
			p.appendRow(w)
			valid++
			if !ok {
				break
			}
		}
	}
	rec := w.builder.NewRecordBatch()
	var dlq arrow.RecordBatch
	if invalid > 0 {
		dlq = w.dlqBuilder.NewRecordBatch()
	}
	return Result{Record: rec, DLQ: dlq, InputRows: valid + invalid, InvalidRows: invalid}, nil
}

func (p *Parser) appendDLQ(w *Workspace, raw []byte, parseErr error, partitionID int64) {
	if w.dlqBuilder == nil {
		w.dlqBuilder = array.NewRecordBuilder(p.mem, p.dlqSchema)
	}
	rawString := bytesString(raw)
	if !utf8.Valid(raw) {
		rawString = strings.ToValidUTF8(rawString, "�")
	}
	w.dlqBuilder.Field(0).(*array.StringBuilder).Append(rawString)
	w.dlqBuilder.Field(1).(*array.StringBuilder).Append(parseErr.Error())
	w.dlqBuilder.Field(2).(*array.Int64Builder).Append(partitionID)
	w.dlqBuilder.Field(3).(*array.StringBuilder).Append(time.Now().UTC().Format(time.RFC3339Nano))
}

func countNonEmptyLines(b []byte) int {
	n := 0
	for len(b) > 0 {
		line, rest, ok := nextLine(b)
		b = rest
		if len(bytes.TrimSpace(line)) != 0 {
			n++
		}
		if !ok {
			break
		}
	}
	return n
}

func nextLine(b []byte) (line, rest []byte, hadNewline bool) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:], true
	}
	return b, nil, false
}

func (p *Parser) scanAndValidate(line []byte, w *Workspace) error {
	clear(w.tokens)
	if err := scanObject(line, p.columns, w.tokens); err != nil {
		return err
	}
	for i := range p.columns {
		c := &p.columns[i]
		t := &w.tokens[i]
		if t.kind == valueMissing || t.kind == valueNull {
			if !c.nullable {
				return errors.New("required field is missing or null")
			}
			continue
		}
		switch c.kind {
		case kindString:
			if t.kind != valueString {
				return errors.New("string field has non-string value")
			}
			if t.escaped {
				w.scratch = w.scratch[:0]
				var err error
				w.scratch, err = appendUnescaped(w.scratch, t.raw)
				if err != nil {
					return err
				}
			} else if !utf8.Valid(t.raw) {
				return errors.New("invalid UTF-8 in JSON string")
			}
		case kindInt32:
			if t.kind != valueNumber {
				return errors.New("Int32 field has non-number value")
			}
			v, ok := parseInt32(t.raw)
			if !ok {
				return errors.New("Int32 field is not an in-range JSON integer")
			}
			t.i64 = int64(v)
		}
	}
	return nil
}

func (p *Parser) appendRow(w *Workspace) {
	for i := range p.columns {
		c := &p.columns[i]
		t := &w.tokens[i]
		if t.kind == valueMissing || t.kind == valueNull {
			w.builder.Field(i).AppendNull()
			continue
		}
		switch c.kind {
		case kindString:
			b := w.builder.Field(i).(*array.StringBuilder)
			if t.escaped {
				w.scratch = w.scratch[:0]
				w.scratch, _ = appendUnescaped(w.scratch, t.raw)
				b.Append(bytesString(w.scratch))
			} else {
				b.Append(bytesString(t.raw))
			}
		case kindInt32:
			w.builder.Field(i).(*array.Int32Builder).Append(int32(t.i64))
		}
	}
}

// bytesString is an allocation-free, read-only view. Arrow copies the bytes
// before the input buffer can be reused.
func bytesString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func scanObject(b []byte, cols []column, out []token) error {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return errors.New("JSON root is not an object")
	}
	i++
	for {
		i = skipSpace(b, i)
		if i >= len(b) {
			return errors.New("unterminated JSON object")
		}
		if b[i] == '}' {
			i = skipSpace(b, i+1)
			if i != len(b) {
				return errors.New("trailing bytes after JSON object")
			}
			return nil
		}
		if b[i] != '"' {
			return errors.New("object key is not a string")
		}
		key, escaped, next, err := scanString(b, i)
		if err != nil {
			return err
		}
		i = skipSpace(b, next)
		if i >= len(b) || b[i] != ':' {
			return errors.New("missing colon after object key")
		}
		i = skipSpace(b, i+1)
		kind, raw, valueEscaped, next, err := scanValue(b, i)
		if err != nil {
			return err
		}
		if !escaped {
			for n := range cols {
				if bytes.Equal(key, cols[n].key) {
					out[n] = token{kind: kind, raw: raw, escaped: valueEscaped}
					break
				}
			}
		}
		i = skipSpace(b, next)
		if i >= len(b) {
			return errors.New("unterminated JSON object")
		}
		switch b[i] {
		case ',':
			if j := skipSpace(b, i+1); j >= len(b) || b[j] == '}' {
				return errors.New("trailing comma in JSON object")
			}
			i++
		case '}':
			i = skipSpace(b, i+1)
			if i != len(b) {
				return errors.New("trailing bytes after JSON object")
			}
			return nil
		default:
			return errors.New("expected comma or object end")
		}
	}
}

func scanString(b []byte, quote int) (raw []byte, escaped bool, next int, err error) {
	start := quote + 1
	for i := start; i < len(b); i++ {
		switch b[i] {
		case '"':
			return b[start:i], escaped, i + 1, nil
		case '\\':
			escaped = true
			i++
			if i >= len(b) {
				return nil, false, 0, errors.New("unterminated JSON escape")
			}
		case 0, 1, 2, 3, 4, 5, 6, 7, 8, 11, 12, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31:
			return nil, false, 0, errors.New("control byte in JSON string")
		}
	}
	return nil, false, 0, errors.New("unterminated JSON string")
}

func scanValue(b []byte, i int) (kind valueKind, raw []byte, escaped bool, next int, err error) {
	if i >= len(b) {
		return 0, nil, false, 0, errors.New("missing JSON value")
	}
	switch b[i] {
	case '"':
		raw, escaped, next, err = scanString(b, i)
		return valueString, raw, escaped, next, err
	case 'n':
		if i+4 <= len(b) && string(b[i:i+4]) == "null" {
			return valueNull, nil, false, i + 4, nil
		}
	case 't':
		if i+4 <= len(b) && string(b[i:i+4]) == "true" {
			return valueOther, b[i : i+4], false, i + 4, nil
		}
	case 'f':
		if i+5 <= len(b) && string(b[i:i+5]) == "false" {
			return valueOther, b[i : i+5], false, i + 5, nil
		}
	case '{', '[':
		next, err = skipComposite(b, i)
		if err != nil {
			return 0, nil, false, 0, err
		}
		if !json.Valid(b[i:next]) {
			return 0, nil, false, 0, errors.New("invalid composite JSON value")
		}
		return valueOther, b[i:next], false, next, nil
	default:
		start := i
		for i < len(b) {
			c := b[i]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				break
			}
			i++
		}
		if i == start {
			break
		}
		if !jsonNumber(b[start:i]) {
			return 0, nil, false, 0, errors.New("invalid JSON number or literal")
		}
		return valueNumber, b[start:i], false, i, nil
	}
	return 0, nil, false, 0, errors.New("invalid JSON value")
}

func parseInt32(b []byte) (int32, bool) {
	if len(b) == 0 {
		return 0, false
	}
	i := 0
	negative := false
	if b[0] == '-' {
		negative = true
		i++
		if i == len(b) {
			return 0, false
		}
	}
	if b[i] == '0' {
		if i+1 != len(b) {
			return 0, false
		}
		return 0, true
	}
	if b[i] < '1' || b[i] > '9' {
		return 0, false
	}
	var n uint64
	for ; i < len(b); i++ {
		if b[i] < '0' || b[i] > '9' {
			return 0, false
		}
		n = n*10 + uint64(b[i]-'0')
		limit := uint64(1<<31 - 1)
		if negative {
			limit++
		}
		if n > limit {
			return 0, false
		}
	}
	if negative {
		return int32(-int64(n)), true
	}
	return int32(n), true
}

func jsonNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	i := 0
	if b[i] == '-' {
		i++
		if i == len(b) {
			return false
		}
	}
	if b[i] == '0' {
		i++
	} else {
		if b[i] < '1' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && b[i] == '.' {
		i++
		start := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		start := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(b)
}

func skipComposite(b []byte, start int) (int, error) {
	var stack [64]byte
	stack[0] = b[start]
	depth := 1
	for i := start + 1; i < len(b); i++ {
		switch b[i] {
		case '"':
			_, _, next, err := scanString(b, i)
			if err != nil {
				return 0, err
			}
			i = next - 1
		case '{', '[':
			if depth == len(stack) {
				return 0, errors.New("JSON nesting exceeds 64 levels")
			}
			stack[depth] = b[i]
			depth++
		case '}', ']':
			open := stack[depth-1]
			if (open == '{' && b[i] != '}') || (open == '[' && b[i] != ']') {
				return 0, errors.New("mismatched JSON delimiter")
			}
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("unterminated composite JSON value")
}

func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func appendUnescaped(dst, src []byte) ([]byte, error) {
	for i := 0; i < len(src); {
		if src[i] != '\\' {
			start := i
			for i < len(src) && src[i] != '\\' {
				i++
			}
			dst = append(dst, src[start:i]...)
			continue
		}
		i++
		if i >= len(src) {
			return dst, errors.New("short JSON escape")
		}
		switch src[i] {
		case '"', '\\', '/':
			dst = append(dst, src[i])
			i++
		case 'b':
			dst = append(dst, '\b')
			i++
		case 'f':
			dst = append(dst, '\f')
			i++
		case 'n':
			dst = append(dst, '\n')
			i++
		case 'r':
			dst = append(dst, '\r')
			i++
		case 't':
			dst = append(dst, '\t')
			i++
		case 'u':
			if i+5 > len(src) {
				return dst, errors.New("short unicode escape")
			}
			r, ok := hex4(src[i+1 : i+5])
			if !ok {
				return dst, errors.New("invalid unicode escape")
			}
			i += 5
			if utf16.IsSurrogate(r) {
				if i+6 > len(src) || src[i] != '\\' || src[i+1] != 'u' {
					return dst, errors.New("unpaired unicode surrogate")
				}
				r2, ok := hex4(src[i+2 : i+6])
				if !ok {
					return dst, errors.New("invalid low surrogate")
				}
				r = utf16.DecodeRune(r, r2)
				if r == utf8.RuneError {
					return dst, errors.New("invalid surrogate pair")
				}
				i += 6
			}
			dst = utf8.AppendRune(dst, r)
		default:
			return dst, errors.New("invalid JSON escape")
		}
	}
	if !utf8.Valid(dst) {
		return dst, errors.New("invalid UTF-8 in JSON string")
	}
	return dst, nil
}

func hex4(b []byte) (rune, bool) {
	var n rune
	for _, c := range b {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n += rune(c - '0')
		case c >= 'a' && c <= 'f':
			n += rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n += rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return n, true
}
