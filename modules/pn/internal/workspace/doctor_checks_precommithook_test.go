// internal/workspace/doctor_checks_precommithook_test.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// The bundle-mode audit (stubs, pointer, unreachable hooks) is covered in
// doctor_checks_hookbundle_test.go. These tests pin what checkPreCommitHookLive
// itself decides: which repos get audited, and that an old
// .pre-commit-config.yaml is not a bundle (R4: no legacy state).

// TestCheckPreCommitHookLive_OldConfigOnlyIsNoBundleError: a DECLARED repo whose
// clone holds only an old .pre-commit-config.yaml (no pointer, no stubs) is
// reported as having no hook bundle, as a hard error, not as a Skipped legacy
// finding.
func TestCheckPreCommitHookLive_OldConfigOnlyIsNoBundleError(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
	hbWantError(t, hbRunLive(ws), "no hook bundle", "(cd "+dir+" && nix run .#install-pre-commit-hooks)")
}

// TestCheckPreCommitHookLive_UndeclaredRepoIsNotAudited: a repo whose
// pn-workspace.toml entry does not run install-pre-commit-hooks has not opted in
// to having its hooks installed, so nothing is asserted about it, whatever it
// holds on disk.
func TestCheckPreCommitHookLive_UndeclaredRepoIsNotAudited(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, false)
	writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
	if fs := hbRunLive(ws); len(fs) != 0 {
		t.Fatalf("an undeclared repo must produce no finding; got %+v", fs)
	}
}

// TestCheckPreCommitHookLive_SkipsDirectoryThatIsNotAGitRepo: a configured repo
// that is not a git work tree is checkRepos' business, not this check's.
func TestCheckPreCommitHookLive_SkipsDirectoryThatIsNotAGitRepo(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "repo"))
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"repo": {
			URL: "git@github.com:o/repo.git", Branch: "main",
			Hooks: []EventHook{{When: []string{"post-clone"}, Run: []string{"{nix_run install-pre-commit-hooks}"}}},
		}}},
	}
	if fs := ws.checkPreCommitHookLive(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("a directory that is not a git work tree must produce no finding; got %+v", fs)
	}
}

// TestCheckPreCommitHookLive_FindingsAreNeverSkipped: every bundle finding is a
// hard error that fails the exit code.
func TestCheckPreCommitHookLive_FindingsAreNeverSkipped(t *testing.T) {
	ws, _ := hbDoctorWorkspace(t, true)
	fs := hbRunLive(ws)
	if len(fs) == 0 {
		t.Fatal("a declared repo with no bundle must produce a finding")
	}
	for _, f := range fs {
		if f.Skipped || !strings.HasPrefix(f.Message, "pg-hooks: ") {
			t.Errorf("want a non-skipped pg-hooks finding; got %+v", f)
		}
	}
	if !(&DoctorReport{Findings: fs}).HasErrors() {
		t.Error("a no-bundle finding must make the report erroneous")
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
