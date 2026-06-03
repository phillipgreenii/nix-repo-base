package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// UpdateOptions configures Update.
type UpdateOptions struct {
	// Recreate forces full lock recreation (currently treated as an
	// indicator for Upgrade; see upgrade.go).
	Recreate bool
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
func (ws *Workspace) Update(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	var failed []string
	for _, name := range names {
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
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				pullFailed = true
				projectFailed = true
			}
		}
		// Skip update-locks if pull failed: the working tree is suspect.
		if !pullFailed {
			if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
				// Keep going to push whatever update-locks committed.
			}
		}
		// Push only when pull succeeded (even on partial update-locks failure).
		if hasUp && !pullFailed {
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
			}
		}

		if projectFailed {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("update failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
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
