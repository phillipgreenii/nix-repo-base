package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// errRelockNoSpace is returned (without a retry) when nix reports the disk is
// full: a second attempt cannot succeed and only delays the failure.
var errRelockNoSpace = errors.New("nix flake update: no space left on device")

// errRelockRestore is wrapped with its cause when the relock could not put
// flake.lock back after a failure. It is a repo failure in every scope, never
// downgraded to a warning, because the repo is left dirty.
var errRelockRestore = errors.New("relock: could not restore flake.lock")

// writeLockFile is the seam the lock restore writes through, so a test can make
// the write-back fail.
var writeLockFile = os.WriteFile

const (
	lockStructureName = "(lock structure)"
	relockSubject     = "chore(deps): nix flake update"
	fsmonitorSocket   = "fsmonitor--daemon.ipc"
)

// inputChange is one root flake input whose locked revision moved.
type inputChange struct{ Name, Old, New string }

type relockNode struct {
	Inputs map[string]json.RawMessage `json:"inputs"`
	Locked map[string]any             `json:"locked"`
}

type relockDoc struct {
	Root  string                `json:"root"`
	Nodes map[string]relockNode `json:"nodes"`
}

func parseRelockDoc(data []byte, which string) (*relockDoc, error) {
	var d relockDoc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse %s flake.lock: %w", which, err)
	}
	if d.Root == "" {
		d.Root = "root"
	}
	return &d, nil
}

// rootInputs maps each root input to its node key. A follows input (the value
// is an array) is left out.
func (d *relockDoc) rootInputs() map[string]string {
	out := map[string]string{}
	for name, raw := range d.Nodes[d.Root].Inputs {
		if key, ok := asString(raw); ok {
			out[name] = key
		}
	}
	return out
}

// lockedOf returns the node's locked object without lastModified.
func (d *relockDoc) lockedOf(input, key string) (map[string]any, error) {
	node, ok := d.Nodes[key]
	if !ok {
		return nil, fmt.Errorf("flake.lock root input %q points at node %q, which is missing from nodes", input, key)
	}
	locked := map[string]any{}
	for k, v := range node.Locked {
		if k != "lastModified" {
			locked[k] = v
		}
	}
	return locked, nil
}

// describeLocked renders a locked object as a short rev, else the head of its
// narHash, else "(no rev)".
func describeLocked(locked map[string]any) string {
	if rev, _ := locked["rev"].(string); rev != "" {
		return shortRev(rev)
	}
	if nar, _ := locked["narHash"].(string); nar != "" {
		nar = strings.TrimPrefix(nar, "sha256-")
		if len(nar) > 12 {
			nar = nar[:12]
		}
		return nar
	}
	return "(no rev)"
}

// stripLastModified removes every lastModified key, at any depth.
func stripLastModified(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "lastModified" {
				delete(x, k)
				continue
			}
			x[k] = stripLastModified(e)
		}
	case []any:
		for i, e := range x {
			x[i] = stripLastModified(e)
		}
	}
	return v
}

