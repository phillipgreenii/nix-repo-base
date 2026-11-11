// Command goembedtestdata is a deliberately BAD fixture for
// checks.go-builders-test-filter-embed: production code embeds a file under
// testdata/, which mkGoApp's test filter removes, so the package build MUST
// fail with the go:embed "no matching files" error. Used only by base's own
// flake checks; never shipped.
package main

import (
	_ "embed"
	"fmt"
	"os"
)

//go:embed testdata/data.txt
var data string

func main() {
	fmt.Fprintln(os.Stdout, data)
}
