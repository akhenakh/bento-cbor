package cbor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/warpstreamlabs/bento/public/service"

	_ "github.com/warpstreamlabs/bento/public/components/io"
	_ "github.com/warpstreamlabs/bento/public/components/pure"
)

const (
	fieldOperator = "operator"
)

// CBORProcessor processes messages by decoding CBOR data,
// displaying the decoded content, and re-encoding it.
type CBORProcessor struct {
	encMode  cbor.EncMode
	decMode  cbor.DecMode
	operator func(msg *service.Message) error
}

func NewProcessor(operatorStr string) (*CBORProcessor, error) {
	p := &CBORProcessor{}
	operator, err := strToOperator(p, operatorStr)
	if err != nil {
		return nil, err
	}

	// Create encoder and decoder modes
	if p.encMode, err = cbor.EncOptions(cbor.PreferredUnsortedEncOptions()).EncMode(); err != nil {
		return nil, fmt.Errorf("failed to create CBOR encoder: %w", err)
	}

	// Create decoder mode with string key maps enabled
	decOpts := cbor.DecOptions{
		MapKeyByteString: cbor.MapKeyByteStringAllowed,
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

		// Create a new decoder
		decoder := cp.decMode.NewDecoder(bytes.NewReader(bytesContent))

		// Decode CBOR to a generic interface
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return fmt.Errorf("failed to decode CBOR: %w %s", err, string(bytesContent))
		}

		// Convert decoded data to map with string keys
		stringKeyData := convertToStringKeyMap(decoded)

		// Convert to JSON
		jsonData, err := json.Marshal(stringKeyData)
		if err != nil {
			return fmt.Errorf("failed to convert CBOR to JSON: %w", err)
		}

		// Set the message content
		msg.SetBytes(jsonData)
		return nil
	}
}

// Add this helper function to the test file if needed
func convertToStringKeyMap(data any) any {
	switch x := data.(type) {
	case map[interface{}]interface{}:
		m := map[string]any{}
		for k, v := range x {
			keyStr := fmt.Sprintf("%v", k)
			m[keyStr] = convertToStringKeyMap(v)
		}
		return m
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			m[k] = convertToStringKeyMap(v)
		}
		return m
	case []any:
		for i, v := range x {
			x[i] = convertToStringKeyMap(v)
		}
	}
	return data
}

func newCBORFromJSONOperator(cp *CBORProcessor) func(msg *service.Message) error {
	return func(msg *service.Message) error {
		bytesContent, err := msg.AsBytes()
		if err != nil {
			return fmt.Errorf("failed to get message bytes: %w", err)
		}

		// Parse JSON
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
`).
		Fields(
			service.NewStringEnumField(fieldOperator, "to_json", "from_json").
				Description("The operator to execute, to_json|from_json").
				Default("to_json"),
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

			return NewProcessor(operatorStr)
		})
	if err != nil {
		panic(err)
	}
}

func strToOperator(p *CBORProcessor, operatorStr string) (func(msg *service.Message) error, error) {
	switch operatorStr {
	case "to_json":
		return newCBORToJSONOperator(p), nil
	case "from_json":
		return newCBORFromJSONOperator(p), nil
	default:
		return nil, errors.New("invalid operator type")
	}
}
