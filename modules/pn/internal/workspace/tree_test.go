package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestRenderTree_DedupAndConnectors checks the pure renderer: connectors,
// indentation, and that a repeated dependency is shown once then marked.
func TestRenderTree_DedupAndConnectors(t *testing.T) {
	var buf bytes.Buffer
	dependsOn := map[string][]string{
		"term":    {"base", "overlay"},
		"overlay": {"base"},
	}
	renderTree(&buf, "term", dependsOn)

	want := "term\n" +
		"├── base\n" +
		"└── overlay\n" +
		"    └── base [↑ shown above]\n"
	if buf.String() != want {
		t.Errorf("renderTree mismatch:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// TestTree_RendersGraphFromDeclaredInputs exercises Tree end-to-end: it derives
// the DAG from each repo's declared flake inputs (not the lock) and renders it.
func TestTree_RendersGraphFromDeclaredInputs(t *testing.T) {
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
	f.AddResponse("nix", evalArgs("base"), exec.Result{Stdout: []byte(`["nixpkgs"]`)}, nil)
	f.AddResponse("nix", evalArgs("overlay"), exec.Result{Stdout: []byte(`["nixpkgs","nb"]`)}, nil)
	f.AddResponse("nix", evalArgs("term"), exec.Result{Stdout: []byte(`["nb","ovl"]`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Tree(context.Background(), &buf, TreeOptions{}); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	want := "term\n" +
		"├── base\n" +
		"└── overlay\n" +
		"    └── base [↑ shown above]\n"
	if buf.String() != want {
		t.Errorf("Tree mismatch:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
