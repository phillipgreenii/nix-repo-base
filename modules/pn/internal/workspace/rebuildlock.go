package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Single-flight lock for `pn workspace build` and `pn workspace apply` (bead
// pg2-sdbhh4.11). On 2026-10-26 an apply and a build ran at once with the same
// 423-derivation plan, doubling the work and contending for the same build
// slots. The lock is part of the shared rebuild prelude (Workspace.rebuild), so
// both verbs take it, after the trust gate and before anything is built.
//
// Two kinds of lock file live under ${XDG_STATE_HOME:-$HOME/.local/state}/pn/locks:
//
//   - APPLY key, host-wide: apply-<terminal>-<hostname>.lock. An apply holds it
//     exclusively from ANY workspace root; a build only probes it. A key scoped
//     to (root, terminal) alone would not have caught the incident: a build in a
//     workforest set has a different root from the canonical apply, yet shares
//     the machine and its build slots.
//   - BUILD key, per (workspace root, terminal): build-<sha256(root, terminal)>.lock.
//     A build holds it exclusively, so two builds in one directory (which would
//     rewrite the same ./result) cannot overlap, while builds in different
//     workforest sets do not conflict. An apply ALSO holds its own root's BUILD
//     key for its whole run, as defense in depth for ./result and to de-duplicate
//     identical plans (the user build runs in a private per-run directory, which
//     is the real pin of ./result, pg2-sdbhh4.9).
//
// The locks are real flock(2) locks, so the kernel drops them when pn dies
// (SIGKILL, power loss) and there is no stale-lock cleanup to write. The files
// are opened close-on-exec (os.OpenFile's default) so a child process never
// inherits the open file description and so can never keep a dead pn's lock
// alive. The holder records pid, verb, workspace root, terminal and start time
// in the file as one JSON object, which a contender reads to name the holder.
//
// The lock is released when Workspace.rebuild returns. Post-hooks run in the CLI
// layer, after Build/Apply returns, so they are never run under the lock.
// `pn workspace flake-check` is NOT covered (its concurrency is pg2-sdbhh4.16).

// LockBusyExitCode is the process exit status when another run holds a lock this
// run needs and it did not wait (or its --wait expired). It is 75, sysexits.h
// EX_TEMPFAIL ("temporary failure; the user is invited to retry"), which says
// exactly that and collides with no other status pn chooses: 0 and 1 are
// generic, 2 is usage/doctor, 69 is a missing tool (exec.ToolNotFoundExitCode).
// A wrapped child's own exit code still propagates unchanged, so a child that
// itself exits 75 is indistinguishable by status alone; the message disambiguates.
const LockBusyExitCode = 75

// WaitForever is the Wait value that means "wait for the lock indefinitely"
// (a bare --wait). A Wait of 0 means fail fast.
const WaitForever time.Duration = -1

// ParseWait parses the value of --wait: a positive Go duration ("5m"), "0" or
// "0s" (fail fast, same as no flag), or "forever" (what a bare --wait supplies).
func ParseWait(s string) (time.Duration, error) {
	if s == "forever" {
		return WaitForever, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		if s == "0" {
			return 0, nil
		}
		return 0, fmt.Errorf("invalid --wait value %q: want a duration such as 5m (written --wait=5m), or no value to wait indefinitely", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --wait value %q: a duration must not be negative", s)
	}
	return d, nil
}

// lockPollInterval is how often a waiting run retries a contended lock. A
// variable so tests can shorten it; production never changes it.
var lockPollInterval = 200 * time.Millisecond

// lockHolder is what a lock's holder writes into the lock file.
type lockHolder struct {
	PID      int    `json:"pid"`
	Verb     string `json:"verb"`
	Root     string `json:"root"`
	Terminal string `json:"terminal"`
	Started  string `json:"started"` // RFC3339, UTC
}

// describe renders the holder for a contention message.
func (h lockHolder) describe() string {
	started := h.Started
	if t, err := time.Parse(time.RFC3339Nano, h.Started); err == nil {
		started = fmt.Sprintf("%s, %s ago", t.UTC().Format(time.RFC3339), time.Since(t).Round(time.Second))
	}
	return fmt.Sprintf("pn workspace %s (pid %d, root %s, terminal %s, started %s)", h.Verb, h.PID, h.Root, h.Terminal, started)
}

// LockBusyError reports that a lock this run needs is held by another run. It
// exits with LockBusyExitCode (cli.ExitCode).
type LockBusyError struct {
	// Key is the lock's kind, "apply" or "build".
	Key string
	// Path is the lock file.
	Path string
	// Holder is who holds it; nil when the holder has not (yet) recorded itself.
	Holder *lockHolder
	// Waited is how long this run waited before giving up (0 when it did not wait).
	Waited time.Duration
}

func (e *LockBusyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "pn: another run holds the %s lock", e.Key)
	if e.Holder != nil {
		fmt.Fprintf(&b, ": %s", e.Holder.describe())
	} else {
		b.WriteString(" (the holder has not recorded itself yet)")
	}
	if e.Waited > 0 {
		fmt.Fprintf(&b, "\nwaited %s without getting it", e.Waited.Round(time.Millisecond))
	}
	fmt.Fprintf(&b, "\nlock file: %s", e.Path)
	b.WriteString("\nwait for the holder to finish, or pass --wait=<duration> (for example --wait=5m; the value needs '=') to wait up to that long; a bare --wait waits indefinitely")
	return b.String()
}

// rebuildLockDir is ${XDG_STATE_HOME:-$HOME/.local/state}/pn/locks.
func rebuildLockDir() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", errors.New("cannot place the pn lock files: neither XDG_STATE_HOME nor HOME is set")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "pn", "locks"), nil
}

