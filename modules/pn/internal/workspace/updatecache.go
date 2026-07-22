package workspace

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// hexFilenameRe matches exactly 64 lowercase hex characters — the form
// produced by appliedStateFile (sha256 of the full repo path).
var hexFilenameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// needsRebuild reports whether apply must rebuild. Returns true if force is set,
// any repo's working tree is dirty, any repo's HEAD differs from the recorded
// applied hash, or any repo has no recorded hash (absent store triggers rebuild).
// A corrupt or unreadable store returns an error (fail-closed) rather than
// triggering a rebuild. Returns false (with a notice) only when every repo is
// clean and unchanged.
func (ws *Workspace) needsRebuild(ctx context.Context, repoDirs []repoDir, force bool, out io.Writer) (bool, error) {
	if force {
		return true, nil
	}
	for _, rd := range repoDirs {
		porcelain, err := ws.gitStatusPorcelain(ctx, rd.gitDir)
		if err != nil {
			return false, fmt.Errorf("git status in %s: %w", rd.gitDir, err)
		}
		if porcelain != "" {
			return true, nil
		}
		res, err := ws.runner.Run(ctx, "git", []string{"-C", rd.gitDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git rev-parse in %s: %w", rd.gitDir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		// Key the store by the canonical path so the rebuild-skip check reads
		// the same entry markApplied wrote and Info reads (shared key rule).
		st, ok, err := readAppliedState(rd.keyPath)
		if err != nil {
			return false, fmt.Errorf("read applied-state for %s: %w", rd.keyPath, err)
		}
		if !ok || head != st.AppliedRef {
			return true, nil
		}
	}
	fmt.Fprintln(out, "Skipping rebuild: all workspace repos clean and unchanged since last apply")
	return false, nil
}

// gitStatusProbeTimeout bounds apply's `git status` dirtiness probes. Disabling
// the filesystem monitor (see gitStatusPorcelain) already removes the wedged
// `git fsmonitor--daemon` hang that motivated bead pg2-0sa8p; this timeout is
// defense-in-depth against any OTHER stall (e.g. a slow or contended index),
// turning an unbounded hang into a clear, actionable error. It is deliberately
// generous: an fsmonitor-off `git status` on these repos completes well under a
// second, so this leaves large headroom and will not false-trip on a cold cache
// or a loaded machine.
const gitStatusProbeTimeout = 60 * time.Second

// gitStatusPorcelain runs `git status --porcelain` in dir for apply's dirtiness
// probes (needsRebuild, markApplied) and returns the trimmed output. It disables
// the filesystem monitor PER-INVOCATION (`-c core.fsmonitor=false`) so the probe
// never queries a `git fsmonitor--daemon` — a wedged daemon would otherwise block
// the status read forever on `.git/fsmonitor--daemon.ipc` and hang apply
// (bead pg2-0sa8p). Per-command config is used deliberately over the update-locks
// approach of writing core.fsmonitor=false into shared .git/config with an
// EXIT-trap restore: it mutates no shared state (safe under concurrent worktrees,
// per ADR 0009) and cannot leave fsmonitor disabled if apply dies mid-run. A
// bounded context guards against non-fsmonitor stalls (gitStatusProbeTimeout).
func (ws *Workspace) gitStatusPorcelain(ctx context.Context, dir string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, gitStatusProbeTimeout)
	defer cancel()
	res, err := ws.runner.Run(tctx, "git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.RunOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// markApplied records each repo's current HEAD (and dirty flag) into the
// authoritative applied-state store. Written only after a successful apply.
// git reads HEAD/dirtiness from the applied checkout (gitDir), but the store is
// keyed by the canonical path (keyPath) — the same key Info reads — so an
// override-path apply is discoverable by `pn workspace info`.
func (ws *Workspace) markApplied(ctx context.Context, repoDirs []repoDir) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rd := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", rd.gitDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git rev-parse in %s: %w", rd.gitDir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		porcelain, err := ws.gitStatusPorcelain(ctx, rd.gitDir)
		if err != nil {
			return fmt.Errorf("git status in %s: %w", rd.gitDir, err)
		}
		dirty := porcelain != ""
		if err := writeAppliedState(rd.keyPath, AppliedState{AppliedRef: head, Dirty: dirty, AppliedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// checkNixDaemon probes daemon responsiveness with a 10s-bounded `nix eval`. On
// failure it returns an actionable error (the interactive restart prompt from
// the bash version is intentionally omitted).
func (ws *Workspace) checkNixDaemon(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ws.runner.Run(tctx, "nix", []string{"eval", "--expr", "true"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("nix daemon health check failed: %w\n  Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon", err)
	}
	return nil
}
