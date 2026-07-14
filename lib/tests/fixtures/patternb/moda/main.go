// Command moda is a Pattern-B fixture: it imports sibling module
// example.com/modb through a local `replace => ../modb`, so building, linting,
// or testing it forces buildGoApplication to cd into this module's subdir
// (modRoot = "moda") with the replaced sibling resolved alongside. Used only by
// base's Go-builder fixture checks (mkGoApp / mkGoLint / mkGoTest); not shipped.
package main

import (
	"fmt"
	"os"

	"example.com/modb"
)

// greeting wraps modb.Greeting so moda has an importable, testable symbol
// independent of the uncallable main().
func greeting() string {
	return modb.Greeting()
}

func main() {
	// fmt.Fprintln is in .golangci.yml's errcheck exclusions (matching base's
	// own convention), so leaving its write unchecked is intentionally allowed.
	fmt.Fprintln(os.Stdout, greeting())
}
