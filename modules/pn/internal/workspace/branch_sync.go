package workspace

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// switchToDefaultBranch checks out branch in repoDir. It refuses to switch a
// dirty working tree (tracked changes) so no local work is silently shelved.
func (ws *Workspace) switchToDefaultBranch(ctx context.Context, repoDir, branch string) error {
	if ws.isDirty(ctx, repoDir) {
		return fmt.Errorf("refusing to switch %s: working tree is dirty", repoDir)
	}
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "switch", branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git switch %s in %s: %w", branch, repoDir, err)
	}
	return nil
}

// fastForwardIfBehind fast-forwards branch to origin/<branch>. It uses
// --ff-only so a non-fast-forward (diverged/ahead) is an error, never a merge.
// Callers must have fetched first (doctor's refRev resolution does).
func (ws *Workspace) fastForwardIfBehind(ctx context.Context, repoDir, branch string) error {
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "merge", "--ff-only", "origin/" + branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git merge --ff-only origin/%s in %s: %w", branch, repoDir, err)
	}
	return nil
}

// pushBranch fast-forward-pushes branch to origin (no force). It fixes the
// strictly-ahead case (local ahead of remote, behind 0): publishing the local
// commits makes origin/<branch> match local HEAD. git rejects a non-fast-forward
// push, so a remote that has moved (diverged) surfaces as an error rather than a
// forced overwrite — destructive resolution stays a manual decision.
func (ws *Workspace) pushBranch(ctx context.Context, repoDir, branch string) error {
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "push", "origin", branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git push origin %s in %s: %w", branch, repoDir, err)
	}
	return nil
}
