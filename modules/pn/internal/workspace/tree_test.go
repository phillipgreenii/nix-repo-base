package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const allInputsLock = `{
  "nodes": {
    "root": {"inputs": {"nb": "nb", "ovl": "ovl", "nixpkgs": "nixpkgs"}},
    "nb": {"inputs": {"nixpkgs": "nixpkgs"}},
    "ovl": {"inputs": {"nixpkgs": "nixpkgs", "nb": ["nb"]}},
    "nixpkgs": {"inputs": {"flake-utils": "flake-utils"}},
    "flake-utils": {}
  },
  "root": "root",
  "version": 7
}`

// TestBuildAllInputsGraph: the full flake.lock node graph is translated into
// display-name space — workspace inputNames become their repo basenames, the
// root becomes the terminal basename, everything else keeps its lock key. A
// single-element follow ([X]) is a direct dep; multi-element follows are skipped.
func TestBuildAllInputsGraph(t *testing.T) {
	wsDisplay := map[string]string{"nb": "base", "ovl": "overlay"}
	root, dependsOn, err := buildAllInputsGraph([]byte(allInputsLock), "term", wsDisplay)
	if err != nil {
		t.Fatalf("buildAllInputsGraph: %v", err)
	}
	if root != "term" {
		t.Errorf("root = %q, want term", root)
	}
	want := map[string][]string{
		"term":    {"base", "nixpkgs", "overlay"},
		"base":    {"nixpkgs"},
		"overlay": {"base", "nixpkgs"},
		"nixpkgs": {"flake-utils"},
	}
	if !reflect.DeepEqual(dependsOn, want) {
		t.Errorf("dependsOn mismatch:\n got %#v\nwant %#v", dependsOn, want)
	}
}

// TestTree_AllInputs renders the full input graph from the terminal flake.lock,
// including external inputs (nixpkgs, flake-utils) that the default
// workspace-internal view omits. Output to a buffer uses the no-color "*" form.
func TestTree_AllInputs(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base", "overlay"} {
		mkRepoDir(t, root, r)
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
	// Terminal flake.lock present on disk: Tree reads it directly, no nix calls.
	writeFile(t, filepath.Join(root, "term", "flake.lock"), allInputsLock)

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf bytes.Buffer
	if err := w.Tree(context.Background(), &buf, TreeOptions{AllInputs: true}); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	want := "term\n" +
		"├── base\n" +
		"│   └── nixpkgs\n" +
		"│       └── flake-utils\n" +
		"├── *nixpkgs\n" +
		"└── overlay\n" +
		"    ├── *base\n" +
		"    └── *nixpkgs\n"
	if buf.String() != want {
		t.Errorf("Tree --all-inputs mismatch:\n got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

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

// TestTree_RendersGraphFromURLMatching exercises Tree end-to-end using the new
// URL-based edge discovery. Repos declare inputs with URLs matching other workspace
// repos' remote URLs, and the DAG is built from those matches.
func TestTree_RendersGraphFromURLMatching(t *testing.T) {
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

[repos.overlay]
url = "github:o/overlay"
`)

	// The new expression for gatherInputURLs.
	fullApplyExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	evalArgs := func(repo, apply string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", apply}
	}

	f := exec.NewFakeRunner()
	// base: no workspace inputs (only nixpkgs which doesn't match).
	f.AddResponse("nix", evalArgs("base", fullApplyExpr),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	// overlay: depends on base via URL matching.
	f.AddResponse("nix", evalArgs("overlay", fullApplyExpr),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true},"nb":{"url":"github:o/base","flake":true}}`)}, nil)
	// term: depends on base and overlay via URL matching.
	f.AddResponse("nix", evalArgs("term", fullApplyExpr),
		exec.Result{Stdout: []byte(`{"nb":{"url":"github:o/base","flake":true},"ovl":{"url":"github:o/overlay","flake":true}}`)}, nil)

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
