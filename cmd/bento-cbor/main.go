// Example build to include bento-cbor with all bento components
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
