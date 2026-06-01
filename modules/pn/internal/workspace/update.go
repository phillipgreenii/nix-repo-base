package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ulLibResolverRef is the flake app that prints the update-locks lib dir. It
// mirrors the one-liner each update-locks.sh uses; pn resolves it once per run
// and injects the result so the per-repo scripts skip the (remote) evaluation.
const ulLibResolverRef = "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir"

// UpdateOptions configures Update.
type UpdateOptions struct {
	// Recreate forces full lock recreation (currently treated as an
	// indicator for Upgrade; see upgrade.go).
	Recreate bool
	// ULLibDir, when set, is exported as UL_LIB_DIR to each update-locks.sh so
	// it skips its own determine-ul-lib-dir resolution. Resolve it once per run
	// via ResolveULLibDir. Empty leaves each script to resolve for itself.
	ULLibDir string
}

// ResolveULLibDir runs the update-locks lib resolver once and returns the path
// it prints (with WORKSPACE_ROOT set so its on-disk sibling tier can fire).
// Best-effort: any failure returns "" so callers fall back to the per-repo
// resolution baked into each update-locks.sh.
func (ws *Workspace) ResolveULLibDir(ctx context.Context) string {
	res, err := ws.runner.Run(ctx, "nix", []string{"run", ulLibResolverRef},
		exec.RunOptions{Env: map[string]string{"WORKSPACE_ROOT": ws.root}})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// ulSubprocessEnv builds the env for an update-locks.sh invocation: the
// workspace-root markers plus UL_LIB_DIR when one was resolved.
func (ws *Workspace) ulSubprocessEnv(ulLibDir string) map[string]string {
	env := map[string]string{
		"PN_WORKSPACE_ROOT": ws.root,
		"WORKSPACE_ROOT":    ws.root,
	}
	if ulLibDir != "" {
		env["UL_LIB_DIR"] = ulLibDir
	}
	return env
}

// Update pulls each workspace repo, runs its ./update-locks.sh, and pushes.
// Repos without an upstream skip pull/push but still attempt update-locks.
// Repos with a dirty working tree are skipped (non-fatal).
//
// Per-repo failures are aggregated rather than aborting the whole sweep on the
// first error: every repo is attempted and the failing repos are named in the
// returned error at the end (like FlakeCheck). Within a single repo, a failed
// pull marks it failed and skips update-locks and push (the working tree is
// suspect); a failed update-locks still lets push run, since pull succeeded.
//
// After processing, pn-workspace.revs.json is rewritten with each
// successfully-processed repo's current HEAD revision and canonical URL.
// Repos that were skipped (dirty) or failed mid-step retain their previous
// rev-lock entry if any.
//
// The provided context is checked between repos and between sub-steps; a
// cancelled context aborts cleanly with the next ctx.Err() observed.
func (ws *Workspace) Update(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	names := ws.updateOrder()
	// Start from the existing rev-lock so untouched repos keep their entries.
	revs := make(map[string]LockedRepo, len(names))
	if ws.revLock != nil {
		for k, v := range ws.revLock.Repos {
			revs[k] = v
		}
	}

	var failed []string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		repoDir := filepath.Join(ws.root, name)

		// Skip (non-fatal) if the working tree is dirty (modified or staged).
		if ws.isDirty(ctx, repoDir) {
			fmt.Fprintf(out, "  ⊘ skipping %s — working tree has uncommitted changes\n", name)
			continue
		}

		fmt.Fprintf(out, "  --== update %s ==--  \n", name)
		hasUp := ws.hasUpstream(ctx, repoDir)
		pullFailed := false
		projectFailed := false

		if hasUp {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				pullFailed = true
				projectFailed = true
			}
		}
		// Skip update-locks if pull failed: the working tree is suspect.
		if !pullFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir, Env: ws.ulSubprocessEnv(opts.ULLibDir), Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
				// Keep going to push whatever update-locks committed.
			}
		}
		// Push only when pull succeeded (even on partial update-locks failure).
		if hasUp && !pullFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
			}
		}

		if projectFailed {
			failed = append(failed, name)
		} else {
			// Capture the new HEAD rev for the rev-lock.
			rev, err := captureHead(ctx, ws.runner, repoDir)
			if err == nil && rev != "" {
				revs[name] = LockedRepo{
					URL: canonicalURL(ws.config.Repos[name]),
					Rev: rev,
				}
			}
		}
	}

	if err := WriteRevLock(filepath.Join(ws.root, RevLockFileName), &RevLock{Repos: revs}); err != nil {
		return fmt.Errorf("write rev lock: %w", err)
	}

	if len(failed) > 0 {
		return fmt.Errorf("update failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// updateOrder returns the repo iteration order for Update: the lock's
// topological order (dependencies first, terminal last) when the lock covers
// exactly the configured repo set, so a downstream repo re-locks against its
// already-updated upstream. Falls back to alphabetical when the lock is empty
// or stale (doesn't match the configured repos), which is always safe.
func (ws *Workspace) updateOrder() []string {
	alpha := orderedRepoNames(ws.config.Repos)
	order := ws.lock.Order
	if len(order) != len(alpha) {
		return alpha
	}
	inLock := make(map[string]bool, len(order))
	for _, n := range order {
		inLock[n] = true
	}
	for _, n := range alpha {
		if !inLock[n] {
			return alpha
		}
	}
	return order
}

// isDirty reports whether repoDir has uncommitted changes — modified or staged
// (untracked files are allowed). Probes are ordered so a dirty modified tree
// short-circuits before the staged check.
func (ws *Workspace) isDirty(ctx context.Context, repoDir string) bool {
	if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "diff", "--quiet"}, exec.RunOptions{}); err != nil {
		return true
	}
	if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "diff", "--cached", "--quiet"}, exec.RunOptions{}); err != nil {
		return true
	}
	return false
}

// captureHead returns the trimmed SHA of HEAD for repoDir.
func captureHead(ctx context.Context, runner exec.Runner, repoDir string) (string, error) {
	res, err := runner.Run(ctx, "git",
		[]string{"-C", repoDir, "rev-parse", "HEAD"},
		exec.RunOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}
