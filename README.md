# Bento CBOR Processor

A [Bento](https://warpstreamlabs.github.io/bento/) processor plugin for working with CBOR (Concise Binary Object Representation) data. This processor allows you to convert between JSON and CBOR formats.

## Overview

CBOR is a binary data format designed for small message size with the ability to support the seamless conversion of JSON data models. This processor is RFC 7049 and RFC 8949 compliant and provides encoding and decoding capabilities for CBOR data.

## Installation

### Using Pre-built Binary

Download a pre-built binary from the releases page.

### Building from Source

Clone the repository and build the custom Bento binary:

```bash
git clone https://github.com/akhenakh/bento-cbor.git
cd bento-cbor
go build ./cmd/bento-cbor
```

## Usage

The CBOR processor supports two operations:

1. `to_json`: Converts CBOR data into JSON format
2. `from_json`: Converts JSON data into CBOR format

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

## Build
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

The processor uses the [fxamacker/cbor](https://github.com/fxamacker/cbor) library for CBOR encoding and decoding, which provides:

- RFC 7049 and RFC 8949 compliant implementation
- High performance encoding and decoding
- Support for various CBOR data types

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the [MIT License](LICENSE).
