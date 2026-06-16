package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// TestWorkspaceUpdate_WritesEventLog: `pn workspace update` opens the JSONL
// event log at ${XDG_STATE_HOME}/pn/events.jsonl and the file is non-empty
// after a successful run (events go to the file, not stdout).
func TestWorkspaceUpdate_WritesEventLog(t *testing.T) {
	// Isolate the event-log location.
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Build a workspace with a terminal + one repo, and script the runner so
	// Update runs to completion (no upstream → dirty-probes, upstream check,
	// update-locks, rev-parse HEAD).
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(`[workspace]
name = "test"
terminal = "myterm"

[repos.myterm]
url = "github:owner/myterm"
`), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	fr := exec.NewFakeRunner()
	repo := filepath.Join(root, "myterm")
	fr.AddResponse("git", []string{"-C", repo, "diff", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "--abbrev-ref", "@{u}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	fr.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "HEAD"},
		exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	w, err := workspace.Open(root, fr)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	t.Cleanup(w.Close)

	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return w, nil }
	t.Cleanup(func() { openWorkspace = orig })

	stdout, _, err := runCobraCmd(t, []string{"update"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// The JSONL event stream must go to the dedicated file, never to stdout.
	eventsPath := filepath.Join(stateHome, "pn", "events.jsonl")
	info, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("expected event log at %s: %v", eventsPath, err)
	}
	if info.Size() == 0 {
		t.Fatalf("event log %s is empty; expected emitted events", eventsPath)
	}

	// Stdout must not contain the JSONL stream (no run_start/run_end markers).
	for _, marker := range []string{"run_start", "run_end", "project_result"} {
		if strings.Contains(stdout, marker) {
			t.Errorf("event marker %q leaked into stdout: %q", marker, stdout)
		}
	}
}
