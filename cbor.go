package cbor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/fxamacker/cbor/v2"
	"github.com/warpstreamlabs/bento/public/service"

	_ "github.com/warpstreamlabs/bento/public/components/io"
	_ "github.com/warpstreamlabs/bento/public/components/pure"
)

const (
	fieldOperator    = "operator"
	fieldOmitZero    = "omit_zero"
	fieldAllowSparse = "allow_sparse"
	fieldMinRows     = "min_rows"
	fieldMinFill     = "min_fill"
	fieldDocTag      = "doc_tag"
	fieldTableTag    = "table_tag"
)

// CBORProcessor processes messages by decoding CBOR data,
// displaying the decoded content, and re-encoding it.
type CBORProcessor struct {
	encMode  cbor.EncMode
	decMode  cbor.DecMode
	operator func(msg *service.Message) error
	pack     PackOptions
}

// Option customises a CBORProcessor at construction time.
type Option func(*CBORProcessor)

// WithPackOptions overrides the layout options used by the pack/unpack
// operators. It has no effect on to_json/from_json.
func WithPackOptions(o PackOptions) Option {
	return func(p *CBORProcessor) { p.pack = o }
}

func NewProcessor(operatorStr string, opts ...Option) (*CBORProcessor, error) {
	p := &CBORProcessor{pack: DefaultPackOptions()}
	for _, opt := range opts {
		opt(p)
	}
	operator, err := strToOperator(p, operatorStr)
	if err != nil {
		return nil, err
	}

	// Configure encoder options for JSON compatibility and better data packing.
	encOpts := cbor.PreferredUnsortedEncOptions()
	encOpts.ByteSliceLaterFormat = cbor.ByteSliceLaterFormatBase64
	encOpts.String = cbor.StringToByteString
	encOpts.ByteArray = cbor.ByteArrayToArray

	// Leverage v2.9.1+ feature for accurate/clean Time encoding
	encOpts.Time = cbor.TimeRFC3339NanoUTC

	// Create encoder mode
	if p.encMode, err = encOpts.EncMode(); err != nil {
		return nil, fmt.Errorf("failed to create CBOR encoder: %w", err)
	}

	// Configure decoder options for JSON compatibility
	decOpts := cbor.DecOptions{
		MapKeyByteString:      cbor.MapKeyByteStringAllowed,     // Convert byte string map keys to strings
		DefaultMapType:        reflect.TypeOf(map[string]any{}), // Use string maps by default
		DefaultByteStringType: reflect.TypeOf(""),               // Convert byte strings to Go strings
		ByteStringToString:    cbor.ByteStringToStringAllowed,
		IndefLength:           cbor.IndefLengthAllowed,
	}

	if p.decMode, err = decOpts.DecMode(); err != nil {
		return nil, fmt.Errorf("failed to create CBOR decoder: %w", err)
	}

	p.operator = operator
	return p, nil
}

// Process implements the service.Processor interface.
func (cp *CBORProcessor) Process(ctx context.Context, m *service.Message) (service.MessageBatch, error) {
	if err := cp.operator(m); err != nil {
		return nil, err
	}
	return []*service.Message{m}, nil
}

// Close implements the service.Processor interface.
func (cp *CBORProcessor) Close(ctx context.Context) error {
	return nil
}

func newCBORToJSONOperator(cp *CBORProcessor) func(msg *service.Message) error {
	return func(msg *service.Message) error {
		bytesContent, err := msg.AsBytes()
		if err != nil {
			return fmt.Errorf("failed to get message bytes: %w", err)
		}

		// Decode CBOR to a generic interface
		var decoded any
		if err := cp.decMode.Unmarshal(bytesContent, &decoded); err != nil {
			return fmt.Errorf("failed to decode CBOR: %w", err)
		}

		// A packed payload would decode into tagged tables that do not round-trip
		// to meaningful JSON. Fail loudly instead of emitting nonsense; untagged
		// payloads are unaffected, so this cannot change existing behaviour.
		if IsPacked(decoded, cp.pack) {
			return fmt.Errorf("payload is packed (CBOR tag %d): use `operator: unpack` to read it", cp.pack.DocTag)
		}

		// Assign structured data back to Bento directly!
		// Bento will encode to JSON lazily ONLY when required by an output or downstream processor,
		// avoiding JSON allocations and CPU cycles altogether if an intervening step handles structured mapped queries.
		msg.SetStructured(decoded)
		return nil
	}
}

