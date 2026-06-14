package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// makeFlakeDirs creates repo directories with a flake.nix for each name.
func makeFlakeDirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		dir := filepath.Join(root, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}
}

// TestDeriveLock_HappyPath: deriveLock returns a complete Lock with edges and no ValidationErrors.
func TestDeriveLock_HappyPath(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "base", "terminal")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "terminal"

[repos.terminal]
url = "github:o/terminal"

[repos.base]
url = "github:o/base"
`)
	// terminal depends on base via alias "my-base"
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	evalArgs := func(repo string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", fullExpr}
	}

	f := exec.NewFakeRunner()
	// base has no workspace inputs
	f.AddResponse("nix", evalArgs("base"),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	// terminal depends on base
	f.AddResponse("nix", evalArgs("terminal"),
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	lock, validErrs, err := deriveLock(context.Background(), ws, "")
	if err != nil {
		t.Fatalf("deriveLock: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if lock.Terminal != "terminal" {
		t.Errorf("lock.Terminal = %q, want %q", lock.Terminal, "terminal")
	}
	if len(lock.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(lock.Edges), lock.Edges)
	}
	e := lock.Edges[0]
	if e.Consumer != "terminal" || e.Alias != "my-base" || e.Target != "base" {
		t.Errorf("edge = %+v, want {Consumer:terminal Alias:my-base Target:base}", e)
	}
}

// TestDeriveLock_MissingTerminal: no terminal in config and auto-detect finds none (multi-repo without edges).
func TestDeriveLock_MissingTerminal(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "a", "b")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:o/a"

[repos.b]
url = "github:o/b"
`)
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	// a and b have no workspace inputs — they're isolated, so auto-detect returns ""
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "a", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "b", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	lock, validErrs, err := deriveLock(context.Background(), ws, "")
	if err != nil {
		t.Fatalf("deriveLock: %v", err)
	}
	// lock.Terminal should be ""
	if lock.Terminal != "" {
		t.Errorf("lock.Terminal = %q, want empty", lock.Terminal)
	}
	// Should have a missing_terminal validation error
	found := false
	for _, ve := range validErrs {
		if ve.Code == "missing_terminal" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing_terminal ValidationError, got %v", validErrs)
	}
}

// TestDeriveLock_TerminalNotSink: config has terminal that is consumed by another repo.
func TestDeriveLock_TerminalNotSink(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "nix-personal", "homelab")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "nix-personal"

[repos.nix-personal]
url = "github:phillipgreenii/nix-personal"

[repos.homelab]
url = "github:phillipgreenii/homelab"
`)
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	// homelab depends on nix-personal
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "homelab", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{"nix-personal":{"url":"github:phillipgreenii/nix-personal","flake":true}}`)}, nil)
	// nix-personal has no workspace inputs
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "nix-personal", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	lock, validErrs, err := deriveLock(context.Background(), ws, "")
	if err != nil {
		t.Fatalf("deriveLock: %v", err)
	}
	// lock should still have the terminal set (from config)
	if lock.Terminal != "nix-personal" {
		t.Errorf("lock.Terminal = %q, want %q", lock.Terminal, "nix-personal")
	}
	// Should have terminal_not_sink error
	found := false
	for _, ve := range validErrs {
		if ve.Code == "terminal_not_sink" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected terminal_not_sink ValidationError, got %v", validErrs)
	}
}

// TestEffectiveLock_ReturnsDiskLockWhenCurrent: when disk lock matches config repos,
// effectiveLock returns the disk lock (no nix eval).
func TestEffectiveLock_ReturnsDiskLockWhenCurrent(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "a", "b")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:o/a"

[repos.b]
url = "github:o/b"
`)
	// Write a lock that covers both repos
	writeFile(t, filepath.Join(root, LockFileName), `{
  "terminal": "b",
  "order": ["a", "b"],
  "repos": {
    "a": {"flake_path": "flake.nix", "remote_url": "github:o/a"},
    "b": {"flake_path": "flake.nix", "remote_url": "github:o/b"}
  },
  "edges": [{"consumer": "b", "alias": "a-input", "target": "a"}]
}`)

	f := exec.NewFakeRunner() // no nix calls expected
	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	lock, validErrs, err := ws.effectiveLock(context.Background())
	if err != nil {
		t.Fatalf("effectiveLock: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if lock.Terminal != "b" {
		t.Errorf("lock.Terminal = %q, want b", lock.Terminal)
	}
	// No nix calls should have been made
	if len(f.Calls()) != 0 {
		t.Errorf("expected no subprocess calls when disk lock is current, got %d", len(f.Calls()))
	}
}

// TestEffectiveLock_DerivesFreshLockWhenStale: when disk lock doesn't match
// configured repos, effectiveLock derives a fresh lock.
func TestEffectiveLock_DerivesFreshLockWhenStale(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "a", "b", "c") // c is new in config, not in disk lock

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:o/a"

[repos.b]
url = "github:o/b"

[repos.c]
url = "github:o/c"
`)
	// Write a stale lock that only has a and b
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["a", "b"],
  "repos": {
    "a": {"flake_path": "flake.nix", "remote_url": "github:o/a"},
    "b": {"flake_path": "flake.nix", "remote_url": "github:o/b"}
  },
  "edges": []
}`)

	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	for _, repo := range []string{"a", "b", "c"} {
		f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", fullExpr},
			exec.Result{Stdout: []byte(`{}`)}, nil)
	}

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	lock, _, err := ws.effectiveLock(context.Background())
	if err != nil {
		t.Fatalf("effectiveLock: %v", err)
	}
	// Should have all 3 repos in the derived lock
	if len(lock.Repos) != 3 {
		t.Errorf("expected 3 repos in derived lock, got %d: %v", len(lock.Repos), lock.Repos)
	}
	// nix eval should have been called (stale lock triggered derivation)
	if len(f.Calls()) == 0 {
		t.Error("expected nix eval calls for derivation, got none")
	}
}

// TestWorkspaceLockCmd_WritesFileAtomically: the lock command writes a file atomically
// (using tempfile+rename). This is tested by verifying the file exists after a
// successful derivation and matches the expected content.
func TestWorkspaceLockCmd_WritesFileAtomically(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "base", "terminal")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "terminal"

[repos.terminal]
url = "github:o/terminal"

[repos.base]
url = "github:o/base"
`)
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "base", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "terminal", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	// Verify file written
	lockPath := filepath.Join(root, LockFileName)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "my-base") {
		t.Errorf("lock file should contain edge alias; got:\n%s", string(data))
	}
}

