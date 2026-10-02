// Package resolve finds CMD on PATH while skipping the wrapper itself (W-3),
// so a later PATH entry that points back at the wrapper cannot recurse.
package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NotFoundError carries the message Go's exec would produce: the LookPath
// wording for a bare name, the fork/exec wording for a name with a slash.
// Callers exit 127 for it.
type NotFoundError struct {
	Name  string
	Slash bool
}

func (e *NotFoundError) Error() string {
	if e.Slash {
		return fmt.Sprintf("fork/exec %s: no such file or directory", e.Name)
	}
	return fmt.Sprintf("exec: %q: executable file not found in $PATH", e.Name)
}

// PermissionError means CMD exists but cannot be executed (exit 126).
type PermissionError struct{ Path string }

func (e *PermissionError) Error() string {
	return fmt.Sprintf("fork/exec %s: permission denied", e.Path)
}

// LookPath resolves name like exec.LookPath, except that any candidate that is
// the same file as self (inode compare, symlinks followed) is skipped. A name
// containing a slash is used as given. self may be nil.
func LookPath(name, pathEnv string, self os.FileInfo) (string, error) {
	if strings.Contains(name, "/") {
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", err
		}
		if err := usable(abs, name); err != nil {
			return "", err
		}
		return abs, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, name)
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		if self != nil && os.SameFile(fi, self) {
			continue
		}
		return filepath.Abs(cand)
	}
	return "", &NotFoundError{Name: name}
}

func usable(path, name string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return &NotFoundError{Name: name, Slash: true}
	}
	if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return &PermissionError{Path: name}
	}
	return nil
}
