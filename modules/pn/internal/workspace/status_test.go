package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestStatus_WritesPerRepoSections(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// bar comes first alphabetically.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte(" M file.txt\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "=== bar ===") {
		t.Errorf("missing bar header in output:\n%s", out)
	}
	if !strings.Contains(out, "(clean)") {
		t.Errorf("expected clean marker for empty status; got:\n%s", out)
	}
	if !strings.Contains(out, "=== foo ===") {
		t.Errorf("missing foo header in output:\n%s", out)
	}
	if !strings.Contains(out, " M file.txt") {
		t.Errorf("expected foo's git status output to be included; got:\n%s", out)
	}
	// Ordering: bar header should precede foo header (alphabetical).
	barIdx := strings.Index(out, "=== bar ===")
	fooIdx := strings.Index(out, "=== foo ===")
	if barIdx > fooIdx {
		t.Errorf("expected bar to appear before foo, got bar@%d foo@%d", barIdx, fooIdx)
	}
}

func TestStatus_ErrorIsNotFatal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128, Stderr: []byte("not a repo")}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{}); err != nil {
		t.Fatalf("Status should not return error on per-repo failure, got %v", err)
	}
	// Error output goes to errOut (stderr).
	if !strings.Contains(errBuf.String(), "(error)") {
		t.Errorf("expected error marker on stderr; got stdout:\n%s\nstderr:\n%s", buf.String(), errBuf.String())
	}
}

// TestStatus_TerminalFlagSuppressesWarning verifies that passing Terminal via
// StatusOptions suppresses the no-terminal warning even when config has no terminal.
func TestStatus_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Status(context.Background(), &out, &errOut, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

// TestStatus_WarningOnStderr verifies that the no-terminal warning goes to
// errOut (stderr) and not to stdout.
func TestStatus_WarningOnStderr(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Status(context.Background(), &out, &errOut, StatusOptions{}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("expected no-terminal warning on stderr; got stderr:\n%s\nstdout:\n%s", errOut.String(), out.String())
	}
	if strings.Contains(out.String(), "no terminal") {
		t.Errorf("warning must not appear on stdout; got:\n%s", out.String())
	}
}
