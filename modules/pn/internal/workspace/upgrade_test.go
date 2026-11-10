package workspace

import (
	"context"
	"io"
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
	e, f := newScriptedEnv(t, asUserTOML)
	leaf, dep := e.leafDir, e.depDir
	// Both repos have an upstream (hasUpstream, migrated onto x/gitclient's
	// RefReader.HasUpstream — bead pg2-oxle0), so Update pulls both.
	stubGitOpener(t, map[string]*fakeGitReader{
		dep:  {hasUpstreamVal: true},
		leaf: {hasUpstreamVal: true},
	})

	// Update sequence (clean, has upstream) for dep and leaf (alphabetical).
	for _, dir := range []string{dep, leaf} {
		f.add("git", []string{"-C", dir, "diff", "--quiet"}, exec.Result{}, nil)
		f.add("git", []string{"-C", dir, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		f.add("git", []string{"-C", dir, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
		f.add("./update-locks.sh", nil, exec.Result{}, nil)
		f.add("git", []string{"-C", dir, "push"}, exec.Result{}, nil)
	}

	// needsRebuild: make dep dirty so it returns true immediately and the build
	// runs. Every other call is answered by the as-user fake.
	f.add("git", []string{"-C", dep, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("M file\n")}, nil)

	if err := e.w.Upgrade(context.Background(), io.Discard, UpgradeOptions{InPlace: true}); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// Ensure the apply phase ran after Update, as the one flow: the user build and
	// the root activate. nix fmt is not part of Apply (it is a separate command).
	gotBuild, gotActivate, gotFmt := false, false, false
	for _, c := range f.Calls() {
		if c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "fmt" {
			gotFmt = true
		}
		gotBuild = gotBuild || isBuildCall(c)
		if c.Name == "sudo" && len(c.Args) == 2 && c.Args[1] == "activate" {
			gotActivate = true
		}
	}
	if !gotBuild || !gotActivate {
		t.Errorf("expected apply phase to build and activate; got calls=%+v", f.Calls())
	}
	if gotFmt {
		t.Errorf("Apply should not invoke nix fmt (fmt is now a separate pn workspace format command); got calls=%+v", f.Calls())
	}
}
