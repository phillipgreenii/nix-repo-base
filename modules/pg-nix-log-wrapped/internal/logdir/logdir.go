// Package logdir owns the nix JSONL log files: choosing the directory (W-4),
// creating a uniquely named scratch/retained pair safely, and the retention
// sweep (W-10).
//
// Each run owns two files with one stem: <stem>.raw, the scratch file nix
// itself writes (json-log-path), and <stem>.jsonl, the filtered retained log
// (package retain). A normal exit deletes the .raw once the .jsonl is
// complete; the sweep finalizes the .raw of a run that died without doing so.
package logdir

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/retain"
)

const (
	// RootDir is where a root wrapper (euid 0) ALWAYS logs. It is never a user
	// directory: a root process must not write into user-owned paths.
	RootDir = "/var/log/pg-nix-log-wrapped"
	// DefaultMaxAge is the retention window (operator ruling D3).
	DefaultMaxAge = 7 * 24 * time.Hour
	// DefaultTotalCap bounds the whole directory (2 GiB).
	DefaultTotalCap int64 = 2 << 30

	rootMode  os.FileMode = 0o750
	userMode  os.FileMode = 0o700
	suffix                = ".jsonl"
	rawSuffix             = ".raw"
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

// Files is the pair of files one run owns.
type Files struct {
	// Raw is the scratch file nix writes through json-log-path (read and
	// tailed by the wrapper); Out is the retained log the wrapper writes.
	Raw, Out         *os.File
	RawPath, OutPath string
}

// Close closes both handles.
func (f *Files) Close() {
	_ = f.Raw.Close()
	_ = f.Out.Close()
}

// Remove closes and deletes both files.
func (f *Files) Remove() {
	f.Close()
	_ = os.Remove(f.RawPath)
	_ = os.Remove(f.OutPath)
}

// Create makes the pair <utc-ts>-<pid>-<rand>.raw and .jsonl, mode 0600, each
// opened O_CREAT|O_EXCL|O_NOFOLLOW so an existing file or symlink is never
// reused. The .raw is pre-created exactly as nix's log file always was.
func Create(dir string, now time.Time, pid int) (*Files, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	const flags = os.O_RDWR | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW
	var lastErr error
	for range 4 {
		var r [4]byte
		if _, err := rand.Read(r[:]); err != nil {
			return nil, err
		}
		stem := filepath.Join(abs, fmt.Sprintf("%s-%d-%s", now.UTC().Format("20060102T150405Z"), pid, hex.EncodeToString(r[:])))
		raw, err := os.OpenFile(stem+rawSuffix, flags, 0o600)
		if err != nil {
			lastErr = err
			if !errors.Is(err, os.ErrExist) {
				break
			}
			continue
		}
		out, err := os.OpenFile(stem+suffix, flags, 0o600)
		if err != nil {
			_ = raw.Close()
			_ = os.Remove(stem + rawSuffix)
			lastErr = err
			if !errors.Is(err, os.ErrExist) {
				break
			}
			continue
		}
		return &Files{Raw: raw, Out: out, RawPath: stem + rawSuffix, OutPath: stem + suffix}, nil
	}
	return nil, lastErr
}

// SweepOptions tune SweepWith; the zero value is what Sweep uses.
type SweepOptions struct {
	// PidAlive reports whether a process exists; nil selects a kill(pid, 0)
	// probe (EPERM counts as alive).
	PidAlive func(pid int) bool
	// RetainedCap caps each finalized .jsonl; <= 0 selects retain.DefaultCap.
	RetainedCap int64
}

// Sweep is SweepWith with default options.
func Sweep(dir string, now time.Time, maxAge time.Duration, capBytes int64) int {
	return SweepWith(dir, now, maxAge, capBytes, SweepOptions{})
}

// SweepWith enforces retention inside dir ONLY. In order:
//
//  1. Orphan scratch files. A *.raw whose pid (part of its name) is no longer
//     alive is FINALIZED: filtered into its .jsonl with the retained rule and
//     cap (retain.Finalize), then removed. It is never deleted unfiltered: it
//     may be the only evidence of a SIGKILLed run. A *.raw whose pid is alive
//     is left alone, except that one older than maxAge is finalized
//     regardless (a reused pid must not keep it forever). mtime alone never
//     decides otherwise. A .raw whose name has no parseable pid is treated as
//     alive until it is older than maxAge.
//  2. Retention. It removes regular *.jsonl files older than maxAge (by
//     mtime), then removes oldest-first until the total is at or under
//     capBytes. The total counts the remaining (live) *.raw files too, but a
//     live .raw is never evicted, and neither is the .jsonl of a live run
//     (same stem): only finalized .jsonl files are evicted.
//
// Symlinks, directories and other names are never touched. It returns how
// many .jsonl files were evicted in step 2 (finalizations are not counted).
// Errors are ignored: the sweep is best-effort and must never block a run.
func SweepWith(dir string, now time.Time, maxAge time.Duration, capBytes int64, o SweepOptions) int {
	alive := o.PidAlive
	if alive == nil {
		alive = processAlive
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), rawSuffix) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		pid, ok := pidOf(e.Name())
		old := now.Sub(fi.ModTime()) > maxAge
		if ok && alive(pid) && !old {
			continue
		}
		if !ok && !old {
			continue
		}
		finalize(filepath.Join(dir, e.Name()), fi.ModTime(), o.RetainedCap)
	}

	entries, err = os.ReadDir(dir)
	if err != nil {
		return 0
	}
	type file struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []file
	live := map[string]bool{} // stems of runs still holding a .raw
	var total int64
	for _, e := range entries {
		n := e.Name()
		isRaw := strings.HasSuffix(n, rawSuffix)
		if (!isRaw && !strings.HasSuffix(n, suffix)) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		total += fi.Size()
		if isRaw {
			live[strings.TrimSuffix(n, rawSuffix)] = true
			continue
		}
		files = append(files, file{filepath.Join(dir, n), fi.ModTime(), fi.Size()})
	}
	evictable := files[:0]
	for _, f := range files {
		if !live[strings.TrimSuffix(filepath.Base(f.path), suffix)] {
			evictable = append(evictable, f)
		}
	}
	files = evictable
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

// finalize filters rawPath into its sibling .jsonl, stamps that file with the
// raw file's mtime (so age retention judges the run, not the sweep), and only
// then removes the raw. On any error the raw is kept for the next sweep.
func finalize(rawPath string, mtime time.Time, retainedCap int64) {
	out := strings.TrimSuffix(rawPath, rawSuffix) + suffix
	if err := retain.Finalize(rawPath, out, retainedCap); err != nil {
		return
	}
	_ = os.Chtimes(out, mtime, mtime)
	_ = os.Remove(rawPath)
}

// pidOf extracts the pid from <ts>-<pid>-<rand>.raw.
func pidOf(name string) (int, bool) {
	parts := strings.Split(strings.TrimSuffix(name, rawSuffix), "-")
	if len(parts) != 3 {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[1])
	return pid, err == nil && pid > 0
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
