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
	var buf bytes.Buffer
	if err := w.Status(context.Background(), &buf); err != nil {
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
	var buf bytes.Buffer
	if err := w.Status(context.Background(), &buf); err != nil {
		t.Fatalf("Status should not return error on per-repo failure, got %v", err)
	}
	if !strings.Contains(buf.String(), "(error)") {
		t.Errorf("expected error marker; got:\n%s", buf.String())
	}
}