// diffFlakeLocks lists the root inputs whose locked object (lastModified
// aside) differs between two flake.lock files, in name order. Follows inputs
// are skipped. When no root input differs but the files still differ once every
// lastModified is removed, it returns the single "(lock structure)" change, so
// a real structural change is never reverted as churn.
func diffFlakeLocks(before, after []byte) ([]inputChange, error) {
	b, err := parseRelockDoc(before, "old")
	if err != nil {
		return nil, err
	}
	a, err := parseRelockDoc(after, "new")
	if err != nil {
		return nil, err
	}
	bIn, aIn := b.rootInputs(), a.rootInputs()
	names := map[string]bool{}
	for n := range bIn {
		names[n] = true
	}
	for n := range aIn {
		names[n] = true
	}
	var changes []inputChange
	for name := range names {
		bKey, inB := bIn[name]
		aKey, inA := aIn[name]
		var bl, al map[string]any
		if inB {
			if bl, err = b.lockedOf(name, bKey); err != nil {
				return nil, err
			}
		}
		if inA {
			if al, err = a.lockedOf(name, aKey); err != nil {
				return nil, err
			}
		}
		switch {
		case inB && inA:
			if !reflect.DeepEqual(bl, al) {
				changes = append(changes, inputChange{name, describeLocked(bl), describeLocked(al)})
			}
		case inA:
			changes = append(changes, inputChange{name, "(new)", describeLocked(al)})
		default:
			changes = append(changes, inputChange{name, describeLocked(bl), "(removed)"})
		}
	}
	if len(changes) == 0 {
		var bv, av any
		if err := json.Unmarshal(before, &bv); err != nil {
			return nil, fmt.Errorf("parse old flake.lock: %w", err)
		}
		if err := json.Unmarshal(after, &av); err != nil {
			return nil, fmt.Errorf("parse new flake.lock: %w", err)
		}
		if !reflect.DeepEqual(stripLastModified(bv), stripLastModified(av)) {
			return []inputChange{{Name: lockStructureName}}, nil
		}
		return nil, nil
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes, nil
}

// flakeUpdateCommitMessage builds the commit message for a relock: a fixed
// subject, a blank line, then one line per change in name order.
func flakeUpdateCommitMessage(changes []inputChange) string {
	sorted := append([]inputChange(nil), changes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	msg := relockSubject + "\n"
	for _, c := range sorted {
		if c.Name == lockStructureName && c.Old == "" && c.New == "" {
			msg += "\n" + lockStructureName + " changed"
			continue
		}
		msg += fmt.Sprintf("\n%s %s -> %s", c.Name, c.Old, c.New)
	}
	return msg
}

// gitConfigDisableFsmonitor returns the environment that makes git clients
// started by nix ignore core.fsmonitor: one more GIT_CONFIG_KEY/VALUE pair after
// any pairs already in the environment (a non-numeric count counts as 0).
func gitConfigDisableFsmonitor() map[string]string {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GIT_CONFIG_COUNT")))
	if err != nil || n < 0 {
		n = 0
	}
	return map[string]string{
		fmt.Sprintf("GIT_CONFIG_KEY_%d", n):   "core.fsmonitor",
		fmt.Sprintf("GIT_CONFIG_VALUE_%d", n): "false",
		"GIT_CONFIG_COUNT":                    strconv.Itoa(n + 1),
	}
}

// stopFsmonitor makes nix safe from git's fsmonitor: it stops a running daemon
// when core.fsmonitor is true and removes a stale daemon socket, because nix
// fails with "unsupported type" on one. Best-effort, as in the shell library:
// every error is ignored.
func (ws *Workspace) stopFsmonitor(ctx context.Context, dir string) {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "config", "--type=bool", "--get", "core.fsmonitor"}, exec.RunOptions{})
	if err == nil && strings.TrimSpace(string(res.Stdout)) == "true" {
		_, _ = ws.runner.Run(ctx, "git", []string{"-C", dir, "fsmonitor--daemon", "stop"}, exec.RunOptions{})
	}
	res, err = ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "--absolute-git-dir"}, exec.RunOptions{})
	if err != nil {
		return
	}
	if gitDir := strings.TrimSpace(string(res.Stdout)); gitDir != "" {
		_ = os.Remove(filepath.Join(gitDir, fsmonitorSocket))
	}
}

// nixOutOfSpace reports whether a failed nix call ran out of disk.
func nixOutOfSpace(res exec.Result, err error) bool {
	const marker = "No space left on device"
	if bytes.Contains(res.Stderr, []byte(marker)) {
		return true
	}
	var ce *exec.CommandError
	return errors.As(err, &ce) && bytes.Contains(ce.Result.Stderr, []byte(marker))
}

