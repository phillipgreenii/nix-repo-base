package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestParseULTransient covers the bash↔Go boundary parser: update-locks.sh
// emits a machine-readable "UL_RESULT transient=<N>" line on its stdout (see
// ul_finalize), and pn recovers the count the exit code hides (ADR 0020). The
// LAST such line wins (a wrapped step could echo the token), and absence /
// garbage yields 0.
func TestParseULTransient(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"absent", "=== Update Summary ===\n  Transient: 0\n✓ done\n", 0},
		{"present", "noise\nUL_RESULT transient=2\n✓ done\n", 2},
		{"last-wins", "UL_RESULT transient=1\nUL_RESULT transient=4\n", 4},
		{"trailing-fields", "UL_RESULT transient=3 failed=0\n", 3},
		{"empty", "", 0},
		{"garbage", "UL_RESULT transient=abc\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseULTransient([]byte(tc.in)); got != tc.want {
				t.Errorf("parseULTransient(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestUpdateViaWorktree_TransientWarnEvent: a repo whose update-locks.sh reports
// transient steps (stdout carries UL_RESULT transient=2) integrates cleanly
// (outcome ok) but its project_result MUST be emitted at warn level with a
// transient:2 field, so an automated `pn workspace update` that watches the
// event stream can see the otherwise-silent transient churn (ADR 0020).
func TestUpdateViaWorktree_TransientWarnEvent(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	// Steps 1–3.
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	// Step 4: update-locks.sh succeeds but reports 2 transient steps.
	f.AddResponse("./update-locks.sh", nil,
		exec.Result{Stdout: []byte("=== Update Summary ===\n  Transient: 2\nUL_RESULT transient=2\n✓ done\n")}, nil)
	// Steps 5–7.
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"}, exec.Result{}, nil)
	// Step 8–9: clean main → ff-merge, remove worktree, delete branch.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-D", branch}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(eventsPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x", Log: lw}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	_ = lw.Close()

	rf := resultFor(readEventLines(t, eventsPath), "foo")
	if rf == nil {
		t.Fatal("no project_result for foo")
	}
	if rf["level"] != "warn" {
		t.Errorf("level = %v, want warn", rf["level"])
	}
	if rf["outcome"] != "ok" {
		t.Errorf("outcome = %v, want ok", rf["outcome"])
	}
	if tv, ok := rf["transient"].(float64); !ok || int(tv) != 2 {
		t.Errorf("transient = %v, want 2", rf["transient"])
	}
}

// TestUpdateInPlace_TransientWarnEvent: the same warn/transient surfacing in the
// --in-place flow (which emits its project_result inline).
func TestUpdateInPlace_TransientWarnEvent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	foo := filepath.Join(root, "foo")
	mkUpdateLocks(t, foo) // existence gate: update-locks.sh must be present to run
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{Stdout: []byte("UL_RESULT transient=3\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(eventsPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{InPlace: true, Log: lw}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	_ = lw.Close()

	rf := resultFor(readEventLines(t, eventsPath), "foo")
	if rf == nil {
		t.Fatal("no project_result for foo")
	}
	if rf["level"] != "warn" {
		t.Errorf("level = %v, want warn", rf["level"])
	}
	if rf["outcome"] != "ok" {
		t.Errorf("outcome = %v, want ok", rf["outcome"])
	}
	if tv, ok := rf["transient"].(float64); !ok || int(tv) != 3 {
		t.Errorf("transient = %v, want 3", rf["transient"])
	}
}
