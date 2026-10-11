// Package canonical implements the bicz-canonical-v1 encoder described in
// docs/CONTRACTS.md §3.1.
//
// The encoding is deterministic: UTF-8, no insignificant whitespace, object keys
// sorted ascending by UTF-8 byte order, array order preserved, only the
// mandatory string escapes, and integers only (through a strict int64 path).
// Floats, duplicate object keys, and values that have no canonical form are hard
// errors, never best-effort coercions.
package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxDepth bounds nesting so a cyclic value fails deterministically instead of
// exhausting the stack.
const maxDepth = 256

var (
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
	numberType     = reflect.TypeOf(json.Number(""))
	timeType       = reflect.TypeOf(time.Time{})
)

// Sentinel errors. Callers test them with errors.Is.
var (
	ErrDuplicateKey = errors.New("canonical: duplicate object key")
	ErrFloat        = errors.New("canonical: floats are forbidden")
	ErrUnsupported  = errors.New("canonical: unsupported type")
	ErrInteger      = errors.New("canonical: integer must be a base-10 literal within int64 range")
	ErrUTF8         = errors.New("canonical: string is not valid UTF-8")
	ErrJSON         = errors.New("canonical: invalid JSON input")
	ErrDepth        = errors.New("canonical: value exceeds maximum nesting depth")
)

// Marshal returns the bicz-canonical-v1 encoding of v.
//
// Supported Go values: nil, bool, all signed integer kinds, unsigned integers
// that fit in int64, string, slices and arrays, maps with a string key type,
// structs (field name from the `json` tag, else the Go field name; a field
// tagged `json:"-"` is omitted), pointers, json.Number (must be a base-10
// integer literal), and json.RawMessage (re-encoded canonically, with duplicate
// keys rejected). time.Time and float kinds are rejected.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	e := encoder{buf: &buf}
	if err := e.value(reflect.ValueOf(v), 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type encoder struct{ buf *bytes.Buffer }

func (e *encoder) value(v reflect.Value, depth int) error {
	if depth > maxDepth {
		return ErrDepth
	}
	if !v.IsValid() {
		e.buf.WriteString("null")
		return nil
	}
	t := v.Type()
	switch t {
	case rawMessageType:
		return e.raw(v.Bytes(), depth)
	case numberType:
		return e.integer(v.String())
	case timeType:
		return fmt.Errorf("%w: %s (format it with canonical.Timestamp)", ErrUnsupported, t)
	}

	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			e.buf.WriteString("null")
			return nil
		}
		return e.value(v.Elem(), depth+1)
	case reflect.Bool:
		if v.Bool() {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buf.WriteString(strconv.FormatInt(v.Int(), 10))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := v.Uint()
		if u > math.MaxInt64 {
			return fmt.Errorf("%w: %d exceeds int64 range", ErrInteger, u)
		}
		e.buf.WriteString(strconv.FormatUint(u, 10))
		return nil
	case reflect.Float32, reflect.Float64:
		return fmt.Errorf("%w: %s", ErrFloat, t)
	case reflect.String:
		return e.str(v.String())
	case reflect.Slice, reflect.Array:
		return e.array(v, depth)
	case reflect.Map:
		return e.object(v, depth)
	case reflect.Struct:
		return e.structValue(v, depth)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupported, t)
	}
}

// integer writes a json.Number through the strict integer path.
func (e *encoder) integer(lit string) error {
	n, err := parseInt64(lit)
	if err != nil {
		return err
	}
	e.buf.WriteString(strconv.FormatInt(n, 10))
	return nil
}

// raw re-encodes a json.RawMessage canonically, rejecting duplicate keys,
// floats, non-canonical integers, and trailing content.
func (e *encoder) raw(raw []byte, depth int) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tree, err := decodeValue(dec, depth)
	if err != nil {
		return err
	}
	switch _, err := dec.Token(); {
	case err == io.EOF:
		// Clean end of input.
	case err == nil:
		return fmt.Errorf("%w: trailing data after JSON value", ErrJSON)
	default:
		return fmt.Errorf("%w: %v", ErrJSON, err)
	}
	return e.value(reflect.ValueOf(tree), depth)
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, ErrDepth
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJSON, err)
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			return decodeObject(dec, depth+1)
		case '[':
			return decodeArray(dec, depth+1)
		default:
			return nil, fmt.Errorf("%w: unexpected delimiter %q", ErrJSON, delim)
		}
	}
	switch tok.(type) {
	case string, bool, nil, json.Number:
		return tok, nil
	default:
		return nil, fmt.Errorf("%w: unexpected token %v", ErrJSON, tok)
	}
}

