package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestUpdateOrder_PrefersLockTopoOrder: Update iterates the lock's topological
// order (dependencies first, terminal last) when the lock covers the repo set —
// here [lib, app], not the alphabetical [app, lib].
func TestUpdateOrder_PrefersLockTopoOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.app]
url = "github:o/app"

[repos.lib]
url = "github:o/lib"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":["lib","app"],"repos":{"app":{"remote_url":"github:o/app"},"lib":{"remote_url":"github:o/lib"}},"edges":[]}`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.updateOrder()
	want := []string{"lib", "app"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("updateOrder = %v, want %v (lock topo order)", got, want)
	}
}

// TestUpdateOrder_FallsBackAlphabeticalWhenLockStale: when the lock doesn't
// cover the configured repo set (stale/empty), fall back to alphabetical.
func TestUpdateOrder_FallsBackAlphabeticalWhenLockStale(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.app]
url = "github:o/app"

[repos.lib]
url = "github:o/lib"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":["lib"],"repos":{"lib":{"remote_url":"github:o/lib"}},"edges":[]}`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.updateOrder()
	want := []string{"app", "lib"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("updateOrder = %v, want alphabetical fallback %v", got, want)
	}
}

// TestResolveULLibDir runs the resolver once and returns its path; on any
// error it returns "" so callers fall back to per-repo resolution.
func TestResolveULLibDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:o/foo"
`)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"run", "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir"},
		exec.Result{Stdout: []byte("/nix/store/abc/lib/scripts\n")}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := w.ResolveULLibDir(context.Background()); got != "/nix/store/abc/lib/scripts" {
		t.Errorf("ResolveULLibDir = %q, want trimmed path", got)
	}
	// The resolver must run with WORKSPACE_ROOT set so its sibling tier can fire.
	if f.Calls()[0].Opts.Env["WORKSPACE_ROOT"] != root {
		t.Errorf("resolver should run with WORKSPACE_ROOT=%q, got env %v", root, f.Calls()[0].Opts.Env)
	}

	// On error (no scripted response), returns empty so callers fall back.
	f2 := exec.NewFakeRunner()
	w2, _ := Open(root, f2)
	if got := w2.ResolveULLibDir(context.Background()); got != "" {
		t.Errorf("ResolveULLibDir on error = %q, want empty", got)
	}
}

// TestUpdate_InjectsULLibDirAndWorkspaceEnv: the update-locks subprocess gets
// UL_LIB_DIR (when supplied) plus the workspace-root env vars, so it skips its
// own resolver call and tools can locate the workspace.
func TestUpdate_InjectsULLibDirAndWorkspaceEnv(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	// no upstream → straight to update-locks
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	// rev-parse HEAD for lock capture.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/nix/store/xyz/lib/scripts"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var found bool
	for _, c := range f.Calls() {
		if c.Name != "./update-locks.sh" {
			continue
		}
		found = true
		if c.Opts.Env["UL_LIB_DIR"] != "/nix/store/xyz/lib/scripts" {
			t.Errorf("UL_LIB_DIR not injected; env=%v", c.Opts.Env)
		}
		if c.Opts.Env["WORKSPACE_ROOT"] != root {
			t.Errorf("WORKSPACE_ROOT not injected; env=%v", c.Opts.Env)
		}
		if c.Opts.Env["PN_WORKSPACE_ROOT"] != root {
			t.Errorf("PN_WORKSPACE_ROOT not injected; env=%v", c.Opts.Env)
		}
	}
	if !found {
		t.Fatal("update-locks.sh was not called")
	}
}

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
	// rev-parse HEAD for foo's lock capture (foo succeeds, bar fails so no capture).
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("aabbccdd0000000000000000000000000000000\n")}, nil)

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
	// update-locks, push) + 1 rev-parse HEAD for foo = 13 total.
	if len(f.Calls()) != 13 {
		t.Errorf("expected both repos fully attempted (13 calls); got %d", len(f.Calls()))
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
	// rev-parse HEAD for lock capture.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("deadbeef0000000000000000000000000000000\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 7 {
		t.Errorf("expected 7 calls, got %d (%+v)", len(calls), calls)
	}

	// Verify pn-workspace.revs.json was written with the expected rev.
	lockBytes, err := os.ReadFile(filepath.Join(root, RevLockFileName))
	if err != nil {
		t.Fatalf("read %s: %v", RevLockFileName, err)
	}
	var revLock RevLock
	if err := json.Unmarshal(lockBytes, &revLock); err != nil {
		t.Fatalf("parse %s: %v", RevLockFileName, err)
	}
	if revLock.Repos["foo"].Rev != "deadbeef0000000000000000000000000000000" {
		t.Errorf("locked rev: got %q, want deadbeef0000000000000000000000000000000", revLock.Repos["foo"].Rev)
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

	// Verify pn-workspace.revs.json was still written (with no entries since repo was skipped).
	lockBytes, err := os.ReadFile(filepath.Join(root, RevLockFileName))
	if err != nil {
		t.Fatalf("read %s: %v", RevLockFileName, err)
	}
	var revLock RevLock
	if err := json.Unmarshal(lockBytes, &revLock); err != nil {
		t.Fatalf("parse %s: %v", RevLockFileName, err)
	}
	// Skipped repo should not appear in rev-lock (no prior lock, repo was skipped).
	if _, exists := revLock.Repos["foo"]; exists {
		t.Errorf("expected foo not in rev-lock for skipped dirty repo; got %+v", revLock.Repos["foo"])
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
	// rev-parse HEAD for lock capture.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("cafebabe0000000000000000000000000000000\n")}, nil)

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

	// Verify pn-workspace.revs.json was written with the new rev.
	lockBytes, err := os.ReadFile(filepath.Join(root, RevLockFileName))
	if err != nil {
		t.Fatalf("read %s: %v", RevLockFileName, err)
	}
	var revLock RevLock
	if err := json.Unmarshal(lockBytes, &revLock); err != nil {
		t.Fatalf("parse %s: %v", RevLockFileName, err)
	}
	if revLock.Repos["foo"].Rev != "cafebabe0000000000000000000000000000000" {
		t.Errorf("locked rev: got %q, want cafebabe0000000000000000000000000000000", revLock.Repos["foo"].Rev)
	}
}

func TestUpdate_RespectsCancelledContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	err = w.Update(ctx, &bytes.Buffer{}, UpdateOptions{})
	if err == nil {
		t.Fatal("expected error on pre-cancelled context")
	}
	if !strings.Contains(err.Error(), "interrupted") && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error should reflect cancellation; got %q", err.Error())
	}
}
