package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// readEventLines parses a JSONL event file into a slice of records.
func readEventLines(t *testing.T, p string) []map[string]any {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open events %s: %v", p, err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad JSON %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// firstByKind returns the first record whose kind field equals k, or nil.
func firstByKind(recs []map[string]any, k string) map[string]any {
	for _, m := range recs {
		if m["kind"] == k {
			return m
		}
	}
	return nil
}

// resultFor returns the project_result record whose name field equals name.
func resultFor(recs []map[string]any, name string) map[string]any {
	for _, m := range recs {
		if m["kind"] == "project_result" && m["name"] == name {
			return m
		}
	}
	return nil
}

// TestUpdate_EmitsRunAndProjectEvents: Update threads a nil-safe logger and
// emits run_start, one project_result per repo (ok / failed), and run_end with
// the failure tally. Here foo succeeds and bar's pull fails (→ outcome failed,
// level error). run_end is level error with failed:1.
func TestUpdate_EmitsRunAndProjectEvents(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	bar := filepath.Join(root, "bar")

	// foo: clean, has upstream, pull/locks/push all succeed.
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "push"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("deadbeef0000000000000000000000000000000\n")}, nil)

	// bar: clean, has upstream, but pull fails → outcome failed (failed_step pull).
	f.AddResponse("git", []string{"-C", bar, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", bar, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", bar, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", bar, "pull", "--rebase", "--autostash"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(eventsPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}

	err = w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{InPlace: true, Log: lw})
	_ = lw.Close()
	if err == nil {
		t.Fatal("expected error reporting failures, got nil")
	}

	recs := readEventLines(t, eventsPath)

	// run_start (info)
	rs := firstByKind(recs, "run_start")
	if rs == nil {
		t.Fatalf("no run_start event: %v", recs)
	}
	if rs["level"] != "info" {
		t.Errorf("run_start level = %v, want info", rs["level"])
	}
	if pj, ok := rs["projects"].(float64); !ok || int(pj) != 2 {
		t.Errorf("run_start projects = %v, want 2", rs["projects"])
	}

	// project_result for foo (info / ok)
	rf := resultFor(recs, "foo")
	if rf == nil {
		t.Fatalf("no project_result for foo: %v", recs)
	}
	if rf["level"] != "info" || rf["outcome"] != "ok" {
		t.Errorf("foo result = %v, want level info outcome ok", rf)
	}

	// project_result for bar (error / failed)
	rb := resultFor(recs, "bar")
	if rb == nil {
		t.Fatalf("no project_result for bar: %v", recs)
	}
	if rb["level"] != "error" || rb["outcome"] != "failed" {
		t.Errorf("bar result = %v, want level error outcome failed", rb)
	}

	// run_end (error, failed:1)
	re := firstByKind(recs, "run_end")
	if re == nil {
		t.Fatalf("no run_end event: %v", recs)
	}
	if re["level"] != "error" {
		t.Errorf("run_end level = %v, want error", re["level"])
	}
	if fc, ok := re["failed"].(float64); !ok || int(fc) != 1 {
		t.Errorf("run_end failed = %v, want 1", re["failed"])
	}
}

// TestUpdate_NilLogIsSafe: a nil Log must not break the run (no events file,
// no panic).
func TestUpdate_NilLogIsSafe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{InPlace: true}); err != nil {
		t.Fatalf("Update with nil Log: %v", err)
	}
}
