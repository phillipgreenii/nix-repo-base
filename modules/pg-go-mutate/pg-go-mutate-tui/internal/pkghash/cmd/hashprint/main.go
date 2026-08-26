// Command hashprint prints the pkghash.Compute digest for a single package
// directory. Its only purpose is to make the Go pkghash algorithm callable
// from bats (via `go run`) so the bash pgm_pkg_hash implementation can be
// cross-checked against it on the same fixture (Task 5, Step 6:
// modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats).
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
