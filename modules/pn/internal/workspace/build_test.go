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
	if err := w.Build(context.Background(), &out, BuildOptions{Builder: "darwin-rebuild"}); err != nil {
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
	if err := w.Build(context.Background(), out, BuildOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"}); err != nil {
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

// TestBuild_ForeignOSErrorsWithoutBuilder verifies the loud-fail path: with the
// default template (which references {builder}) and no builder for this OS,
// Build returns an actionable error and never invokes the runner. Skipped on a
// host that HAS a built-in builder (darwin / NixOS), where the default resolves.
func TestBuild_ForeignOSErrorsWithoutBuilder(t *testing.T) {
	if defaultBuilder() != "" {
		t.Skip("host has a built-in builder; foreign-OS path not exercised here")
	}
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
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{})
	if err == nil {
		t.Fatal("expected loud error when no builder is available for the default template")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("runner must not be invoked on the builder-empty error; got %d calls", len(f.Calls()))
	}
}

// TestBuild_CheckFollowsRunsOnNixDir is the regression test for §3.7: for a
// subdir-flake terminal the follows check must read <repo>/nix/flake.lock, not
// <repo>/flake.lock. Before the re-wire it silently no-op'd (repo-root lock
// absent); now a broken follow in the nix-subdir lock fails Build. checkFollows
// runs before builder resolution, so this is deterministic on any host.
func TestBuild_CheckFollowsRunsOnNixDir(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, filepath.Join("leaf", "nix"))
	mkRepoDir(t, root, "a_dep")
	mkRepoDir(t, root, "b_dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.a_dep]
url = "github:owner/a"

[repos.b_dep]
url = "github:owner/b"
`)
	// Lock: leaf is a subdir flake (nix/flake.nix) with two workspace inputs.
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["a_dep", "b_dep", "leaf"],
  "repos": {
    "a_dep": {"flake_path": "flake.nix", "remote_url": "github:owner/a"},
    "b_dep": {"flake_path": "flake.nix", "remote_url": "github:owner/b"},
    "leaf":  {"flake_path": "nix/flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [
    {"consumer": "leaf", "alias": "a", "target": "a_dep"},
    {"consumer": "leaf", "alias": "b", "target": "b_dep"}
  ]
}`)
	// Broken follow lives in the nix SUBDIR lock: input "a" carries its own
	// unfollowed copy of "b". A repo-root flake.lock is intentionally absent.
	writeFile(t, filepath.Join(root, "leaf", "nix", "flake.lock"), `{
  "nodes": {
    "root": {"inputs": {"a": "a", "b": "b"}},
    "a": {"inputs": {"b": "b_2"}},
    "b": {"inputs": {}},
    "b_2": {"inputs": {}}
  }
}`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// ShowNixCommandsOnly still triggers checkFollows (it runs first); Builder is
	// injected but irrelevant because the follows error precedes builder use.
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"})
	if err == nil {
		t.Fatal("expected follows error from the nix-subdir flake.lock")
	}
	if !strings.Contains(err.Error(), "does not follow") {
		t.Errorf("expected a follows-violation error; got: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("runner must not be invoked when follows check fails; got %d calls", len(f.Calls()))
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
