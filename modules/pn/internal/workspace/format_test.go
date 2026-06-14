package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestFormat_RunsPerRepoInTopoOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // bar
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // foo

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Format(context.Background(), &out, &errOut, FormatOptions{}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 nix fmt calls (one per repo), got %d", len(calls))
	}
	// Both calls must be nix fmt with the repo dir set.
	for i, c := range calls {
		if c.Name != "nix" || len(c.Args) != 1 || c.Args[0] != "fmt" {
			t.Errorf("call[%d]: expected nix fmt, got %v %v", i, c.Name, c.Args)
		}
		if c.Opts.Dir == "" {
			t.Errorf("call[%d]: Opts.Dir must be set (repo dir)", i)
		}
		if c.Opts.Stdout == nil {
			t.Errorf("call[%d]: format should stream output (Opts.Stdout set)", i)
		}
	}
}

// TestFormat_TerminalFlagSuppressesWarning verifies that passing Terminal via
// FormatOptions suppresses the no-terminal warning.
func TestFormat_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Format(context.Background(), &out, &errOut, FormatOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

func TestFormat_NoTerminalEmitsWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Format(context.Background(), &out, &errOut, FormatOptions{}); err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("expected no-terminal warning on errOut; got:\n%s", errOut.String())
	}
	if strings.Contains(out.String(), "no terminal") {
		t.Errorf("no-terminal warning must not appear on stdout; got:\n%s", out.String())
	}
}

func TestFormat_StopsOnFirstError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// First repo fails; second should not be called.
	f.AddResponse("nix", []string{"fmt"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	err = w.Format(context.Background(), &out, &errOut, FormatOptions{})
	if err == nil {
		t.Fatal("expected error when nix fmt fails")
	}
	if len(f.Calls()) != 1 {
		t.Errorf("expected exactly 1 call before stop-on-first-error; got %d", len(f.Calls()))
	}
}
