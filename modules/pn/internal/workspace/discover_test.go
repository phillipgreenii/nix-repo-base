package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestDiscover_TopoOrderWithInputNames verifies Discover returns repos in
// dependency order (dependencies first, terminal last) with each non-terminal
// repo's resolved inputName attached. The terminal repo has no inputName.
func TestDiscover_TopoOrderWithInputNames(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base", "overlay"} {
		mkRepoDir(t, root, r)
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{ inputs = {}; }")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
input-name = "nb"

[repos.overlay]
url = "github:o/overlay"
input-name = "ovl"
`)

	f := exec.NewFakeRunner()
	evalArgs := func(repo string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", "builtins.attrNames"}
	}
	// overlay depends on base (nb); term depends on base (nb) + overlay (ovl).
	f.AddResponse("nix", evalArgs("base"), exec.Result{Stdout: []byte(`["nixpkgs"]`)}, nil)
	f.AddResponse("nix", evalArgs("overlay"), exec.Result{Stdout: []byte(`["nixpkgs","nb"]`)}, nil)
	f.AddResponse("nix", evalArgs("term"), exec.Result{Stdout: []byte(`["nb","ovl"]`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repos, err := w.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []Repo{
		{Name: "base", URL: "github:o/base", Path: filepath.Join(root, "base"), InputName: "nb"},
		{Name: "overlay", URL: "github:o/overlay", Path: filepath.Join(root, "overlay"), InputName: "ovl"},
		{Name: "term", URL: "github:o/term", Path: filepath.Join(root, "term"), InputName: ""},
	}
	if len(repos) != len(want) {
		t.Fatalf("got %d repos, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repo %d:\n got %+v\nwant %+v", i, repos[i], want[i])
		}
	}
}

// TestDiscover_NoDepsFallsBackAlphabetical: repos with no inter-repo deps come
// back alphabetically, and with no terminal configured every repo keeps an
// inputName (its key by default).
func TestDiscover_NoDepsFallsBackAlphabetical(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	// No repo dirs on disk: deriveDAG finds no declared inputs, so order is
	// alphabetical and no nix eval runs.
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repos, err := w.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].Name != "bar" || repos[0].InputName != "bar" {
		t.Errorf("first repo: got %+v, want bar/bar", repos[0])
	}
	if repos[1].Name != "foo" || repos[1].InputName != "foo" {
		t.Errorf("second repo: got %+v, want foo/foo", repos[1])
	}
}

func TestDiscover_EmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "empty"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repos, err := w.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected empty repo list, got %+v", repos)
	}
}