// lockNameSafe maps a value onto characters safe in a file name.
func lockNameSafe(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}

// applyLockName is the APPLY key's file name: apply-<terminal>-<hostname>.lock.
func applyLockName(terminal, hostname string) string {
	return "apply-" + lockNameSafe(terminal) + "-" + lockNameSafe(hostname) + ".lock"
}

// buildLockName is the BUILD key's file name: build-<sha of root+terminal>.lock.
func buildLockName(root, terminal string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root) + "\x00" + terminal))
	return "build-" + hex.EncodeToString(sum[:])[:16] + ".lock"
}

// lockMode is how a step holds its file.
type lockMode int

const (
	lockExclusive lockMode = iota // held until release; the holder records itself
	lockProbe                     // taken and dropped at once: "is anyone holding this?"
)

// lockStep is one lock a run needs.
type lockStep struct {
	key  string // "apply" or "build", for messages
	path string
	mode lockMode
}

// heldLock is a lock this process holds.
type heldLock struct{ f *os.File }

func (h *heldLock) release() {
	// Empty the record first so a later contender never reads a finished
	// holder's details; the flock itself is dropped by closing the file.
	_ = h.f.Truncate(0)
	_ = syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
	_ = h.f.Close()
}

// tryLock takes path with flock without blocking. busy is true when another
// open file description holds a conflicting lock (an exclusive lock conflicts
// with any other; a probe, which is shared, conflicts only with an exclusive).
func tryLock(path string, mode lockMode) (h *heldLock, busy bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_EX
	if mode == lockProbe {
		how = syscall.LOCK_SH
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("lock %s: %w", path, err)
	}
	return &heldLock{f: f}, false, nil
}

// readHolder reads the holder record of the lock file at path, or nil when it is
// empty or unreadable (a holder records itself just after taking the lock, and a
// probe never does).
func readHolder(path string) *lockHolder {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var h lockHolder
	if json.Unmarshal(data, &h) != nil || h.PID == 0 {
		return nil
	}
	return &h
}

// recordHolder writes this run's details into a lock it holds exclusively.
func recordHolder(h *heldLock, rec lockHolder) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if h.f.Truncate(0) != nil {
		return
	}
	_, _ = h.f.WriteAt(data, 0)
}

