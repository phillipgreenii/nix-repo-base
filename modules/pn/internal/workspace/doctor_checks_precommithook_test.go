// internal/workspace/doctor_checks_precommithook_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// preCommitHookWorkspace builds a one-repo workspace whose repo is a real git
// repo (so isGitRepo passes, and .git/hooks exists to hold a hook file).
func preCommitHookWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apps")
	initRealRepo(t, dir)
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"apps": {URL: "git@github.com:o/apps.git", Branch: "main"},
		}},
	}
	return ws, dir
}

func hookPathFor(repoDir string) string {
	return filepath.Join(repoDir, ".git", "hooks", "pre-commit")
}

// TestCheckPreCommitHookLive_LiveHookIsClean covers the ordinary steady state
// this check must NOT flag: .pre-commit-config.yaml declared, and
// .git/hooks/pre-commit present and executable — the shape prek actually
// generates (a plain script, not a symlink).
func TestCheckPreCommitHookLive_LiveHookIsClean(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), "generated config")
	if err := os.WriteFile(hookPathFor(dir), []byte("#!/bin/sh\nexec prek hook-impl\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a present, executable hook must produce no finding; got %+v", fs)
	}
}

// TestCheckPreCommitHookLive_MissingHookIsSkippedFinding is the exact
// regression bd tc-0wzp/tc-771m record: 5 of 8 workspace repos silently lost
// their .git/hooks/pre-commit and commits stopped firing it, with nothing to
// notice. Per GAP 3's severity convention (bd tc-atsmj), the finding is
// Skipped: true — fully visible, never a hard failure.
func TestCheckPreCommitHookLive_MissingHookIsSkippedFinding(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), "generated config")
	// .git/hooks/pre-commit deliberately not created.

	fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "pre-commit-hook-live", "apps") {
		t.Fatalf("expected a Skipped pre-commit-hook-live finding for a missing hook; got %+v", fs)
	}
	f := findingByID(t, fs, "pre-commit-hook-live")
	if !contains([]byte(f.Message), "missing") {
		t.Errorf("message should say the hook is missing: %q", f.Message)
	}
	if !contains([]byte(f.Manual), "install-pre-commit-hooks") {
		t.Errorf("manual hint must name the regeneration command; got %q", f.Manual)
	}
}

// TestCheckPreCommitHookLive_DanglingSymlinkIsSkippedFinding covers the
// dangling-symlink failure mode named explicitly in bd tc-wdwnl / tc-0wzp: the
// hook resolves into a /nix/store path that has since been garbage-collected.
// Detection reuses symlinkLiveInNixStore (nix_hooks.go), the same primitive
// preCommitConfigLive applies to the generated config.
func TestCheckPreCommitHookLive_DanglingSymlinkIsSkippedFinding(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), "generated config")
	dangling := "/nix/store/deadbeefdeadbeefdeadbeefdeadbeef-does-not-exist/pre-commit"
	if err := os.Symlink(dangling, hookPathFor(dir)); err != nil {
		t.Fatal(err)
	}

	fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "pre-commit-hook-live", "apps") {
		t.Fatalf("expected a Skipped pre-commit-hook-live finding for a dangling symlink hook; got %+v", fs)
	}
	f := findingByID(t, fs, "pre-commit-hook-live")
	if !contains([]byte(f.Message), "dangling") {
		t.Errorf("message should call out the dangling symlink: %q", f.Message)
	}
}

// TestCheckPreCommitHookLive_NotExecutableIsSkippedFinding covers the third
// documented failure mode (bd tc-wdwnl): present, not a dangling reference,
// but missing its executable bit — git silently declines to run a
// non-executable hook.
func TestCheckPreCommitHookLive_NotExecutableIsSkippedFinding(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), "generated config")
	writeFile(t, hookPathFor(dir), "#!/bin/sh\nexec prek hook-impl\n") // writeFile uses 0o644, not executable

	fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "pre-commit-hook-live", "apps") {
		t.Fatalf("expected a Skipped pre-commit-hook-live finding for a non-executable hook; got %+v", fs)
	}
	f := findingByID(t, fs, "pre-commit-hook-live")
	if !contains([]byte(f.Message), "not executable") {
		t.Errorf("message should call out the missing executable bit: %q", f.Message)
	}
}

// TestCheckPreCommitHookLive_NoConfigDeclaredDoesNotApply is the negative
// space: a repo that never declared .pre-commit-config.yaml has opted out of
// pre-commit entirely, so the check must produce nothing regardless of
// .git/hooks/pre-commit's state (even absent, which would otherwise look
// exactly like the missing-hook regression).
func TestCheckPreCommitHookLive_NoConfigDeclaredDoesNotApply(t *testing.T) {
	ws, _ := preCommitHookWorkspace(t)
	// preCommitConfigName deliberately never written.

	fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a repo declaring no %s must produce no finding; got %+v", preCommitConfigName, fs)
	}
}

// hasSkippedFinding reports whether fs contains a Skipped: true finding for
// (id, repo) — the severity convention GAP 3 (bd tc-atsmj) established for
// this shape of problem: fully visible, never a hard failure.
func hasSkippedFinding(t *testing.T, fs []Finding, id, repo string) bool {
	t.Helper()
	for _, f := range fs {
		if f.CheckID == id && f.Repo == repo {
			if !f.Skipped {
				t.Errorf("finding %s/%s must be Skipped: true per the tc-atsmj GAP 3 convention; got %+v", id, repo, f)
			}
			return true
		}
	}
	return false
}
