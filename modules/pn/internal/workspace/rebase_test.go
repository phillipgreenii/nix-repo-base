package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestRebase_PerRepoInOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "mu"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "mu"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Errorf("expected 4 calls (check+mu per repo), got %d", len(calls))
	}
	// The rebase (git mu) streams; the upstream probe stays captured.
	for _, c := range calls {
		last := c.Args[len(c.Args)-1]
		if last == "mu" && c.Opts.Stdout == nil {
			t.Errorf("git mu should stream output (Opts.Stdout set); got %v", c.Args)
		}
		if last == "@{u}" && c.Opts.Stdout != nil {
			t.Errorf("upstream probe should stay captured (Opts.Stdout nil); got %v", c.Args)
		}
	}
}

func TestRebase_SkipsWithoutUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "mu" {
				t.Errorf("expected no git mu call when upstream missing; got %v", c.Args)
			}
		}
	}
}
