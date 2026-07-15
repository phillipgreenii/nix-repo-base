package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// openNoTerminal creates a workspace with two repos but no terminal configured,
// and a lock matching the config (so topoAlpha uses lock order).
func openNoTerminal(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	mkRepoDir(t, root, "base")
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.base]
url = "github:o/base"

[repos.leaf]
url = "github:o/leaf"
`)
	// Lock present but no terminal.
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["base", "leaf"],
  "repos": {
    "base": {"flake_path": "flake.nix", "remote_url": "github:o/base"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:o/leaf"}
  },
  "edges": []
}`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("openNoTerminal: %v", err)
	}
	return w
}

// TestTerminalRequired_Build: build errors with the standard message when
// no terminal is configured.
func TestTerminalRequired_Build(t *testing.T) {
	w := openNoTerminal(t)
	err := w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{})
	if err == nil {
		t.Fatal("Build should error when no terminal configured")
	}
	if !strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("Build error should use standard message; got: %v", err)
	}
}

// TestTerminalRequired_Build_FlagOverride: --terminal flag bypasses the error.
func TestTerminalRequired_Build_FlagOverride(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.leaf]
url = "github:o/leaf"
`)
	trustWS(t, root) // build now gates on workspace trust (bead pg2-x2q6o)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	f.AddResponse("darwin-rebuild", []string{"build", "--flake", filepath.Join(root, "leaf")}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Build(context.Background(), &out, BuildOptions{Terminal: "leaf", Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Build with --terminal should succeed; got: %v", err)
	}
}

// TestTerminalRequired_Apply: apply errors with the standard message when
// no terminal is configured.
func TestTerminalRequired_Apply(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}"

[repos.leaf]
url = "github:o/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply should error when no terminal configured")
	}
	if !strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("Apply error should use standard message; got: %v", err)
	}
}

// TestTerminalRequired_Tree: Tree errors with the standard message when
// no terminal is configured (non-allInputs path).
func TestTerminalRequired_Tree(t *testing.T) {
	w := openNoTerminal(t)
	err := w.Tree(context.Background(), &bytes.Buffer{}, TreeOptions{})
	if err == nil {
		t.Fatal("Tree should error when no terminal configured")
	}
	if !strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("Tree error should use standard message; got: %v", err)
	}
}

// TestTerminalRequired_Update: Update errors with the standard message when
// no terminal is configured.
func TestTerminalRequired_Update(t *testing.T) {
	w := openNoTerminal(t)
	err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{})
	if err == nil {
		t.Fatal("Update should error when no terminal configured")
	}
	if !strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("Update error should use standard message; got: %v", err)
	}
}

// TestTerminalOptional_Rebase_WarnsButContinues: rebase warns when no terminal
// but still runs. The warning must appear on errOut (stderr), not stdout.
func TestTerminalOptional_Rebase_WarnsButContinues(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "base")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.base]
url = "github:o/base"
`)
	f := exec.NewFakeRunner()
	// upstream check
	f.AddResponse("git", []string{"-C", filepath.Join(root, "base"), "rev-parse", "--abbrev-ref", "@{u}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase should succeed even without terminal; got: %v", err)
	}
	// Warning must appear on stderr, not stdout.
	if !strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("Rebase should warn about missing terminal on stderr; got errOut:\n%s\nstdout:\n%s", errOut.String(), out.String())
	}
	if strings.Contains(out.String(), "no terminal") {
		t.Errorf("warning must not appear on stdout; got stdout:\n%s", out.String())
	}
}

// TestTerminalCandidateList: error message includes candidate list when sinks
// exist with flake paths.
func TestTerminalCandidateList(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.leaf]
url = "github:o/leaf"
`)
	// Lock where leaf has a flake_path (so it's a candidate).
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["leaf"],
  "repos": {"leaf": {"flake_path": "flake.nix", "remote_url": "github:o/leaf"}},
  "edges": []
}`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{})
	if err == nil {
		t.Fatal("expected error when no terminal set")
	}
	// When there's one repo with a flake_path and no deps, it should appear in
	// the candidate list.
	if !strings.Contains(err.Error(), "leaf") {
		t.Errorf("error should list leaf as candidate; got: %v", err)
	}
}