// TestWorkspaceLockCmd_PreservesFileOnValidationError: if validation errors exist,
// WriteDerivedLock should NOT write the file.
func TestWorkspaceLockCmd_PreservesFileOnValidationError(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "a", "b")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:o/a"

[repos.b]
url = "github:o/b"
`)
	// Pre-existing lock file
	prevLockContent := `{"order":[],"repos":{},"edges":[]}`
	writeFile(t, filepath.Join(root, LockFileName), prevLockContent)

	// Both repos isolated, so missing_terminal → validation error
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	for _, r := range []string{"a", "b"} {
		f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, r, "flake.nix"), "inputs", "--apply", fullExpr},
			exec.Result{Stdout: []byte(`{}`)}, nil)
	}

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = ws.WriteDerivedLock(context.Background(), root)
	if err == nil {
		t.Fatal("expected error from WriteDerivedLock when validation errors exist")
	}

	// Previous file should still have its content
	data, readErr := os.ReadFile(filepath.Join(root, LockFileName))
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(data) != prevLockContent {
		t.Errorf("previous lock file should be preserved; got:\n%s", string(data))
	}
}

// TestWorkspaceOpen_SucceedsWithMissingLockFile: Open should not fail when
// pn-workspace.lock.json is absent.
func TestWorkspaceOpen_SucceedsWithMissingLockFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:o/a"
`)
	// No lock file written
	ws, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open should succeed without lock file: %v", err)
	}
	// ws.lock should be the emptyLock
	if len(ws.lock.Repos) != 0 || len(ws.lock.Edges) != 0 {
		t.Errorf("expected empty lock, got %+v", ws.lock)
	}
}

// TestWriteDerivedLock_RemovesLegacyLock: when pn-workspace.lock exists alongside
// pn-workspace.lock.json after a successful write, the legacy file is removed.
func TestWriteDerivedLock_RemovesLegacyLock(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "base", "terminal")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "terminal"

[repos.terminal]
url = "github:o/terminal"

[repos.base]
url = "github:o/base"
`)
	// Write a legacy lock file
	legacyPath := filepath.Join(root, LockFileNameLegacy)
	writeFile(t, legacyPath, `{"order":["base","terminal"],"dependsOn":{}}`)

	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "base", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "terminal", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var notice strings.Builder
	if err := ws.WriteDerivedLockTo(context.Background(), root, &notice, ""); err != nil {
		t.Fatalf("WriteDerivedLockTo: %v", err)
	}

	// Legacy file should be gone
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("expected legacy lock to be removed, but still exists")
	}

	// New lock file should exist
	if _, err := os.Stat(filepath.Join(root, LockFileName)); err != nil {
		t.Errorf("expected %s to exist: %v", LockFileName, err)
	}

	// Notice should mention removal
	if !strings.Contains(notice.String(), "removed legacy") {
		t.Errorf("expected removal notice, got %q", notice.String())
	}
}

// TestWriteDerivedLockTo_FlagTerminalOverridesConfig verifies that the flagTerminal
// parameter to WriteDerivedLockTo overrides workspace.terminal from the config,
// implementing the --terminal flag priority for the lock subcommand.
func TestWriteDerivedLockTo_FlagTerminalOverridesConfig(t *testing.T) {
	root := t.TempDir()
	// Two standalone repos, no config terminal — would auto-detect nothing (two sinks).
	makeFlakeDirs(t, root, "base", "override")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.base]
url = "github:o/base"

[repos.override]
url = "github:o/override"
`)
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	// Neither repo has workspace-level inputs (no edges between them).
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "base", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "override", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Without flag, WriteDerivedLock would fail (ambiguous terminal — two isolated sinks).
	errNoFlag := ws.WriteDerivedLockTo(context.Background(), root, nil, "")
	if errNoFlag == nil {
		t.Fatal("expected error without flagTerminal (ambiguous terminal); got nil")
	}

	// Re-open (nix eval responses consumed above; need fresh fake runner).
	f2 := exec.NewFakeRunner()
	f2.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "base", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f2.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "override", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)
	ws2, err := Open(root, f2)
	if err != nil {
		t.Fatalf("Open ws2: %v", err)
	}

	// With flagTerminal = "override", it should succeed and write override as terminal.
	if err := ws2.WriteDerivedLockTo(context.Background(), root, nil, "override"); err != nil {
		t.Fatalf("WriteDerivedLockTo with flagTerminal: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(string(data), `"override"`) || !strings.Contains(string(data), `"terminal"`) {
		t.Errorf("lock should have terminal=override when flag is set; got:\n%s", string(data))
	}
	lock, err := ReadLock(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if lock.Terminal != "override" {
		t.Errorf("lock.Terminal = %q; want override", lock.Terminal)
	}
}
