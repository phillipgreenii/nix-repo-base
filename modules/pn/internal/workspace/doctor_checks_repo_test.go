// internal/workspace/doctor_checks_repo_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckRepos_MissingNonTerminalIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{
				"term": {URL: "u", Branch: "main"},
				"dep":  {URL: "u2", Branch: "main"},
			},
		},
	}
	initRealRepo(t, filepath.Join(root, "term")) // term present, dep missing
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-present", "dep", SevWarning) {
		t.Fatalf("missing non-terminal dep should be warning: %+v", fs)
	}
}

// Item 5: Clone clones ALL missing repos in one call, so attaching a Clone fix
// to every missing-repo finding runs Clone once per finding (redundant, though
// idempotent). checkRepos must attach the actual Clone fix to only ONE
// repos-present finding; the others stay Fixable (so they still render/plan as
// fixable and clear via the residual re-run) but carry no fix closure.
func TestCheckRepos_MissingReposShareOneCloneFix(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{
				"term": {URL: "u", Branch: "main"}, // present
				"a":    {URL: "ua", Branch: "main"},
				"b":    {URL: "ub", Branch: "main"},
			},
		},
	}
	initRealRepo(t, filepath.Join(root, "term")) // a and b are missing
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term"}
	fs := ws.checkRepos(context.Background(), env)

	var withFix, present int
	for i := range fs {
		if fs[i].CheckID != "repos-present" {
			continue
		}
		present++
		if !fs[i].Fixable {
			t.Fatalf("every repos-present finding should stay Fixable: %+v", fs[i])
		}
		if fs[i].fix != nil {
			withFix++
		}
	}
	if present != 2 {
		t.Fatalf("expected 2 missing-repo findings, got %d: %+v", present, fs)
	}
	if withFix != 1 {
		t.Fatalf("expected exactly ONE repos-present finding to carry the Clone fix, got %d", withFix)
	}
}

func TestCheckRepos_MissingTerminalIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos:     map[string]RepoConfig{"term": {URL: "u", Branch: "main"}},
		},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-present", "term", SevError) {
		t.Fatalf("missing terminal should be error: %+v", fs)
	}
}

func TestCheckRepos_PresentNotGitIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}},
	}
	if err := os.MkdirAll(filepath.Join(root, "dep"), 0o755); err != nil { // dir, no .git
		t.Fatal(err)
	}
	env := &doctorEnv{ws: ws, mode: "primary"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repo-is-git", "dep", SevError) {
		t.Fatalf("present-not-git should be error: %+v", fs)
	}
}

func TestCheckRepos_ExtraIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}},
	}
	initRealRepo(t, filepath.Join(root, "stray"))
	env := &doctorEnv{ws: ws, mode: "primary"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-extra", "stray", SevWarning) {
		t.Fatalf("extra repo should be warning: %+v", fs)
	}
}

func TestCheckRepos_IdentityMismatchIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	// origin disagrees (different slug) with the configured url -> identity mismatch
	runGitT(t, dir, "remote", "add", "origin", "git@github.com:o/actual.git")
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"dep": {URL: "git@github.com:o/configured.git", Branch: "main"},
		}},
	}
	env := &doctorEnv{ws: ws, mode: "primary"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repo-identity", "dep", SevError) {
		t.Fatalf("origin/url mismatch should be repo-identity error: %+v", fs)
	}
}

func hasFindingForRepo(fs []Finding, id, repo string, sev Severity) bool {
	for _, f := range fs {
		if f.CheckID == id && f.Repo == repo && f.Severity == sev {
			return true
		}
	}
	return false
}
