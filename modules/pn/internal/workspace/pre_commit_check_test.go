package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPreCommitCheck_PerRepoInOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pre-commit", []string{"run", "--all-files"}, exec.Result{}, nil)
	f.AddResponse("pre-commit", []string{"run", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.PreCommitCheck(context.Background(), &out, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("expected bar first; dir=%q", calls[0].Opts.Dir)
	}
	if calls[1].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("expected foo second; dir=%q", calls[1].Opts.Dir)
	}
	for i, c := range calls {
		if c.Opts.Stdout == nil {
			t.Errorf("call %d: pre-commit should stream output (Opts.Stdout set)", i)
		}
	}
}

func TestPreCommitCheck_ContinuesPastFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pre-commit", []string{"run", "--all-files"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "pre-commit", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("pre-commit", []string{"run", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, PreCommitCheckOptions{}); err == nil {
		t.Fatal("expected combined error from per-repo failure")
	}
	if len(f.Calls()) != 2 {
		t.Errorf("expected both repos attempted; got %d calls", len(f.Calls()))
	}
}
