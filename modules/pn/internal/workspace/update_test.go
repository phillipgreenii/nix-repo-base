package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestUpdate_ContinuesPastFailureAndAggregates: when one repo's update-locks
// fails, Update must still process the remaining repos and report the failure
// at the end (naming the failing repo), instead of aborting on first error.
func TestUpdate_ContinuesPastFailureAndAggregates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	bar := filepath.Join(root, "bar")
	foo := filepath.Join(root, "foo")
	for _, d := range []string{bar, foo} {
		f.AddResponse("git", []string{"-C", d, "diff", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
		f.AddResponse("git", []string{"-C", d, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "push"}, exec.Result{}, nil)
	}
	// update-locks: bar (runs first, alphabetical) fails; foo succeeds. Pull
	// succeeded for bar, so its push still runs (matches bash).
	f.AddResponse("./update-locks.sh", nil, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "./update-locks.sh", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{})
	if err == nil {
		t.Fatal("expected error reporting failures, got nil")
	}
	if !strings.Contains(err.Error(), "bar") {
		t.Errorf("error should name failing repo (bar); got %q", err.Error())
	}
	if strings.Contains(err.Error(), "foo") {
		t.Errorf("error should not name the passing repo (foo); got %q", err.Error())
	}
	// Both repos fully processed: 6 calls each (diff, cached, rev-parse, pull,
	// update-locks, push).
	if len(f.Calls()) != 12 {
		t.Errorf("expected both repos fully attempted (12 calls); got %d", len(f.Calls()))
	}
}

// TestUpdate_PullFailureSkipsLocksAndPush: a failed git pull marks the repo as
// failed and skips update-locks and push (the working tree is suspect).
func TestUpdate_PullFailureSkipsLocksAndPush(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	// pull fails — update-locks and push must NOT run.
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{})
	if err == nil {
		t.Fatal("expected error for failed pull, got nil")
	}
	for _, c := range f.Calls() {
		if c.Name == "./update-locks.sh" {
			t.Errorf("update-locks must not run after a failed pull")
		}
		for _, a := range c.Args {
			if a == "push" {
				t.Errorf("push must not run after a failed pull; got %v", c.Args)
			}
		}
	}
}

func TestUpdate_PullLocksPushPerRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	// dirty checks: both pass (clean).
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	// upstream check.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	// pull rebase autostash.
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	// update-locks.
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	// push.
	f.AddResponse("git", []string{"-C", foo, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 6 {
		t.Errorf("expected 6 calls, got %d (%+v)", len(calls), calls)
	}
	// Long-running steps stream; the silent --quiet probes stay captured.
	for _, c := range calls {
		switch {
		case lastArg(c.Args) == "--autostash", c.Name == "./update-locks.sh", lastArg(c.Args) == "push":
			if c.Opts.Stdout == nil {
				t.Errorf("%s %v should stream (Opts.Stdout set)", c.Name, c.Args)
			}
		case lastArg(c.Args) == "--quiet":
			if c.Opts.Stdout != nil {
				t.Errorf("dirty probe %v should stay captured (Opts.Stdout nil)", c.Args)
			}
		}
	}
}

func lastArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func TestUpdate_SkipsDirtyRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	// dirty check fails -> repo skipped.
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Only the dirty probe should be called; no pull/locks/push.
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "pull" || a == "push" {
				t.Errorf("expected no pull/push for dirty repo; got %v", c.Args)
			}
		}
	}
}

func TestUpdate_NoUpstreamRunsLocksOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	// no upstream
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	// update-locks still runs.
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "pull" || a == "push" {
				t.Errorf("expected no pull/push without upstream; got %v", c.Args)
			}
		}
	}
}
