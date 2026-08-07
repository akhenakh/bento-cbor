# Bento CBOR Processor

A [Bento](https://warpstreamlabs.github.io/bento/) processor plugin for working with CBOR (Concise Binary Object Representation) data. This processor allows you to convert between JSON and CBOR formats.

## Overview

CBOR is a binary data format designed for small message size with the ability to support the seamless conversion of JSON data models. This processor is RFC 7049 and RFC 8949 compliant and provides encoding and decoding capabilities for CBOR data.

## Installation

### Using Pre-built Binary

Download a pre-built binary from the releases page.


## Usage

The CBOR processor supports four operations:

1. `to_json`: Converts CBOR data into JSON format
2. `from_json`: Converts JSON data into CBOR format
3. `pack`: Like `from_json`, plus schema-agnostic size optimisations
4. `unpack`: Reverses `pack` (and passes plain CBOR through untouched)

### Configuration Examples

#### Convert CBOR to JSON

```yaml
pipeline:
  processors:
    - cbor:
        operator: to_json
```

#### Convert JSON to CBOR

```yaml
pipeline:
  processors:
    - cbor:
        operator: from_json
```

#### Pack / unpack

```yaml
pipeline:
  processors:
    - cbor:
        operator: pack    # JSON -> optimised CBOR
    - cbor:
        operator: unpack  # optimised CBOR -> structured data
```

### Size optimisation (`pack`)

`fxamacker/cbor` exposes its size optimisations (`toarray`, `keyasint`,
`omitempty`, `omitzero`) as **struct tags**, applied by reflecting over a typed
Go struct. This processor encodes `map[string]any` decoded from arbitrary JSON,
so there are no fields for those tags to hang on and that whole class of
optimisation is structurally unreachable.

`pack` reproduces the same effects as transforms over the generic tree, deriving
the layout from the data at encode time and carrying it inside the payload. No
schema, `.proto` file or field list is needed, and `unpack` reverses everything
with no configuration.

| struct tag | `pack` equivalent |
|---|---|
| `toarray` | homogeneous arrays of objects become a single column header plus positional rows |
| `keyasint` | field names are written once per array instead of once per element |
| `omitzero` | `omit_zero: true` drops null / false / `0` / `""` / empty containers |

The win comes from repeated field names, which dominate a typical CBOR document
made of many similar objects. In a document of 1,700 objects with 36 fields each
(`TestPackShrinksUniformArrays`), the names are re-encoded 1,700 times and
`pack` writes them once:

```
rows=1700 fields=36  plain=1532762  packed=554147  ratio=2.77x
```

The ratio scales with how much of your payload is field names, so it climbs with
longer names, more fields per object and more elements per array. Measure your
own data with `TestPackFixture` (below) rather than relying on this figure.

Values are untouched — `PreferredUnsortedEncOptions` already applies
`ShortestFloat16`, so floats that fit are 3 bytes and there is little left to
reclaim on that side.

To check the ratio on your own data without copying it anywhere:

```sh
BENTO_CBOR_FIXTURE=/path/to/record.cbor go test -run TestPackFixture -v
```

That test asserts an exact round-trip and prints the ratio. The file is read at
run time only; no fixture data is stored in this repository.

#### Losslessness

The defaults are lossless: `pack` → `unpack` returns exactly the input tree
(asserted against real records in the test suite). Two opt-in settings trade
fidelity for size:

- **`omit_zero`** collapses absent / null / zero into one state, exactly like
  proto3 field presence. Consumers must read a missing key as the zero value.
- **`allow_sparse`** packs arrays whose elements have differing key sets by
  taking the union and writing null where a value is missing — so a key that was
  *absent* comes back as an explicit *null*. CBOR `undefined` cannot be told
  apart from `null` through a generic `any`, so absence cannot be preserved.
  With `allow_sparse: false` (the default), heterogeneous arrays are simply left
  unpacked.

#### Migration

`unpack` passes non-packed payloads through unchanged, so you can switch readers
to `unpack` first and writers to `pack` afterwards without a flag day. Records
written before the switch stay readable.

`to_json` deliberately rejects packed payloads with an error pointing at
`unpack`, rather than emitting a confusing half-decoded document. Payloads that
were never packed carry no tag and are unaffected.

#### Options

All are advanced fields and only affect `pack` / `unpack`:

| field | default | meaning |
|---|---|---|
| `omit_zero` | `false` | drop null / false / `0` / `""` / empty containers (lossy) |
| `allow_sparse` | `false` | pack heterogeneous arrays using the key union (lossy) |
| `min_rows` | `8` | shortest array worth packing |
| `min_fill` | `0.5` | minimum populated-cell ratio before packing a sparse array |
| `doc_tag` | `60000` | CBOR tag marking a packed document |
| `table_tag` | `60001` | CBOR tag marking a packed table |

The tag numbers are application-private and **not IANA-registered**; they only
need to match between writer and reader, and are configurable in case they
collide with something else in your pipeline.

### Full Example

This example demonstrates a complete roundtrip conversion - JSON to CBOR and back to JSON:

```yaml
input:
  generate:
    count: 1
    interval: 1ms
    mapping: |
      root = {
        "message": "Hello CBOR World",
        "numbers": [1, 2, 3, 4, 5],
        "nested": {
          "boolean": true,
          "null_value": null
        },
        "m": { "c": 3, "a": 1, "b": 2,}
      }

pipeline:
  processors:
    - cbor:
        operator: from_json  # Convert JSON to CBOR
    - cbor:
        operator: to_json    # Convert CBOR back to JSON

output:
  stdout: {}

logger:
  level: info
```

## Docker

A pre built binary is also availale as a docker image:

```sh
 docker pull ghcr.io/akhenakh/bento-cbor:latest
```

## Build

Clone the repository and build the custom Bento binary:

```bash
git clone https://github.com/akhenakh/bento-cbor.git
cd bento-cbor
go build ./cmd/bento-cbor
```

You can build your own binary, just load the plugin:

```go
package main

import (
	"context"

	"github.com/warpstreamlabs/bento/public/service"

	// Import all standard Benthos components
	_ "github.com/warpstreamlabs/bento/public/components/all"

	_ "github.com/akhenakh/bento-cbor"
)

func main() {
	service.RunCLI(context.Background())
}
```

## Technical Details

The processor uses the [fxamacker/cbor/v2](https://github.com/fxamacker/cbor) library for CBOR encoding and decoding, which provides:

- RFC 7049 and RFC 8949 compliant implementation.
- High performance encoding and decoding (updated to `v2.9.2` for hardened indefinite-length data encoding).
- RFC3339 UTC time encoding with nanosecond precision natively within CBOR (`TimeRFC3339NanoUTC`).
- Support for various CBOR data types.

### Performance Optimizations

- **Lazy JSON Evaluation**: The `to_json` operator sets the decoded CBOR payload natively as Bento's internal structured message (`msg.SetStructured()`). This enables lazy evaluation, skipping expensive JSON byte allocation and serialization steps entirely if the next processor handles mapped structured queries or structural modifications.
- **Precision Safety**: The `from_json` processor guarantees that numbers are not incorrectly interpreted as strings inside CBOR payloads by circumventing `json.Number` fallback behaviors.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the [MIT License](LICENSE).
