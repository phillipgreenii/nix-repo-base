package workspace

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestUpgrade_TerminalFlagForwardedToUpdateAndApply verifies that UpgradeOptions.Terminal
// is forwarded to both Update and Apply, allowing --terminal to override config.
// When config has no terminal and no flag is passed, Update returns an error;
// passing the flag must allow the command to proceed past the requireTerminal check.
func TestUpgrade_TerminalFlagForwardedToUpdateAndApply(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
apply_command = "true"

[repos.leaf]
url = "github:owner/leaf"
`)
	// Without a terminal flag, Upgrade must fail with the no-terminal error.
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	noFlagErr := w.Upgrade(context.Background(), io.Discard, UpgradeOptions{})
	if noFlagErr == nil {
		t.Fatal("Upgrade without terminal and no config terminal: expected error, got nil")
	}
	if !strings.Contains(noFlagErr.Error(), "no terminal repo configured") {
		t.Fatalf("unexpected error (want no-terminal): %v", noFlagErr)
	}

	// With terminal flag, Upgrade must NOT return a no-terminal error — it proceeds
	// past requireTerminal. Subsequent subprocess failures are fine (FakeRunner).
	f2 := exec.NewFakeRunner()
	w2, err := Open(root, f2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w2.Upgrade(context.Background(), io.Discard, UpgradeOptions{Terminal: "leaf"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("--terminal flag must be forwarded to Update; got no-terminal error: %v", err)
	}
}

func TestUpgrade_RunsUpdateThenApply(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	// Lock file: leaf depends on dep via alias "dep" (default = repo key).
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "leaf"],
  "repos": {
    "dep":  {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [{"consumer": "leaf", "alias": "dep", "target": "dep"}]
}`)

	f := exec.NewFakeRunner()
	leaf := filepath.Join(root, "leaf")
	dep := filepath.Join(root, "dep")

	// Update sequence (clean, has upstream) for dep and leaf (alphabetical).
	for _, dir := range []string{dep, leaf} {
		f.AddResponse("git", []string{"-C", dir, "diff", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", dir, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", dir, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
		f.AddResponse("git", []string{"-C", dir, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
		f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", dir, "push"}, exec.Result{}, nil)
	}
	// captureHead for rev-lock: Update calls rev-parse HEAD for dep and leaf.
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	f.AddResponse("git", []string{"-C", leaf, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("def\n")}, nil)

	// Apply sequence: daemon check only (nix fmt is now a separate pn workspace format command).
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)

	// needsRebuild: git status --porcelain + git rev-parse HEAD for each repo.
	// Pre-write the applied-hash files with matching hashes so rebuild is skipped,
	// OR mark repos dirty so we skip the hash check and force a rebuild.
	// Simplest: make dep dirty so needsRebuild returns true immediately.
	f.AddResponse("git", []string{"-C", dep, "status", "--porcelain"}, exec.Result{Stdout: []byte("M file\n")}, nil)

	// Apply command runs because dep is dirty.
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leaf + "#" + shortHostname(),
		"--override-input", "dep", "git+file://" + dep,
	}, exec.Result{}, nil)

	// markApplied: git rev-parse HEAD for dep and leaf.
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	f.AddResponse("git", []string{"-C", leaf, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("def\n")}, nil)

	// Write applied-hash dir so markApplied can write files.
	hashDir := filepath.Join(stateDir, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatalf("mkdir hash dir: %v", err)
	}

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Upgrade(context.Background(), io.Discard, UpgradeOptions{InPlace: true}); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// Ensure the apply command was invoked (Apply ran after Update).
	// nix fmt is no longer part of Apply; it is a separate pn workspace format command.
	gotApply := false
	gotFmt := false
	for _, c := range f.Calls() {
		if c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "fmt" {
			gotFmt = true
		}
		if c.Name == "sudo" && len(c.Args) > 0 && c.Args[0] == "darwin-rebuild" {
			gotApply = true
		}
	}
	if !gotApply {
		t.Errorf("expected apply phase to run apply command; got calls=%+v", f.Calls())
	}
	if gotFmt {
		t.Errorf("Apply should not invoke nix fmt (fmt is now a separate pn workspace format command); got calls=%+v", f.Calls())
	}
}
