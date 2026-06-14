package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestBuild_TerminalOnlyWithOverrides(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	// Write lock file so overrideInputArgsFor has edges to work with.
	// leaf depends on dep via alias "dep-input".
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "leaf"],
  "repos": {
    "dep":  {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [{"consumer": "leaf", "alias": "dep-input", "target": "dep"}]
}`)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")
	f := exec.NewFakeRunner()
	f.AddResponse("darwin-rebuild", []string{
		"build", "--flake", leafDir,
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Build(context.Background(), &out, BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out.String(), "leaf") {
		t.Errorf("build output should name the terminal project %q; got:\n%s", "leaf", out.String())
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 build call (no fmt), got %d", len(calls))
	}
	if calls[0].Opts.Dir != leafDir {
		t.Errorf("build command must run in terminal dir; got %q", calls[0].Opts.Dir)
	}
	// Build command streams its output live (Opts.Stdout set).
	if calls[0].Opts.Stdout == nil {
		t.Errorf("build should stream subprocess output (Opts.Stdout set on build)")
	}
}

func TestBuild_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Build(context.Background(), out, BuildOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "darwin-rebuild build --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing build command:\n%s", out.String())
	}
	if strings.Contains(out.String(), "nix fmt") {
		t.Errorf("dry-run output should not contain 'nix fmt' (fmt is now a separate command):\n%s", out.String())
	}
}

func TestBuild_ErrorsWhenTerminalUnset(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{}); err == nil {
		t.Fatal("expected error when terminal unset")
	}
}
