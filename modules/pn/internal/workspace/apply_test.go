package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const applyTOML = `
[workspace]
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`

// applyLock is the lock with edge leaf→dep alias "dep-input", used by tests
// that need overrideInputArgsFor to emit the override.
const applyLock = `{
  "order": ["dep", "leaf"],
  "repos": {
    "dep":  {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [{"consumer": "leaf", "alias": "dep-input", "target": "dep"}]
}`

func TestApply_RunsApplyCommandWithOverrides(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Apply's success path calls markApplied -> writeAppliedState; isolate the
	// store to a temp dir so it never touches ~/.local/share (the XDG fallback).
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	// Write lock so overrideInputArgsFor emits the dep-input override.
	writeFile(t, filepath.Join(root, LockFileName), applyLock)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leafDir + "#" + shortHostname(),
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", depDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("d\n")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)
	f.AddResponse("git", []string{"-C", depDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Output names the terminal project.
	if !strings.Contains(out.String(), "leaf") {
		t.Errorf("apply output should name the terminal project %q; got:\n%s", "leaf", out.String())
	}
	// The apply command streams its output live (Opts.Stdout set).
	var streamed bool
	for _, c := range f.Calls() {
		if c.Name == "sudo" {
			streamed = c.Opts.Stdout != nil
		}
	}
	if !streamed {
		t.Errorf("apply command should stream output (Opts.Stdout set)")
	}
}

// calledPkillFsmonitor reports whether the FakeRunner saw the
// `pkill -f 'git fsmonitor--daemon'` invocation.
func calledPkillFsmonitor(calls []exec.Call) bool {
	for _, c := range calls {
		if c.Name == "pkill" && len(c.Args) == 2 && c.Args[0] == "-f" && c.Args[1] == "git fsmonitor--daemon" {
			return true
		}
	}
	return false
}

// applyTestRunner scripts a successful single-repo apply (daemon check, the
// darwin-rebuild command, and markApplied's rev-parse) for terminal "leaf".
func applyTestRunner(t *testing.T, root string) (*exec.FakeRunner, string) {
	t.Helper()
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leafDir + "#" + shortHostname(),
	}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	return f, leafDir
}

const applySingleRepoTOML = `
[workspace]
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`

// TestApply_RestartsFsmonitorWhenGitVersionChanges asserts that pkill is invoked
// when the git version differs before vs after the rebuild.
func TestApply_RestartsFsmonitorWhenGitVersionChanges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Apply's success path writes the applied-state store; keep it in a temp dir.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)

	f, _ := applyTestRunner(t, root)
	// git --exec-path: old then new (changed store path) — FIFO consumption.
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.43.0/libexec/git-core\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/cccccccccccccccccccccccccccccccc-git-2.45.0/libexec/git-core\n")}, nil)
	f.AddResponse("pkill", []string{"-f", "git fsmonitor--daemon"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !calledPkillFsmonitor(f.Calls()) {
		t.Errorf("expected pkill of fsmonitor daemon when git version changed; calls:\n%+v", f.Calls())
	}
}

// TestApply_RestartsFsmonitorWhenGitBinaryChangesSameVersion asserts that pkill
// is invoked when the git binary changes (its --exec-path / store path) even
// though the version string is identical. A same-version Nix rebuild swaps the
// binary store path, and the running daemon would otherwise keep executing the
// stale binary — keying on the version string alone misses this case.
func TestApply_RestartsFsmonitorWhenGitBinaryChangesSameVersion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)

	f, _ := applyTestRunner(t, root)
	// git --exec-path: same version 2.54.0, different store path before/after.
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.54.0/libexec/git-core\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-git-2.54.0/libexec/git-core\n")}, nil)
	f.AddResponse("pkill", []string{"-f", "git fsmonitor--daemon"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !calledPkillFsmonitor(f.Calls()) {
		t.Errorf("expected pkill of fsmonitor daemon when git binary path changed (same version); calls:\n%+v", f.Calls())
	}
}

// TestApply_NoFsmonitorRestartWhenGitUnchanged asserts that pkill is NOT invoked
// when the git version is identical before and after the rebuild.
func TestApply_NoFsmonitorRestartWhenGitUnchanged(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Apply's success path writes the applied-state store; keep it in a temp dir.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)

	f, _ := applyTestRunner(t, root)
	// git --exec-path: same path both times — git binary unchanged.
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.43.0/libexec/git-core\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.43.0/libexec/git-core\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calledPkillFsmonitor(f.Calls()) {
		t.Errorf("pkill must not run when git version is unchanged; calls:\n%+v", f.Calls())
	}
}

// TestApply_NoFsmonitorRestartOnSkippedRebuild asserts that the skip-rebuild
// (no-op) path neither checks the git version nor kills the daemon.
func TestApply_NoFsmonitorRestartOnSkippedRebuild(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)
	leafDir := filepath.Join(root, "leaf")

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	// needsRebuild: clean tree + HEAD matches the recorded applied hash → no rebuild.
	f.AddResponse("git", []string{"-C", leafDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	// Pre-seed the new applied-state store so HEAD ("abc") matches and the rebuild is skipped.
	if err := writeAppliedState(leafDir, AppliedState{AppliedRef: "abc"}); err != nil {
		t.Fatalf("seed applied state: %v", err)
	}

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calledPkillFsmonitor(f.Calls()) {
		t.Errorf("pkill must not run on the skip-rebuild path; calls:\n%+v", f.Calls())
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) == 1 && c.Args[0] == "--exec-path" {
			t.Errorf("git --exec-path must not be probed on the skip-rebuild path; calls:\n%+v", f.Calls())
		}
	}
}

func TestApply_ErrorsWhenApplyCommandMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{}); err == nil {
		t.Fatal("expected error when apply_command unset")
	}
}

func TestApply_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "sudo darwin-rebuild switch --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing apply command:\n%s", out.String())
	}
	if strings.Contains(out.String(), "nix fmt") {
		t.Errorf("dry-run output should not contain 'nix fmt' (fmt is now a separate command):\n%s", out.String())
	}
}

func TestApplyColorEnv(t *testing.T) {
	if got := applyColorEnv(false); got != nil {
		t.Fatalf("colorOK=false: want nil env, got %v", got)
	}
	got := applyColorEnv(true)
	if len(got) != 1 {
		t.Fatalf("colorOK=true: want exactly 1 env key, got %d: %v", len(got), got)
	}
	if got["CLICOLOR_FORCE"] != "1" {
		t.Fatalf("colorOK=true: want CLICOLOR_FORCE=1, got %v", got)
	}
}

func TestAllRepoDirs_SkipsMissingClones(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf") // cloned
	// "dep" declared but NOT cloned on disk.
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	got := w.allRepoDirs(nil)
	want := []string{filepath.Join(root, "leaf")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allRepoDirs should skip missing clones: got %#v want %#v", got, want)
	}
}
