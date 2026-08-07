package cbor

import (
	"fmt"
	"sort"

	"github.com/fxamacker/cbor/v2"
)

// Defaults for the packed layout.
//
// The tag numbers are application-private: they are NOT registered with IANA.
// They only need to be stable between the writer and the reader, and both are
// configurable so they can be moved if they ever collide with something else in
// your pipeline.
const (
	DefaultDocTag   uint64 = 60000 // wraps a whole packed document
	DefaultTableTag uint64 = 60001 // wraps a single packed [cols, rows] table
	DefaultMinRows         = 8
	DefaultMinFill         = 0.5
)

// PackOptions controls the schema-agnostic size optimisations applied by the
// "pack" operator.
//
// None of these require knowing the shape of the data up front: the column
// names are derived from the values at encode time and travel inside the
// payload, so "unpack" can reverse the transform with no configuration.
type PackOptions struct {
	// OmitZero drops map entries whose value is null, false, 0, "" or an empty
	// container before encoding.
	//
	// This is lossy: it collapses "absent", "null" and "zero" into a single
	// state, exactly like proto3 field presence. Consumers must treat a missing
	// key as the zero value. Off by default.
	OmitZero bool

	// AllowSparse permits packing arrays whose elements do not all share the
	// same key set, by taking the union of keys and writing null in the cells
	// that have no value.
	//
	// This is lossy: a key that was absent from a row round-trips as an explicit
	// null, because CBOR "undefined" decodes to Go nil and cannot be told apart
	// from null through a generic any. Off by default, which makes packing
	// strictly lossless at the cost of skipping heterogeneous arrays.
	AllowSparse bool

	// MinRows is the shortest array worth packing. Below this the column header
	// costs more than the repeated keys save.
	MinRows int

	// MinFill is the minimum ratio of populated cells (0..1) required before a
	// sparse array is packed. Only consulted when AllowSparse is set.
	MinFill float64

	// DocTag and TableTag are the CBOR tag numbers used to mark a packed
	// document and a packed table respectively.
	DocTag   uint64
	TableTag uint64
}

// DefaultPackOptions returns the lossless defaults.
func DefaultPackOptions() PackOptions {
	return PackOptions{
		OmitZero:    false,
		AllowSparse: false,
		MinRows:     DefaultMinRows,
		MinFill:     DefaultMinFill,
		DocTag:      DefaultDocTag,
		TableTag:    DefaultTableTag,
	}
}

// Pack applies the configured transforms and wraps the result in the document
// tag so that Unpack (and any other reader) can identify it.
func Pack(v any, o PackOptions) any {
	if o.OmitZero {
		v = omitZeroTree(v)
	}
	return cbor.Tag{Number: o.DocTag, Content: packTree(v, o)}
}

// Unpack reverses Pack. Input that was never packed is returned untouched, so
// the same operator can read both old and new records during a rollout.
func Unpack(v any, o PackOptions) (any, error) {
	if t, ok := v.(cbor.Tag); ok && t.Number == o.DocTag {
		v = t.Content
	}
	return unpackTree(v, o)
}

// IsPacked reports whether a decoded document carries the packed document tag.
func IsPacked(v any, o PackOptions) bool {
	t, ok := v.(cbor.Tag)
	return ok && t.Number == o.DocTag
}

// --- omit zero ---------------------------------------------------------------

func isZeroValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case []byte:
		return len(t) == 0
	case float64:
		return t == 0
	case float32:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	case uint64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func omitZeroTree(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			c := omitZeroTree(vv)
			if isZeroValue(c) {
				continue
			}
			out[k] = c
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = omitZeroTree(vv)
		}
		return out
	}
	return v
}

// --- pack --------------------------------------------------------------------

func packTree(v any, o PackOptions) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = packTree(vv, o)
		}
		return out
	case []any:
		if packed, ok := packTable(t, o); ok {
			return packed
		}
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = packTree(vv, o)
		}
		return out
	}
	return v
}

// packTable turns a homogeneous array of objects into a single column header
// plus positional rows. Returns false when the array does not qualify, in which
// case the caller leaves it as-is.
func packTable(rows []any, o PackOptions) (any, bool) {
	if len(rows) < o.MinRows {
		return nil, false
	}

	maps := make([]map[string]any, len(rows))
	for i, r := range rows {
		m, ok := r.(map[string]any)
		if !ok || m == nil {
			return nil, false // not an array of objects
		}
		maps[i] = m
	}

	union := make(map[string]bool)
	for _, m := range maps {
		for k := range m {
			union[k] = true
		}
	}
	if len(union) == 0 {
		return nil, false
	}

	// Lossless mode: every row must carry exactly the same key set, so no cell
	// is ever synthesised and "absent" never turns into "null".
	populated := 0
	for _, m := range maps {
		if !o.AllowSparse && len(m) != len(union) {
			return nil, false
		}
		populated += len(m)
	}
	if o.AllowSparse {
		if fill := float64(populated) / float64(len(maps)*len(union)); fill < o.MinFill {
			return nil, false
		}
	}

	cols := make([]string, 0, len(union))
	for k := range union {
		cols = append(cols, k)
	}
	sort.Strings(cols) // deterministic bytes for identical input

	colsAny := make([]any, len(cols))
	for i, c := range cols {
		colsAny[i] = c
	}

	packedRows := make([]any, len(maps))
	for i, m := range maps {
		cells := make([]any, len(cols))
		for j, c := range cols {
			if vv, ok := m[c]; ok {
				cells[j] = packTree(vv, o)
			}
		}
		packedRows[i] = cells
	}

	return cbor.Tag{
		Number:  o.TableTag,
		Content: []any{colsAny, packedRows},
	}, true
}

// --- unpack ------------------------------------------------------------------

func unpackTree(v any, o PackOptions) (any, error) {
	switch t := v.(type) {
	case cbor.Tag:
		if t.Number == o.TableTag {
			return expandTable(t.Content, o)
		}
		// Unrelated tag: keep the content, drop the wrapper, so the result stays
		// JSON-serialisable.
		return unpackTree(t.Content, o)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			c, err := unpackTree(vv, o)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			c, err := unpackTree(vv, o)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}

func expandTable(content any, o PackOptions) (any, error) {
	parts, ok := content.([]any)
	if !ok || len(parts) != 2 {
		return nil, fmt.Errorf("packed table: expected [cols, rows], got %T", content)
	}

	rawCols, ok := parts[0].([]any)
	if !ok {
		return nil, fmt.Errorf("packed table: expected column array, got %T", parts[0])
	}
	cols := make([]string, len(rawCols))
	for i, c := range rawCols {
		s, ok := asString(c)
		if !ok {
			return nil, fmt.Errorf("packed table: column %d is %T, want string", i, c)
		}
		cols[i] = s
	}

	rawRows, ok := parts[1].([]any)
	if !ok {
		return nil, fmt.Errorf("packed table: expected row array, got %T", parts[1])
	}

	out := make([]any, len(rawRows))
	for i, r := range rawRows {
		cells, ok := r.([]any)
		if !ok {
			return nil, fmt.Errorf("packed table: row %d is %T, want array", i, r)
		}
		if len(cells) != len(cols) {
			return nil, fmt.Errorf("packed table: row %d has %d cells, want %d", i, len(cells), len(cols))
		}
		m := make(map[string]any, len(cols))
		for j, c := range cols {
			v, err := unpackTree(cells[j], o)
			if err != nil {
				return nil, err
			}
			m[c] = v
		}
		out[i] = m
	}
	return out, nil
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	}
	return "", false
}
