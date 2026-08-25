// Package pkghash computes a deterministic content hash for a Go package
// directory. It is the single place pg-go-mutate-tui hashes a package's
// contents; Task 8's queue and Task 9's worker both call Compute rather than
// reimplementing hashing themselves.
package pkghash

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Compute returns a deterministic content hash over every *.go file
// directly inside pkgDir (non-recursive: a Go package is one directory).
// Sorted filename order is load-bearing -- filesystem listing order is not
// guaranteed stable across platforms or runs. MUST match pg-go-mutate's
// bash pgm_pkg_hash exactly (Task 2's cross-check fixture, Step 6 below).
func Compute(pkgDir string) (string, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			return "", err
		}
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
