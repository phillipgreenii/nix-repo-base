package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPush_AllReposWithUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// upstream check + push, alphabetical order (bar, foo).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "push"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Errorf("expected 4 calls (check+push per repo), got %d", len(calls))
	}
	// The push streams; the upstream probe stays captured (silent).
	for _, c := range calls {
		last := c.Args[len(c.Args)-1]
		if last == "push" && c.Opts.Stdout == nil {
			t.Errorf("git push should stream output (Opts.Stdout set); got %v", c.Args)
		}
		if last == "@{u}" && c.Opts.Stdout != nil {
			t.Errorf("upstream probe should stay captured (Opts.Stdout nil); got %v", c.Args)
		}
	}
}

// TestPush_TerminalFlagSuppressesWarning verifies that passing Terminal via
// PushOptions suppresses the no-terminal warning even when config has no terminal.
func TestPush_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails — no push (we just care about the warning, not push behavior).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

func TestPush_SkipsWithoutUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails — no push should happen.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	for _, c := range f.Calls() {
		if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "push" {
			t.Errorf("expected no push call when upstream missing; got %v", c.Args)
		}
	}
}

// ---------------------------------------------------------------------------
// Push with SetUpstream flag
// ---------------------------------------------------------------------------

// TestPush_NoUpstreamNoFlag verifies that a repo with no upstream is skipped
// (no-op) when SetUpstream is false.
func TestPush_NoUpstreamNoFlag(t *testing.T) {
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
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{SetUpstream: false}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "push" || a == "-u" {
				t.Errorf("no push expected when no upstream and SetUpstream is false; got %v", c.Args)
			}
		}
	}
}

// TestPush_NoUpstreamWithFlag verifies that a repo with no upstream gets
// `git push -u origin <branch>` when SetUpstream is true.
func TestPush_NoUpstreamWithFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	// current branch lookup.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("my-feature\n")}, nil)
	// push -u origin <branch>.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push", "-u", "origin", "my-feature"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{SetUpstream: true}); err != nil {
		t.Fatalf("Push --set-upstream: %v", err)
	}
	// Verify push -u origin <branch> was called.
	var foundSetUpstream bool
	for _, c := range f.Calls() {
		args := c.Args
		if len(args) >= 6 && args[len(args)-4] == "push" && args[len(args)-3] == "-u" && args[len(args)-2] == "origin" && args[len(args)-1] == "my-feature" {
			foundSetUpstream = true
		}
	}
	if !foundSetUpstream {
		t.Errorf("expected git push -u origin my-feature; calls: %v", f.Calls())
	}
}

// TestPush_ExistingUpstreamPlainPush verifies that a repo that already has an
// upstream always gets a plain `git push`, even when SetUpstream is true.
func TestPush_ExistingUpstreamPlainPush(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{SetUpstream: true}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Verify a plain push (no -u) was issued.
	var foundPlainPush bool
	for _, c := range f.Calls() {
		if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "push" {
			// Args should be exactly ["-C", repoDir, "push"] — no -u.
			foundPlainPush = true
			for _, a := range c.Args {
				if a == "-u" {
					t.Errorf("existing-upstream push must NOT have -u; got %v", c.Args)
				}
			}
		}
	}
	if !foundPlainPush {
		t.Error("expected a plain git push for repo with existing upstream")
	}
}