// attemptLocks takes every step's lock, in order, or none: on the first busy
// step it drops what it already holds and reports that step. Never holding one
// lock while waiting for another is what keeps a waiting build and a waiting
// apply in the same root from deadlocking each other.
func attemptLocks(steps []lockStep, rec lockHolder) (held []*heldLock, busy *lockStep, err error) {
	drop := func() {
		for _, h := range held {
			h.release()
		}
		held = nil
	}
	for i := range steps {
		h, isBusy, err := tryLock(steps[i].path, steps[i].mode)
		if err != nil {
			drop()
			return nil, nil, err
		}
		if isBusy {
			drop()
			return nil, &steps[i], nil
		}
		if steps[i].mode == lockProbe {
			h.release()
			continue
		}
		recordHolder(h, rec)
		held = append(held, h)
	}
	return held, nil, nil
}

// acquireLocks takes all of steps, waiting up to wait (0: fail fast;
// WaitForever: no limit) for contended ones. The returned release drops them.
// A busy lock after the wait is a *LockBusyError. While waiting, one line naming
// the holder goes to out.
func acquireLocks(ctx context.Context, out io.Writer, steps []lockStep, rec lockHolder, wait time.Duration) (release func(), err error) {
	start := time.Now()
	announced := false
	for {
		held, busy, err := attemptLocks(steps, rec)
		if err != nil {
			return nil, err
		}
		if busy == nil {
			return func() {
				for _, h := range held {
					h.release()
				}
			}, nil
		}
		busyErr := &LockBusyError{Key: busy.key, Path: busy.path, Holder: readHolder(busy.path)}
		elapsed := time.Since(start)
		if wait == 0 || (wait > 0 && elapsed >= wait) {
			if wait != 0 {
				busyErr.Waited = elapsed
			}
			return nil, busyErr
		}
		if !announced {
			announced = true
			limit := "indefinitely"
			if wait > 0 {
				limit = "up to " + wait.String()
			}
			holder := "another run (holder not recorded yet)"
			if busyErr.Holder != nil {
				holder = busyErr.Holder.describe()
			}
			fmt.Fprintf(out, "pn: waiting %s for the %s lock held by %s\n", limit, busy.key, holder)
		}
		sleep := lockPollInterval
		if wait > 0 && wait-elapsed < sleep {
			sleep = wait - elapsed
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// rebuildLockSteps lists, in the order they are taken, the locks verb needs for
// a run of terminal in workspace root (see the file comment for the keys).
//
//	apply: APPLY (exclusive), own-root BUILD (exclusive)
//	build: own-root BUILD (exclusive), APPLY (probe)
func rebuildLockSteps(dir, verb, root, terminal, hostname string) ([]lockStep, error) {
	build := lockStep{key: "build", path: filepath.Join(dir, buildLockName(root, terminal)), mode: lockExclusive}
	apply := lockStep{key: "apply", path: filepath.Join(dir, applyLockName(terminal, hostname)), mode: lockExclusive}
	switch verb {
	case "apply":
		return []lockStep{apply, build}, nil
	case "build":
		apply.mode = lockProbe
		return []lockStep{build, apply}, nil
	}
	return nil, fmt.Errorf("no lock plan for verb %q", verb)
}

// acquireRebuildLock takes the locks verb needs and returns the function that
// releases them. It runs in the shared rebuild prelude (stepAcquireLock).
func (ws *Workspace) acquireRebuildLock(ctx context.Context, out io.Writer, verb, terminal string, wait time.Duration) (release func(), err error) {
	dir, err := rebuildLockDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the pn lock directory: %w", err)
	}
	steps, err := rebuildLockSteps(dir, verb, ws.root, terminal, ws.hostname())
	if err != nil {
		return nil, err
	}
	return acquireLocks(ctx, out, steps, lockHolder{
		PID:      os.Getpid(),
		Verb:     verb,
		Root:     ws.root,
		Terminal: terminal,
		Started:  time.Now().UTC().Format(time.RFC3339Nano),
	}, wait)
}