func decodeObject(dec *json.Decoder, depth int) (any, error) {
	obj := make(map[string]any)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrJSON, err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: object key is not a string", ErrJSON)
		}
		if _, dup := obj[key]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateKey, key)
		}
		val, err := decodeValue(dec, depth)
		if err != nil {
			return nil, err
		}
		obj[key] = val
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, fmt.Errorf("%w: %v", ErrJSON, err)
	}
	return obj, nil
}

func decodeArray(dec *json.Decoder, depth int) (any, error) {
	arr := make([]any, 0, 4)
	for dec.More() {
		val, err := decodeValue(dec, depth)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, fmt.Errorf("%w: %v", ErrJSON, err)
	}
	return arr, nil
}

func (e *encoder) array(v reflect.Value, depth int) error {
	e.buf.WriteByte('[')
	n := v.Len()
	for i := 0; i < n; i++ {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		if err := e.value(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	e.buf.WriteByte(']')
	return nil
}

func (e *encoder) object(v reflect.Value, depth int) error {
	if k := v.Type().Key(); k.Kind() != reflect.String {
		return fmt.Errorf("%w: map key type %s", ErrUnsupported, k)
	}
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	e.buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		if err := e.str(k.String()); err != nil {
			return err
		}
		e.buf.WriteByte(':')
		if err := e.value(v.MapIndex(k), depth+1); err != nil {
			return err
		}
	}
	e.buf.WriteByte('}')
	return nil
}

type structField struct {
	name  string
	index int
}

func (e *encoder) structValue(v reflect.Value, depth int) error {
	rt := v.Type()
	fields := make([]structField, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			if idx := strings.IndexByte(tag, ','); idx >= 0 {
				tag = tag[:idx]
			}
			switch tag {
			case "-":
				continue
			case "":
				// Keep the Go field name.
			default:
				name = tag
			}
		} else if f.Anonymous {
			return fmt.Errorf("%w: embedded field %s needs an explicit json tag", ErrUnsupported, f.Name)
		}
		fields = append(fields, structField{name: name, index: i})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	for i := 1; i < len(fields); i++ {
		if fields[i-1].name == fields[i].name {
			return fmt.Errorf("%w: %q", ErrDuplicateKey, fields[i].name)
		}
	}
	e.buf.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		if err := e.str(f.name); err != nil {
			return err
		}
		e.buf.WriteByte(':')
		if err := e.value(v.Field(f.index), depth+1); err != nil {
			return err
		}
	}
	e.buf.WriteByte('}')
	return nil
}

func (e *encoder) str(s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %q", ErrUTF8, s)
	}
	b := e.buf.AvailableBuffer()
	b = appendJSONString(b, s)
	e.buf.Write(b)
	return nil
}

// appendJSONString writes s as a canonical JSON string: only the mandatory
// escapes (\" \\ \b \f \n \r \t), \u00xx (lowercase hex) for every other code
// point below U+0020, and every remaining code point — including non-ASCII —
// literally as UTF-8. Solidus is not escaped.
func appendJSONString(dst []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0x0f])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// parseInt64 accepts exactly the integer literal grammar of bicz-canonical-v1:
// optional '-', then digits, no leading zeros, no '+', no '-0', magnitude within
// signed 64-bit range.
func parseInt64(lit string) (int64, error) {
	digits := lit
	negative := false
	if digits == "" {
		return 0, fmt.Errorf("%w: empty", ErrInteger)
	}
	switch digits[0] {
	case '-':
		negative = true
		digits = digits[1:]
	case '+':
		return 0, fmt.Errorf("%w: %q has a leading '+'", ErrInteger, lit)
	}
	if digits == "" {
		return 0, fmt.Errorf("%w: %q", ErrInteger, lit)
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' || c == 'e' || c == 'E' {
			return 0, fmt.Errorf("%w: %q", ErrFloat, lit)
		}
		return 0, fmt.Errorf("%w: %q", ErrInteger, lit)
	}
	if len(digits) > 1 && digits[0] == '0' {
		return 0, fmt.Errorf("%w: %q has a leading zero", ErrInteger, lit)
	}
	if negative && digits == "0" {
		return 0, fmt.Errorf("%w: %q is negative zero", ErrInteger, lit)
	}
	n, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInteger, lit)
	}
	return n, nil
}
