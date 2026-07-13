package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestInfo_JoinsConfigAndAppliedState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "ws1"
terminal = "r"

[repos.r]
url = "github:owner/r"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repoPath := filepath.Join(root, "r")
	if err := writeAppliedState(repoPath, AppliedState{AppliedRef: "abc123", Dirty: false}); err != nil {
		t.Fatal(err)
	}
	info, err := w.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Wsid != "ws1" || info.Root == "" {
		t.Fatalf("wsid/root: %+v", info)
	}
	if info.Terminal != "r" {
		t.Fatalf("terminal: %+v", info)
	}
	if len(info.Repos) != 1 || info.Repos[0].Path != repoPath || info.Repos[0].AppliedRef != "abc123" {
		t.Fatalf("repos: %+v", info.Repos)
	}
}

// TestInfo_FindsOverridePathAppliedState is the regression test for pg2-k43p.3.
// An override-path apply (coordinated-worktree flow) applies the terminal repo
// from an alternate checkout (`OverridePaths`). The applied-state store MUST be
// keyed by the canonical workspace path (`<root>/<name>`), the same key `Info`
// reads — so `pn workspace info` finds the applied_ref recorded by that apply
// rather than reporting the empty string (the mis-gate this bead fixes).
func TestInfo_FindsOverridePathAppliedState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "ws1"
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_repo_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`)
	// The override checkout — a stand-in coordinated-worktree of "leaf" at a
	// path OTHER than the canonical <root>/leaf.
	overrideDir := t.TempDir()

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", overrideDir + "#" + shortHostname(),
	}, exec.Result{}, nil)
	// markApplied runs git against the OVERRIDE dir (the applied checkout).
	f.AddResponse("git", []string{"-C", overrideDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("feedface\n")}, nil)
	f.AddResponse("git", []string{"-C", overrideDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{
		Force:         true,
		OverridePaths: map[string]string{"leaf": overrideDir},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := w.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if len(info.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %+v", info.Repos)
	}
	if got := info.Repos[0].AppliedRef; got != "feedface" {
		t.Fatalf("info must report the override-applied ref keyed by the canonical "+
			"path; got applied_ref=%q want %q (repo %+v)", got, "feedface", info.Repos[0])
	}
}

func TestInfo_NoNixEval(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "ws1"
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Info(context.Background()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			t.Fatalf("info must not invoke nix eval; saw %v", c.Args)
		}
	}
}
