package workspace

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

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

	// Apply sequence: daemon check, nix fmt.
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)

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
	if err := w.Upgrade(context.Background(), io.Discard, UpgradeOptions{}); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// Ensure both nix fmt and the apply command were invoked, indicating Apply ran after Update.
	gotFmt := false
	gotApply := false
	for _, c := range f.Calls() {
		if c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "fmt" {
			gotFmt = true
		}
		if c.Name == "sudo" && len(c.Args) > 0 && c.Args[0] == "darwin-rebuild" {
			gotApply = true
		}
	}
	if !gotFmt || !gotApply {
		t.Errorf("expected apply phase to run nix fmt + apply command; got calls=%+v", f.Calls())
	}
}
