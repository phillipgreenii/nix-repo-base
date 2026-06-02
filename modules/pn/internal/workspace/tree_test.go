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

// TestTree_RendersGraphFromTerminalFlakeLock exercises Tree end-to-end: it
// derives the DAG from the terminal flake.lock and renders the hierarchy.
func TestTree_RendersGraphFromTerminalFlakeLock(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	mkRepoDir(t, root, "base")
	mkRepoDir(t, root, "overlay")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:owner/term"

[repos.base]
url = "github:owner/base"
input-name = "nb"

[repos.overlay]
url = "github:owner/overlay"
input-name = "ovl"
`)
	writeFile(t, filepath.Join(root, "term", "flake.lock"), `{
	  "nodes": {
	    "root": {"inputs": {"nb": "nb", "ovl": "ovl"}},
	    "nb": {"inputs": {}},
	    "ovl": {"inputs": {"nb": ["nb"]}}
	  }
	}`)

	w, err := Open(root, exec.NewFakeRunner())
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
