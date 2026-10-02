// Command moda is a Pattern-B fixture: it imports sibling module
// example.com/modb through a local `replace => ../modb`, so building, linting,
// or testing it forces buildGoApplication to cd into this module's subdir
// (modRoot = "moda") with the replaced sibling resolved alongside. It also
// imports one real third-party dependency (github.com/spf13/pflag), so the
// committed gomod2nix.toml carries a `cachePackages` list that mixes a
// local-replace root (example.com/modb, which the Go cache env must drop) with a
// real one (which it must actually compile into the cache). Used only by base's
// Go-builder fixture checks (mkGoApp / mkGoLint / mkGoTest and the cache-env
// checks); not shipped.
package main

import (
	"fmt"
	"os"

	"example.com/modb"
	"github.com/spf13/pflag"
)

// greeting wraps modb.Greeting so moda has an importable, testable symbol
// independent of the uncallable main().
func greeting() string {
	return modb.Greeting()
}

func main() {
	// A real call into the third-party dependency, so the import is not elided.
	fs := pflag.NewFlagSet("moda", pflag.ContinueOnError)
	_ = fs.Parse(os.Args[1:])
	// fmt.Fprintln is in .golangci.yml's errcheck exclusions (matching base's
	// own convention), so leaving its write unchecked is intentionally allowed.
	fmt.Fprintln(os.Stdout, greeting())
}
