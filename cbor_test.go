package cbor

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warpstreamlabs/bento/public/service"
)

func mustEncodeMapToCBORBase64(t *testing.T, input map[string]any) string {
	// Create CBOR encoder with default options
	encMode, err := cbor.EncOptions{}.EncMode()
	if err != nil {
		t.Fatalf("Failed to create CBOR encoder: %v", err)
	}

	// Marshal input map to CBOR
	cborData, err := encMode.Marshal(input)
	if err != nil {
		t.Fatalf("Failed to marshal to CBOR: %v", err)
	}

	// Encode CBOR data to base64
	return b64.StdEncoding.EncodeToString(cborData)
}

func TestCBORToJson(t *testing.T) {
	type testCase struct {
		name           string
		base64Input    string
		expectedOutput any
	}

	data := map[string]any{
		"key":      "foo",
		"trueKey":  true,
		"falseKey": false,
		"nullKey":  nil,
		"intKey":   float64(123),
		"floatKey": 45.6,
		"array": []any{
			"bar",
		},
		"nested": map[string]any{
			"key": "baz",
		},
	}

	tests := []testCase{
		{
			name:           "basic",
			base64Input:    mustEncodeMapToCBORBase64(t, data),
			expectedOutput: data,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proc, err := NewProcessor("to_json")
			require.NoError(t, err)

			inputBytes, err := b64.StdEncoding.DecodeString(test.base64Input)
			require.NoError(t, err)

			input := service.NewMessage(inputBytes)

			msgs, err := proc.Process(context.Background(), input)
			require.NoError(t, err)
			require.Len(t, msgs, 1)

			// Get the JSON bytes from the message
			jsonBytes, err := msgs[0].AsBytes()
			require.NoError(t, err)

			// Unmarshal JSON directly with standard json.Unmarshal
			var act any
			err = json.Unmarshal(jsonBytes, &act)
			require.NoError(t, err)

			assert.Equal(t, test.expectedOutput, act)
		})
	}
}

func TestCBORFromJson(t *testing.T) {
	type testCase struct {
		name           string
		input          string
		expectedOutput any
	}

	data := map[string]any{
		"key":      "foo",
		"trueKey":  true,
		"falseKey": false,
		"nullKey":  nil,
		"intKey":   float64(123),
		"floatKey": 45.6,
		"array": []any{
			"bar",
		},
		"nested": map[string]any{
			"key": "baz",
		},
	}

	// Marshal the data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal to JSON: %v", err)
	}

	tests := []testCase{
		{
			name:           "basic",
			input:          string(jsonData),
			expectedOutput: data,
		},
		{
			name:  "various ints",
			input: `{"int8": 13, "uint8": 254, "int16": -257, "uint16" : 65534, "int32" : -70123, "uint32" : 2147483648, "int64" : -9223372036854775808, "uint64": 18446744073709551615}`,
			expectedOutput: map[string]any{
				"int8":   float64(13),
				"uint8":  float64(254),
				"int16":  float64(-257),
				"uint16": float64(65534),
				"int32":  float64(-70123),
				"uint32": float64(2147483648),
				"int64":  float64(-9223372036854775808),
				"uint64": float64(18446744073709551615),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proc, err := NewProcessor("from_json")
			require.NoError(t, err)

			input := service.NewMessage([]byte(test.input))

			msgs, err := proc.Process(context.Background(), input)
			require.NoError(t, err)
			require.Len(t, msgs, 1)

			rawBytes, err := msgs[0].AsBytes()
			require.NoError(t, err)

			var act any
			require.NoError(t, cbor.Unmarshal(rawBytes, &act))

			// Convert the decoded CBOR data to a map with string keys for comparison
			convertedAct := convertToStringKeyMap(act)
			assert.Equal(t, test.expectedOutput, convertedAct)
		})
	}
}
