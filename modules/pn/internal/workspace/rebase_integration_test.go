//go:build integration

// Heavy real-git tests moved out of the unit gate: each builds real git
// fixtures. They run in integration-checks.pn-workspace-integration-tests.

package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestRebase_RealGit_ConflictingDivergedRebaseIsRestored drives real git: a
// genuinely conflicting ahead-1/behind-1 branch. Rebase must fail, and the
// repo must be restored (same branch tip, clean tree, no rebase state dir).
func TestRebase_RealGit_ConflictingDivergedRebaseIsRestored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")
	initRealRepo(t, repoDir)
	bare := setupLocalBareRemoteNamed(t, repoDir, "origin")

	// A peer pushes a conflicting change to README.md.
	peer := filepath.Join(t.TempDir(), "peer")
	runGitT(t, root, "clone", "-q", bare, peer)
	runGitT(t, peer, "config", "user.email", "p@p")
	runGitT(t, peer, "config", "user.name", "p")
	addCommit(t, peer, "README.md", "peer\n", "peer change")
	runGitT(t, peer, "push", "-q", "origin", "main")

	// Local diverges with a conflicting change to the same file.
	tip := addCommit(t, repoDir, "README.md", "local\n", "local change")

	w, err := Open(root, exec.NewRealRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected the conflicting rebase to fail")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("expected a rolled-back error; got: %v", err)
	}
	if got := headRev(t, repoDir); got != tip {
		t.Errorf("branch tip not restored: want %s, got %s", tip, got)
	}
	if st := runGitT(t, repoDir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean after rollback:\n%s", st)
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		p := runGitT(t, repoDir, "rev-parse", "--git-path", d)
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoDir, p)
		}
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("%s still present after rollback", d)
		}
	}
}
