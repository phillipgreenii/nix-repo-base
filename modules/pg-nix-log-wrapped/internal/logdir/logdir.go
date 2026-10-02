// Package logdir owns the raw nix JSONL log files: choosing the directory
// (W-4), creating a uniquely named file safely, and the retention sweep
// (W-10).
package logdir

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// RootDir is where a root wrapper (euid 0) ALWAYS logs. It is never a user
	// directory: a root process must not write into user-owned paths.
	RootDir = "/var/log/pg-nix-log-wrapped"
	// DefaultMaxAge is the retention window (operator ruling D3).
	DefaultMaxAge = 7 * 24 * time.Hour
	// DefaultTotalCap bounds the whole directory (2 GiB).
	DefaultTotalCap int64 = 2 << 30

	rootMode os.FileMode = 0o750
	userMode os.FileMode = 0o700
	suffix               = ".jsonl"
)

// Params are the inputs to Select.
type Params struct {
	Euid int
	// Flag is --log-dir; ignored for a root wrapper.
	Flag string
	// XDGState is $XDG_STATE_HOME; Home is the user's home directory.
	XDGState string
	Home     string
	// RootDir overrides the root-mode directory (tests only).
	RootDir string
}

// Select chooses the log directory. For euid 0 it is always the root-owned
// directory, whatever the flag or env says. Otherwise: --log-dir, then
// $XDG_STATE_HOME/pn/nix-logs, then $HOME/.local/state/pn/nix-logs.
func Select(p Params) (string, error) {
	if p.Euid == 0 {
		if p.RootDir != "" {
			return p.RootDir, nil
		}
		return RootDir, nil
	}
	switch {
	case p.Flag != "":
		return filepath.Abs(p.Flag)
	case filepath.IsAbs(p.XDGState):
		return filepath.Join(p.XDGState, "pn", "nix-logs"), nil
	case p.Home != "":
		return filepath.Join(p.Home, ".local", "state", "pn", "nix-logs"), nil
	}
	return "", errors.New("no log directory: set --log-dir, XDG_STATE_HOME or HOME")
}

// Prepare creates dir if needed. As root it additionally insists the
// directory is a real (non-symlink) directory owned by the effective uid and
// not writable by group or other, so a pre-planted directory cannot redirect
// root's writes.
func Prepare(dir string, euid int) error {
	mode := userMode
	if euid == 0 {
		mode = rootMode
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if euid != 0 {
		if _, err := os.Stat(dir); err != nil {
			return err
		}
		return nil
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a plain directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != euid {
		return fmt.Errorf("%s is not owned by uid %d", dir, euid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable", dir)
	}
	return nil
}

// Create makes a new log file named <utc-ts>-<pid>-<rand>.jsonl, mode 0600,
// opened O_CREAT|O_EXCL|O_NOFOLLOW so an existing file or symlink is never
// reused. It returns the open handle and the absolute path.
func Create(dir string, now time.Time, pid int) (*os.File, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	for range 4 {
		var r [4]byte
		if _, err := rand.Read(r[:]); err != nil {
			return nil, "", err
		}
		name := fmt.Sprintf("%s-%d-%s%s", now.UTC().Format("20060102T150405Z"), pid, hex.EncodeToString(r[:]), suffix)
		path := filepath.Join(abs, name)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			return f, path, nil
		}
		lastErr = err
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	return nil, "", lastErr
}

// Sweep enforces retention inside dir ONLY: it removes regular *.jsonl files
// older than maxAge (by mtime), then removes oldest-first until the total is
// at or under capBytes. Symlinks, directories and other names are never
// touched. It returns how many files were removed. Errors are ignored: the
// sweep is best-effort and must never block a run.
func Sweep(dir string, now time.Time, maxAge time.Duration, capBytes int64) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type file struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []file
	var total int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), suffix) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		files = append(files, file{filepath.Join(dir, e.Name()), fi.ModTime(), fi.Size()})
		total += fi.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	removed := 0
	cut := 0
	for cut < len(files) && now.Sub(files[cut].mtime) > maxAge {
		if os.Remove(files[cut].path) == nil {
			removed++
		}
		total -= files[cut].size
		cut++
	}
	for cut < len(files) && total > capBytes {
		if os.Remove(files[cut].path) == nil {
			removed++
		}
		total -= files[cut].size
		cut++
	}
	return removed
}