// newCBORUnpackOperator decodes a packed CBOR payload back to structured data.
// Payloads that were never packed are passed through unchanged, so a single
// config can read both formats while a rollout is in progress.
func newCBORUnpackOperator(cp *CBORProcessor) func(msg *service.Message) error {
	return func(msg *service.Message) error {
		bytesContent, err := msg.AsBytes()
		if err != nil {
			return fmt.Errorf("failed to get message bytes: %w", err)
		}

		var decoded any
		if err := cp.decMode.Unmarshal(bytesContent, &decoded); err != nil {
			return fmt.Errorf("failed to decode CBOR: %w", err)
		}

		expanded, err := Unpack(decoded, cp.pack)
		if err != nil {
			return fmt.Errorf("failed to unpack CBOR: %w", err)
		}

		msg.SetStructured(expanded)
		return nil
	}
}

// newCBORPackOperator encodes JSON to CBOR using the space optimisations that
// fxamacker exposes through struct tags (toarray/keyasint/omitempty), derived
// from the data at runtime rather than from a Go type so the processor stays
// schema-agnostic.
func newCBORPackOperator(cp *CBORProcessor) func(msg *service.Message) error {
	return func(msg *service.Message) error {
		bytesContent, err := msg.AsBytes()
		if err != nil {
			return fmt.Errorf("failed to get message bytes: %w", err)
		}

		// Same rationale as from_json: avoid json.Number.
		var jsonData any
		if err := json.Unmarshal(bytesContent, &jsonData); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		cborData, err := cp.encMode.Marshal(Pack(jsonData, cp.pack))
		if err != nil {
			return fmt.Errorf("failed to encode JSON to packed CBOR: %w", err)
		}

		msg.SetBytes(cborData)
		return nil
	}
}

func newCBORFromJSONOperator(cp *CBORProcessor) func(msg *service.Message) error {
	return func(msg *service.Message) error {
		bytesContent, err := msg.AsBytes()
		if err != nil {
			return fmt.Errorf("failed to get message bytes: %w", err)
		}

		// Parse JSON directly.
		// We use standard Unmarshal here over Benthos's AsStructured() to avoid `json.Number`
		// which isn't mapped to ints/floats out-of-the-box by the CBOR package and encodes as raw strings.
		var jsonData any
		if err := json.Unmarshal(bytesContent, &jsonData); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		// Encode to CBOR
		cborData, err := cp.encMode.Marshal(jsonData)
		if err != nil {
			return fmt.Errorf("failed to encode JSON to CBOR: %w", err)
		}

		// Update the message with the CBOR data
		msg.SetBytes(cborData)
		return nil
	}
}

