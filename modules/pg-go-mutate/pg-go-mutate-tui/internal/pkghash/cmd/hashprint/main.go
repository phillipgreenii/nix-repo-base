// Command hashprint prints the pkghash.Compute digest for a single package
// directory. Its only purpose is to make the Go pkghash algorithm callable
// (via `go run`) so the bash pgm_pkg_hash implementation can be cross-checked
// against it on the same fixture -- now the top-level nix check
// `checks.<system>.pg-go-mutate-lib-pkghash-cross-check` (flake.nix; moved
// there from a bats test by bead pg2-nwtf2, since this module is not present
// in pg-go-mutate-lib's own Pattern-A check sandbox).
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hashprint <pkg-dir>")
		os.Exit(2)
	}

	hash, err := pkghash.Compute(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println(hash)
}
