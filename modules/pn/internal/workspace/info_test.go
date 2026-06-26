package workspace

import (
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