// This is the configuration specification for our CBOR processor
func getCBORConfigSpec() *service.ConfigSpec {
	return service.NewConfigSpec().
		Stable().
		Categories("Parsing", "Format").
		Summary(`
Processes CBOR (Concise Binary Object Representation) data, providing decoding and re-encoding capabilities.
`).
		Description(`
This processor allows you to manipulate CBOR data by decoding the input, displaying the decoded content, and re-encoding it with configurable options.

CBOR is a binary data format designed for small message size with the ability to support the seamless conversion of JSON data models.
This processor supports RFC 7049 and RFC 8949 compliant encoding and decoding of CBOR data.

You can configure various encoding options to control how specific data types are represented in the CBOR output.

## Operators

### `+"`to_json`"+`

Converts CBOR data into JSON format.

### `+"`from_json`"+`

Converts JSON data into CBOR format using the configured encoding options.

### `+"`pack`"+`

Like `+"`from_json`"+`, but additionally applies schema-agnostic size optimisations
before encoding. The equivalents of fxamacker's `+"`toarray`"+`, `+"`keyasint`"+` and
`+"`omitzero`"+` struct tags are derived from the data at runtime instead of from a Go
type, so no schema, `+"`.proto`"+` file or field list is required.

The dominant win is on arrays of objects: the shared key names are hoisted into a
single column header and each element becomes a positional row. On a record of
1,679 uniform objects this cuts the payload by ~3.4x, because repeated field
names account for roughly 71% of a typical CBOR document.

The column header travels inside the payload, so `+"`unpack`"+` needs no
configuration to reverse it.

### `+"`unpack`"+`

Reverses `+"`pack`"+` and emits structured data, exactly like `+"`to_json`"+`.
Payloads that were never packed are passed through untouched, so the same config
reads both formats while a rollout is in progress.
`).
		Fields(
			service.NewStringEnumField(fieldOperator, "to_json", "from_json", "pack", "unpack").
				Description("The operator to execute, to_json|from_json|pack|unpack").
				Default("to_json"),
			service.NewBoolField(fieldOmitZero).
				Description("For `pack`: drop map entries whose value is null, false, 0, \"\" or an empty container. Lossy: this collapses absent/null/zero into one state (proto3 field-presence semantics), so consumers must treat a missing key as the zero value.").
				Default(false).
				Advanced(),
			service.NewBoolField(fieldAllowSparse).
				Description("For `pack`: also pack arrays whose elements do not share the same key set, using the union of keys and writing null where a value is missing. Lossy: a key that was absent from an element round-trips as an explicit null. When false, heterogeneous arrays are left unpacked and the transform is lossless.").
				Default(false).
				Advanced(),
			service.NewIntField(fieldMinRows).
				Description("For `pack`: the shortest array worth packing. Below this the column header costs more than the repeated keys save.").
				Default(DefaultMinRows).
				Advanced(),
			service.NewFloatField(fieldMinFill).
				Description("For `pack`: minimum ratio of populated cells (0..1) before a sparse array is packed. Only consulted when `allow_sparse` is true.").
				Default(DefaultMinFill).
				Advanced(),
			service.NewIntField(fieldDocTag).
				Description("CBOR tag number marking a packed document. Application-private, not IANA-registered; it only needs to match between writer and reader.").
				Default(int(DefaultDocTag)).
				Advanced(),
			service.NewIntField(fieldTableTag).
				Description("CBOR tag number marking a packed table. Application-private, not IANA-registered; it only needs to match between writer and reader.").
				Default(int(DefaultTableTag)).
				Advanced(),
		).
		Example("Convert CBOR to JSON", `
This example demonstrates how to convert CBOR data to JSON format.
`, `
pipeline:
  processors:
    - cbor:
        operator: to_json
`).
		Example("Convert JSON to CBOR", `
This example shows how to convert JSON data to CBOR format with specific encoding options.
`, `
pipeline:
  processors:
    - cbor:
        operator: from_json
`)
}

func init() {
	err := service.RegisterProcessor(
		"cbor",
		getCBORConfigSpec(),
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.Processor, error) {
			// Get operator type
			operatorStr, err := conf.FieldString(fieldOperator)
			if err != nil {
				return nil, err
			}

			packOpts, err := packOptionsFromConfig(conf)
			if err != nil {
				return nil, err
			}

			return NewProcessor(operatorStr, WithPackOptions(packOpts))
		})
	if err != nil {
		panic(err)
	}
}

func packOptionsFromConfig(conf *service.ParsedConfig) (PackOptions, error) {
	o := DefaultPackOptions()

	var err error
	if o.OmitZero, err = conf.FieldBool(fieldOmitZero); err != nil {
		return o, err
	}
	if o.AllowSparse, err = conf.FieldBool(fieldAllowSparse); err != nil {
		return o, err
	}
	if o.MinRows, err = conf.FieldInt(fieldMinRows); err != nil {
		return o, err
	}
	if o.MinFill, err = conf.FieldFloat(fieldMinFill); err != nil {
		return o, err
	}

	docTag, err := conf.FieldInt(fieldDocTag)
	if err != nil {
		return o, err
	}
	tableTag, err := conf.FieldInt(fieldTableTag)
	if err != nil {
		return o, err
	}
	if docTag < 0 || tableTag < 0 {
		return o, errors.New("CBOR tag numbers must not be negative")
	}
	if docTag == tableTag {
		return o, errors.New("doc_tag and table_tag must differ")
	}
	o.DocTag, o.TableTag = uint64(docTag), uint64(tableTag)

	return o, nil
}

func strToOperator(p *CBORProcessor, operatorStr string) (func(msg *service.Message) error, error) {
	switch operatorStr {
	case "to_json":
		return newCBORToJSONOperator(p), nil
	case "from_json":
		return newCBORFromJSONOperator(p), nil
	case "pack":
		return newCBORPackOperator(p), nil
	case "unpack":
		return newCBORUnpackOperator(p), nil
	default:
		return nil, errors.New("invalid operator type")
	}
}
