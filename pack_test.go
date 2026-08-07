package cbor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warpstreamlabs/bento/public/service"
)

// All fixtures below are synthetic. Field names are deliberately meaningless so
// that the tests describe the *shape* the packer cares about — a long array of
// uniform objects — without encoding anyone's schema.
//
// To exercise the packer against real data, point BENTO_CBOR_FIXTURE at a raw
// CBOR file (see TestPackFixture). Nothing is read from disk otherwise.

// uniformDoc builds a document with a few scalar fields plus an array of
// objects that all share the same key set.
func uniformDoc(n int) map[string]any {
	items := make([]any, n)
	for i := range items {
		items[i] = map[string]any{
			"alpha":   1.5 + float64(i)*1e-5,
			"bravo":   -2.25,
			"charlie": float64(1_000_000_000 + i*1000),
			"delta":   float64(i % 360),
			"echo":    false,
			"foxtrot": "kind_a",
		}
	}
	return map[string]any{
		"id":    "00000000-0000-0000-0000-000000000001",
		"group": "00000000-0000-0000-0000-0000000000ff",
		"note":  nil,
		"items": items,
	}
}

// wideDoc mimics the shape the packer targets: many narrow fields per element,
// where repeated key names dominate the encoding.
func wideDoc(rows, fields int) map[string]any {
	items := make([]any, rows)
	for i := range items {
		m := make(map[string]any, fields)
		for f := 0; f < fields; f++ {
			m[fmt.Sprintf("field_number_%02d", f)] = float64(f) + float64(i)*1e-4
		}
		items[i] = m
	}
	return map[string]any{"items": items}
}

func roundTrip(t *testing.T, doc any, o PackOptions) (any, int) {
	t.Helper()

	p, err := NewProcessor("pack", WithPackOptions(o))
	require.NoError(t, err)

	packed, err := p.encMode.Marshal(Pack(mustJSONTree(t, mustMarshal(t, doc)), o))
	require.NoError(t, err)

	var decoded any
	require.NoError(t, p.decMode.Unmarshal(packed, &decoded))
	require.True(t, IsPacked(decoded, o), "packed payload must carry the document tag")

	out, err := Unpack(decoded, o)
	require.NoError(t, err)

	return out, len(packed)
}

func mustJSONTree(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestPackRoundTripIsLossless(t *testing.T) {
	o := DefaultPackOptions()
	doc := uniformDoc(50)

	got, _ := roundTrip(t, doc, o)

	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got)
}

func TestPackShrinksUniformArrays(t *testing.T) {
	o := DefaultPackOptions()
	doc := wideDoc(1700, 36)

	p, err := NewProcessor("pack", WithPackOptions(o))
	require.NoError(t, err)

	plain, err := p.encMode.Marshal(mustJSONTree(t, mustMarshal(t, doc)))
	require.NoError(t, err)

	got, packedLen := roundTrip(t, doc, o)

	ratio := float64(len(plain)) / float64(packedLen)
	t.Logf("rows=1700 fields=36 plain=%d packed=%d ratio=%.2fx", len(plain), packedLen, ratio)

	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got, "must stay lossless")
	assert.Greater(t, ratio, 2.0, "packing a wide uniform array should be a clear win")
}

func TestPackSkipsShortArrays(t *testing.T) {
	o := DefaultPackOptions()
	// Below MinRows the column header would cost more than it saves.
	doc := uniformDoc(o.MinRows - 1)

	got, _ := roundTrip(t, doc, o)
	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got)
}

// withExtraKey returns doc with one element carrying an additional field.
func withExtraKey(doc map[string]any, idx int) map[string]any {
	items := doc["items"].([]any)
	extra := map[string]any{}
	for k, v := range items[idx].(map[string]any) {
		extra[k] = v
	}
	extra["golf"] = "extra"
	items[idx] = extra
	return doc
}

func TestPackStrictModeSkipsHeterogeneousArrays(t *testing.T) {
	o := DefaultPackOptions() // AllowSparse == false
	doc := withExtraKey(uniformDoc(20), 3)

	got, _ := roundTrip(t, doc, o)

	// Lossless: the odd element keeps its extra key and no other gains a null.
	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got)
	gotItems := got.(map[string]any)["items"].([]any)
	_, present := gotItems[0].(map[string]any)["golf"]
	assert.False(t, present, "strict mode must not synthesise null cells")
}

