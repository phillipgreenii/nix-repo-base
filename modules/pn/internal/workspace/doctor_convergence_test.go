// internal/workspace/doctor_convergence_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestDoctor_ConvergesAfterFix(t *testing.T) {
	root := t.TempDir()

	// Two repos with bare remotes; "term" is the terminal.
	term := filepath.Join(root, "term")
	dep := filepath.Join(root, "dep")
	initRealRepo(t, term)
	setupLocalBareRemote(t, term)
	initRealRepo(t, dep)
	bareDep := setupLocalBareRemote(t, dep)

	// Drift 1: dep is behind its remote (advance remote, reset local).
	addCommit(t, dep, "x.txt", "new", "advance")
	runGitT(t, dep, "push", "-q", "origin", "main")
	runGitT(t, dep, "reset", "-q", "--hard", "HEAD~1")
	// Drift 2: term is on a feature branch.
	runGitT(t, term, "switch", "-q", "-c", "feature")

	// Write a config (no lock.json on disk -> lock-present warning, fixed by --fix).
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"term": {URL: term + ".git", Branch: "main"},
			"dep":  {URL: bareDep, Branch: "main"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := exec.NewRealRunner()

	// First run: expect errors (branch-current term, branch-synced dep).
	r1, err := Doctor(ctx, root, runner, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.HasErrors() {
		t.Fatalf("expected errors before fix; got %+v", r1.Findings)
	}

	// Fix.
	if _, err := Doctor(ctx, root, runner, DoctorOptions{Fix: true}); err != nil {
		t.Fatal(err)
	}

	// Second run: branch + sync errors resolved.
	r2, err := Doctor(ctx, root, runner, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r2.Findings {
		if f.Severity == SevError && !f.Skipped &&
			(f.CheckID == "branch-current" || f.CheckID == "branch-synced") {
			t.Fatalf("residual error after fix: %s (%s) %s", f.CheckID, f.Repo, f.Message)
		}
	}
}
