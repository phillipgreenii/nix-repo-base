package workspace

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// These tests pin WHICH call sites set exec.RunOptions.WrapNix (pg2-kqrrs.8,
// ADR 0028): only the long-running nix calls (build, apply, flake check,
// format, the nix verb, propagate, tree's lock call). The wrapper decorator
// never rewrites by command name, so every short stdout-parsed probe that runs
// the same binaries MUST leave the flag false.

// wrapNixOf returns the WrapNix value of the single recorded call whose Name is
// name and whose first arg is first ("" matches any), failing if there is none.
func wrapNixOf(t *testing.T, calls []exec.Call, name, first string) bool {
	t.Helper()
	for _, c := range calls {
		if c.Name != name {
			continue
		}
		if first != "" && (len(c.Args) == 0 || c.Args[0] != first) {
			continue
		}
		return c.Opts.WrapNix
	}
	t.Fatalf("no recorded %s %s call in %+v", name, first, calls)
	return false
}

func TestWrapNix_SetOnBuild(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	trustWS(t, root)
	f := exec.NewFakeRunner()
	f.AddResponse("darwin-rebuild", []string{"build", "--flake", filepath.Join(root, "leaf")}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), io.Discard, BuildOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !wrapNixOf(t, f.Calls(), "darwin-rebuild", "build") {
		t.Error("Build must set WrapNix")
	}
}

// Apply sets WrapNix on the apply command (sudo darwin-rebuild switch) and NOT
// on the nix daemon health probe (nix eval) that runs just before it.
func TestWrapNix_SetOnApplyNotOnItsProbe(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	writeFile(t, filepath.Join(root, LockFileName), applyLock)
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leafDir + "#" + shortHostname(),
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", depDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("d\n")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)
	f.AddResponse("git", []string{"-C", depDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), io.Discard, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := f.Calls()
	if !wrapNixOf(t, calls, "sudo", "darwin-rebuild") {
		t.Error("Apply must set WrapNix on the apply command")
	}
	if wrapNixOf(t, calls, "nix", "eval") {
		t.Error("the nix daemon health probe (nix eval) must NOT set WrapNix")
	}
	for _, c := range calls {
		if c.Name == "git" && c.Opts.WrapNix {
			t.Errorf("git call %v must not set WrapNix", c.Args)
		}
	}
}

func TestWrapNix_SetOnFlakeCheck(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:owner/foo\"\n")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.FlakeCheck(context.Background(), io.Discard, io.Discard, FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}
	if !wrapNixOf(t, f.Calls(), "nix", "flake") {
		t.Error("FlakeCheck must set WrapNix")
	}
}

func TestWrapNix_SetOnFormat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:owner/foo\"\n")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Format(context.Background(), io.Discard, io.Discard, FormatOptions{}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !wrapNixOf(t, f.Calls(), "nix", "fmt") {
		t.Error("Format must set WrapNix")
	}
}

func TestWrapNix_SetOnNixVerb(t *testing.T) {
	w := newTestWorkspace(t, "[workspace]\nname = \"test\"\nterminal = \"foo\"\n\n[repos.foo]\nurl = \"github:o/foo\"\n",
		map[string]struct {
			flakeInputs string
			gitRemotes  string
			createFlake bool
		}{
			"foo": {flakeInputs: `{}`, gitRemotes: "origin\tgithub:o/foo (fetch)\norigin\tgithub:o/foo (push)\n", createFlake: true},
		})
	runner := w.Runner().(*exec.FakeRunner)
	runner.AddResponse("nix", []string{"build"}, exec.Result{}, nil)
	var out bytes.Buffer
	if err := w.NixCommand(context.Background(), &out, []string{"build"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
	if !wrapNixOf(t, runner.Calls(), "nix", "build") {
		t.Error("the nix verb must set WrapNix")
	}
}

func TestWrapNix_SetOnPropagate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "flake.lock"), lockWith("1111111111111111111111111111111111111111", 1))
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "update", "--refresh", "sib"}, exec.Result{}, nil)
	ws := &Workspace{runner: f}
	// Later steps (git probes) are unscripted and fail; only the nix call matters.
	_, _ = ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if !wrapNixOf(t, f.Calls(), "nix", "flake") {
		t.Error("propagate's nix flake update must set WrapNix")
	}
}

func TestWrapNix_SetOnTreeLock(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "lock", "path:" + filepath.Join(root, "term")}, exec.Result{}, nil)
	ws := &Workspace{root: root, runner: f}
	// The lock file the fake never writes makes the follow-up read fail; only
	// the lock call matters.
	_ = ws.treeAllInputs(context.Background(), io.Discard, "term")
	if !wrapNixOf(t, f.Calls(), "nix", "flake") {
		t.Error("tree's nix flake lock must set WrapNix")
	}
}

// Short stdout-parsed probes that run the same binaries MUST NOT set WrapNix.
func TestWrapNix_FalseOnProbes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "flake.nix"), "{}")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(dir, "flake.nix"), "inputs"},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	if _, err := readFlakeInputs(context.Background(), f, dir); err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	// evalInputSpecs: scripted nothing, so every tier fails; the calls are what matter.
	_, _ = evalInputSpecs(context.Background(), f, filepath.Join(dir, "flake.nix"))
	ws := &Workspace{runner: f}
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	if err := ws.checkNixDaemon(context.Background()); err != nil {
		t.Fatalf("checkNixDaemon: %v", err)
	}

	calls := f.Calls()
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 probe calls, got %d", len(calls))
	}
	for _, c := range calls {
		if c.Opts.WrapNix {
			t.Errorf("probe %s %v must not set WrapNix", c.Name, c.Args)
		}
	}
}
