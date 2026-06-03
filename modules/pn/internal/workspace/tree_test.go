package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestColorEnabled covers the non-terminal cases (the only ones unit-testable
// without a PTY): plain output for non-file writers, regular files, and when
// NO_COLOR is set. The char-device "true" path is a standard OS check.
func TestColorEnabled(t *testing.T) {
	if colorEnabled(&bytes.Buffer{}) {
		t.Error("colorEnabled should be false for a non-*os.File writer (pipe/buffer)")
	}

	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if colorEnabled(f) {
		t.Error("colorEnabled should be false for a regular file (not a char device)")
	}

	t.Setenv("NO_COLOR", "1")
	if colorEnabled(f) {
		t.Error("colorEnabled should be false when NO_COLOR is set")
	}
}

// TestRenderTree_NoColor_AsteriskOnDuplicate: without color, a repeated
// dependency is printed the same but prefixed with "*".
func TestRenderTree_NoColor_AsteriskOnDuplicate(t *testing.T) {
	var buf bytes.Buffer
	dependsOn := map[string][]string{
		"term":    {"base", "overlay"},
		"overlay": {"base"},
	}
	renderTree(&buf, "term", dependsOn, false)

	want := "term\n" +
		"├── base\n" +
		"└── overlay\n" +
		"    └── *base\n"
	if buf.String() != want {
		t.Errorf("renderTree(no color) mismatch:\n got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// TestRenderTree_Color_DimOnDuplicate: with color, the first listing is
// untouched and a repeat is dimmed (ANSI 2m … 0m).
func TestRenderTree_Color_DimOnDuplicate(t *testing.T) {
	var buf bytes.Buffer
	dependsOn := map[string][]string{
		"term":    {"base", "overlay"},
		"overlay": {"base"},
	}
	renderTree(&buf, "term", dependsOn, true)

	want := "term\n" +
		"├── base\n" +
		"└── overlay\n" +
		"    └── \x1b[2mbase\x1b[0m\n"
	if buf.String() != want {
		t.Errorf("renderTree(color) mismatch:\n got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// TestTree_RendersGraphFromDeclaredInputs exercises Tree end-to-end: it derives
// the DAG from each repo's declared flake inputs (not the lock) and renders it.
// Output goes to a bytes.Buffer (not a TTY), so the no-color "*" form is used.
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
		"    └── *base\n"
	if buf.String() != want {
		t.Errorf("Tree mismatch:\n got:\n%q\nwant:\n%q", buf.String(), want)
	}
}
