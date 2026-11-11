// Command gotestfilter is a dependency-free Pattern-A fixture for
// checks.go-builders-test-filter: it ships _test.go files and testdata/
// directories (at the module root and nested) so the check can prove mkGoApp
// excludes them from the source digest and the build input unless
// doCheck = true. Used only by base's own flake checks; never shipped.
package main

import (
	"fmt"
	"os"

	"example.com/gotestfilter/pkg"
)

func main() {
	fmt.Fprintln(os.Stdout, pkg.Name())
}