func TestPackSparseModePacksHeterogeneousArrays(t *testing.T) {
	o := DefaultPackOptions()
	o.AllowSparse = true
	doc := withExtraKey(uniformDoc(20), 3)

	got, _ := roundTrip(t, doc, o)
	gotItems := got.(map[string]any)["items"].([]any)

	// Documented lossiness: absent keys come back as explicit nulls.
	assert.Equal(t, "extra", gotItems[3].(map[string]any)["golf"])
	v, present := gotItems[0].(map[string]any)["golf"]
	assert.True(t, present)
	assert.Nil(t, v)
}

func TestPackOmitZero(t *testing.T) {
	o := DefaultPackOptions()
	o.OmitZero = true

	doc := uniformDoc(20)
	got, _ := roundTrip(t, doc, o)

	// `note: null` and `echo: false` are dropped entirely.
	assert.NotContains(t, got.(map[string]any), "note")
	items := got.(map[string]any)["items"].([]any)
	assert.NotContains(t, items[0].(map[string]any), "echo")
	// Non-zero values survive.
	assert.Equal(t, "kind_a", items[0].(map[string]any)["foxtrot"])
}

func TestUnpackPassesThroughUnpackedPayloads(t *testing.T) {
	// A record written by from_json before the rollout must still be readable.
	doc := uniformDoc(20)

	from, err := NewProcessor("from_json")
	require.NoError(t, err)

	out, err := from.Process(context.Background(), service.NewMessage(mustMarshal(t, doc)))
	require.NoError(t, err)

	unpack, err := NewProcessor("unpack")
	require.NoError(t, err)

	out, err = unpack.Process(context.Background(), out[0])
	require.NoError(t, err)

	b, err := out[0].AsBytes()
	require.NoError(t, err)

	var got any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got)
}

func TestToJSONRejectsPackedPayload(t *testing.T) {
	pack, err := NewProcessor("pack")
	require.NoError(t, err)
	packed, err := pack.Process(context.Background(), service.NewMessage(mustMarshal(t, uniformDoc(20))))
	require.NoError(t, err)

	toJSON, err := NewProcessor("to_json")
	require.NoError(t, err)
	_, err = toJSON.Process(context.Background(), packed[0])
	require.ErrorContains(t, err, "operator: unpack")
}

func TestPackUnpackThroughProcessors(t *testing.T) {
	doc := uniformDoc(100)

	pack, err := NewProcessor("pack")
	require.NoError(t, err)
	unpack, err := NewProcessor("unpack")
	require.NoError(t, err)

	msgs, err := pack.Process(context.Background(), service.NewMessage(mustMarshal(t, doc)))
	require.NoError(t, err)

	msgs, err = unpack.Process(context.Background(), msgs[0])
	require.NoError(t, err)

	b, err := msgs[0].AsBytes()
	require.NoError(t, err)

	var got any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, mustJSONTree(t, mustMarshal(t, doc)), got)
}

// TestPackFixture round-trips a CBOR file supplied by the caller and reports the
// achieved ratio. It exists so the packer can be validated against real data
// without that data ever entering this repository.
//
//	BENTO_CBOR_FIXTURE=/path/to/record.cbor go test -run TestPackFixture -v
func TestPackFixture(t *testing.T) {
	path := os.Getenv("BENTO_CBOR_FIXTURE")
	if path == "" {
		t.Skip("set BENTO_CBOR_FIXTURE to a raw CBOR file to run this")
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	o := DefaultPackOptions()
	p, err := NewProcessor("pack", WithPackOptions(o))
	require.NoError(t, err)

	var doc any
	require.NoError(t, p.decMode.Unmarshal(raw, &doc))

	packed, err := p.encMode.Marshal(Pack(doc, o))
	require.NoError(t, err)

	var decoded any
	require.NoError(t, p.decMode.Unmarshal(packed, &decoded))
	got, err := Unpack(decoded, o)
	require.NoError(t, err)

	assert.Equal(t, doc, got, "packing must round-trip exactly")
	t.Logf("original=%d packed=%d ratio=%.2fx", len(raw), len(packed),
		float64(len(raw))/float64(len(packed)))
}