// relockAllInputs runs `nix flake update --refresh` over every input of the
// flake in dir and commits the result with pn's own commit path. flakeRel is
// the flake.nix FILE path (resolveFlakePath); flake.lock lives beside it.
//
// It returns the number of root inputs that moved (0 for every no-op path: no
// lock, nix wrote nothing, only lastModified churn). Once nix has run, every
// error path puts the original flake.lock bytes back first, so a failure never
// leaves a dirty tree (a write-back that fails returns errRelockRestore). A
// cancelled context returns the context error itself.
func (ws *Workspace) relockAllInputs(ctx context.Context, out io.Writer, name, dir, flakeRel string) (changed int, err error) {
	flakeDirRel := filepath.Dir(flakeRel)
	flakeDir := filepath.Join(dir, flakeDirRel)
	lockRel := filepath.Join(flakeDirRel, "flake.lock")
	lockPath := filepath.Join(dir, lockRel)

	before, rerr := os.ReadFile(lockPath)
	if os.IsNotExist(rerr) {
		fmt.Fprintf(out, "    [%s] no flake.lock — skipping (pn does not bootstrap a lock)\n", name)
		return 0, nil
	}
	if rerr != nil {
		return 0, fmt.Errorf("read %s: %w", lockRel, rerr)
	}

	// restore puts the original bytes back; cause is the error being returned.
	restore := func(cause error) error {
		if werr := writeLockFile(lockPath, before, 0o644); werr != nil {
			if cause == nil {
				return fmt.Errorf("%w: %v", errRelockRestore, werr)
			}
			return fmt.Errorf("%w: %v (while handling: %w)", errRelockRestore, werr, cause)
		}
		return cause
	}

	ws.stopFsmonitor(ctx, dir)

	args := []string{"flake", "update", "--refresh"}
	opts := exec.RunOptions{Dir: flakeDir, Env: gitConfigDisableFsmonitor(), Stdout: out, Stderr: out, WrapNix: true}
	var runErr error
	for attempt := 1; attempt <= 2; attempt++ {
		var res exec.Result
		res, runErr = ws.runner.Run(ctx, "nix", args, opts)
		if runErr == nil {
			break
		}
		if nixOutOfSpace(res, runErr) {
			return 0, restore(fmt.Errorf("%w: %v", errRelockNoSpace, runErr))
		}
		if ctx.Err() != nil {
			return 0, restore(ctx.Err())
		}
		if attempt == 1 {
			fmt.Fprintf(out, "    [%s] relock all flake inputs: attempt 1 failed, retrying once\n", name)
		}
	}
	if runErr != nil {
		return 0, restore(fmt.Errorf("nix flake update --refresh: %w", runErr))
	}

	after, rerr := os.ReadFile(lockPath)
	if rerr != nil {
		return 0, restore(fmt.Errorf("read %s: %w", lockRel, rerr))
	}
	if bytes.Equal(before, after) {
		return 0, nil
	}
	changes, derr := diffFlakeLocks(before, after)
	if derr != nil {
		return 0, restore(derr)
	}
	if len(changes) == 0 {
		// Only lastModified/formatting churn: put the original back.
		return 0, restore(nil)
	}

	client, oerr := openGitMutator(ctx, dir)
	if oerr != nil {
		return 0, restore(fmt.Errorf("git add %s: %w", lockRel, oerr))
	}
	if aerr := client.Add(ctx, lockRel); aerr != nil {
		// Add may have partially staged the lock: reset, then write back.
		return 0, restore(restoreAfterFailedCommit(ctx, client, lockRel, fmt.Errorf("git add %s: %w", lockRel, aerr)))
	}
	h, cerr := client.Commit(ctx, flakeUpdateCommitMessage(changes))
	if cerr != nil {
		return 0, restoreAfterFailedCommit(ctx, client, lockRel, fmt.Errorf("git commit: %w", cerr))
	}
	h.AttachStream(out, out)
	if werr := h.Wait(); werr != nil {
		return 0, restoreAfterFailedCommit(ctx, client, lockRel, fmt.Errorf("git commit: %w", werr))
	}

	names := make([]string, len(changes))
	for i, c := range changes {
		names[i] = c.Name
	}
	fmt.Fprintf(out, "  → %s: nix flake update moved %d input(s): %s\n", name, len(changes), strings.Join(names, ", "))
	return len(changes), nil
}
