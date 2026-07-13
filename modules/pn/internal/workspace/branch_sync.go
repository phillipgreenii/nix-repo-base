package workspace

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// switchToDefaultBranch checks out branch in repoDir. It refuses to switch a
// dirty working tree (tracked changes) so no local work is silently shelved.
func (ws *Workspace) switchToDefaultBranch(ctx context.Context, repoDir, branch string) error {
	dirty, err := ws.isDirty(ctx, repoDir)
	if err != nil {
		return fmt.Errorf("refusing to switch %s: %w", repoDir, err)
	}
	if dirty {
		return fmt.Errorf("refusing to switch %s: working tree is dirty", repoDir)
	}
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "switch", branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git switch %s in %s: %w", branch, repoDir, err)
	}
	return nil
}

// fastForwardIfBehind fast-forwards branch to <remote>/<branch>. It uses
// --ff-only so a non-fast-forward (diverged/ahead) is an error, never a merge.
// Callers must have fetched <remote>/<branch> first (doctor's refRev resolution
// does, using the same resolved remote). remote is the resolved canonical push
// remote (see resolvePushRemote), not a hardcoded "origin".
func (ws *Workspace) fastForwardIfBehind(ctx context.Context, repoDir, remote, branch string) error {
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "merge", "--ff-only", remote + "/" + branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git merge --ff-only %s/%s in %s: %w", remote, branch, repoDir, err)
	}
	return nil
}

// pushBranch fast-forward-pushes branch to remote (no force). It fixes the
// strictly-ahead case (local ahead of remote, behind 0): publishing the local
// commits makes <remote>/<branch> match local HEAD. git rejects a non-fast-forward
// push, so a remote that has moved (diverged) surfaces as an error rather than a
// forced overwrite — destructive resolution stays a manual decision. remote is
// the resolved canonical push remote (see resolvePushRemote), not a hardcoded
// "origin".
func (ws *Workspace) pushBranch(ctx context.Context, repoDir, remote, branch string) error {
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "push", remote, branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git push %s %s in %s: %w", remote, branch, repoDir, err)
	}
	return nil
}
