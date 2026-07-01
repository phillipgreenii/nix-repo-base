package workspace

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ulLibResolverRef is the flake app that prints the update-locks lib dir. It
// mirrors the one-liner each update-locks.sh uses; pn resolves it once per run
// and injects the result so the per-repo scripts skip the (remote) evaluation.
const ulLibResolverRef = "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir"

// UpdateOptions configures Update.
type UpdateOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
	// Recreate forces full lock recreation (currently treated as an
	// indicator for Upgrade; see upgrade.go).
	Recreate bool
	// InPlace selects the legacy direct-on-main flow (pull → update-locks →
	// push in each primary checkout). When false (the default), Update isolates
	// each repo in an ephemeral worktree and fast-forwards back to main.
	InPlace bool
	// SiblingsOnly relocks ONLY the phillipgreenii-* workspace-sibling flake
	// inputs (the propagateWorkspaceEdges pass) and SKIPS each repo's
	// update-locks.sh, so nixpkgs and other third-party inputs are left
	// untouched. Everything else — topological order, worktree isolation vs
	// InPlace, pull/rebase, push (so a consumer picks up a sibling's
	// freshly-pushed tip), and the revs.json rewrite — is unchanged. It is
	// orthogonal to InPlace (composes with either flow). Upgrade never sets it;
	// Recreate (an Upgrade-only marker) and SiblingsOnly are not combined.
	SiblingsOnly bool
	// ULLibDir, when set, is exported as UL_LIB_DIR to each update-locks.sh so
	// it skips its own determine-ul-lib-dir resolution. Resolve it once per run
	// via ResolveULLibDir. Empty leaves each script to resolve for itself.
	ULLibDir string
	// Log, when non-nil, receives a structured JSONL event stream for the run
	// (run_start / project_result / run_end). Nil disables event logging.
	Log *eventlog.Writer
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

// Update runs the workspace update. By default each repo is updated in an
// ephemeral git worktree and fast-forwarded back onto the primary main
// (updateViaWorktree); opts.InPlace selects the legacy direct-on-main flow
// (updateInPlace). See ADR 0009.
func (ws *Workspace) Update(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if opts.InPlace {
		return ws.updateInPlace(ctx, out, opts)
	}
	return ws.updateViaWorktree(ctx, out, opts)
}

// updateInPlace pulls each workspace repo, runs its ./update-locks.sh, and pushes.
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
//
// Repos are processed in topological order (dependencies before consumers) so
// that downstream repos re-lock against already-updated upstreams.
// updateInPlace is a required-terminal command: it errors when no terminal is configured.
func (ws *Workspace) updateInPlace(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if _, err := ws.requireTerminal(ctx, opts.Terminal); err != nil {
		return err
	}
	names := ws.topoAlpha(ctx)
	// Derive the lock once for workspace-edge propagation; effectiveLock (not the
	// possibly-empty ws.lock) is the source topoAlpha trusts (C3).
	edgeLock, _, _ := ws.effectiveLock(ctx)
	_ = opts.Log.Emit("info", "run_start", "workspace update started", map[string]any{
		"terminal": opts.Terminal,
		"projects": len(names),
	})
	// Start from the existing rev-lock so untouched repos keep their entries.
	revs := make(map[string]LockedRepo, len(names))
	if ws.revLock != nil {
		maps.Copy(revs, ws.revLock.Repos)
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
			_ = opts.Log.Emit("warn", "project_result", "project skipped (dirty working tree)",
				map[string]any{"name": name, "outcome": "skipped"})
			continue
		}

		fmt.Fprintf(out, "  --== update %s ==--  \n", name)
		hasUp := ws.hasUpstream(ctx, repoDir)
		pullFailed := false
		propagateFailed := false
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
		// Propagate workspace-edge inputs (ungated) before update-locks. Skip if
		// pull failed: the working tree is suspect.
		if !pullFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if err := ws.propagateWorkspaceEdges(ctx, out, name, repoDir, ws.resolveFlakePath(name), workspaceAliasesFromLock(edgeLock, name)); err != nil {
				fmt.Fprintf(out, "  ✗ %s: propagate-edges failed: %v\n", name, err)
				propagateFailed = true
				projectFailed = true
			}
		}
		// Run update-locks (when present) only if pull and propagation succeeded:
		// a propagation error may have left a dirty tree. A repo without
		// ./update-locks.sh is skipped (not failed) — propagation already
		// maintained its workspace locks.
		if !pullFailed && !propagateFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			switch {
			case opts.SiblingsOnly:
				fmt.Fprintf(out, "  ⊘ %s: --siblings-only — skipping update-locks.sh (workspace inputs relocked, nixpkgs/third-party untouched)\n", name)
			case fileExists(filepath.Join(repoDir, "update-locks.sh")):
				if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir, Env: ws.ulSubprocessEnv(opts.ULLibDir), Stdout: out, Stderr: out}); err != nil {
					projectFailed = true
					// Keep going to push whatever update-locks committed.
				}
			default:
				fmt.Fprintf(out, "  ⊘ %s: no update-locks.sh — skipping (workspace inputs already propagated)\n", name)
			}
		}
		// Push only when pull and propagation succeeded (even on partial
		// update-locks failure).
		if hasUp && !pullFailed && !propagateFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
			}
		}

		if projectFailed {
			failed = append(failed, name)
			step := "push"
			if pullFailed {
				step = "pull"
			} else if propagateFailed {
				step = "propagate-edges"
			}
			_ = opts.Log.Emit("error", "project_result", "project failed", map[string]any{
				"name": name, "outcome": "failed", "failed_step": step,
			})
		} else {
			_ = opts.Log.Emit("info", "project_result", "project updated", map[string]any{
				"name": name, "outcome": "ok",
			})
			// Capture the new HEAD rev for the rev-lock.
			rev, err := captureHead(ctx, ws.runner, repoDir)
			if err == nil && rev != "" {
				revs[name] = LockedRepo{
					URL: displayURL(ws.config.Repos[name]),
					Rev: rev,
				}
			}
		}
	}

	if err := WriteRevLock(filepath.Join(ws.root, RevLockFileName), &RevLock{Repos: revs}); err != nil {
		return fmt.Errorf("write rev lock: %w", err)
	}

	if len(failed) > 0 {
		_ = opts.Log.Emit("error", "run_end", "workspace update finished with failures",
			map[string]any{"status": "failed", "failed": len(failed), "failed_projects": failed})
		return fmt.Errorf("update failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	_ = opts.Log.Emit("info", "run_end", "workspace update finished", map[string]any{"status": "ok", "failed": 0})
	return nil
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
