package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
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

	// hasUpstream is migrated onto x/gitclient's RefReader.HasUpstream (bead
	// pg2-oxle0); both repos report an upstream via the stubbed gitReader.
	stubGitOpener(t, map[string]*fakeGitReader{
		filepath.Join(root, "bar"): {hasUpstreamVal: true},
		filepath.Join(root, "foo"): {hasUpstreamVal: true},
	})

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "fetch"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "fetch"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "pull", "--rebase", "--autostash"}, exec.Result{}, nil)

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
		t.Errorf("expected 4 calls (fetch+pull per repo), got %d", len(calls))
	}
	// The rebase commands (fetch, pull) stream.
	for _, c := range calls {
		last := c.Args[len(c.Args)-1]
		if (last == "fetch" || last == "--autostash") && c.Opts.Stdout == nil {
			t.Errorf("rebase command should stream output (Opts.Stdout set); got %v", c.Args)
		}
	}
}

// TestRebase_TerminalFlagSuppressesWarning verifies that passing Terminal via
// RebaseOptions suppresses the no-terminal warning even when config has no terminal.
func TestRebase_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	// upstream check fails — no rebase (we just care about the warning).
	stubGitOpener(t, map[string]*fakeGitReader{filepath.Join(root, "foo"): {hasUpstreamVal: false}})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

func TestRebase_SkipsWithoutUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	stubGitOpener(t, map[string]*fakeGitReader{filepath.Join(root, "foo"): {hasUpstreamVal: false}})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "fetch" || a == "--autostash" {
				t.Errorf("expected no rebase call when upstream missing; got %v", c.Args)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Rebase with Onto field (rebase <branch> local-ref form)
// ---------------------------------------------------------------------------

// TestRebase_OntoLocalRef verifies that when RebaseOptions.Onto is set the
// function resolves the ref, runs git rebase --autostash <onto>, and skips
// fetch/pull entirely.
func TestRebase_OntoLocalRef(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	// ref resolution (resolveRef, migrated onto x/gitclient's RefReader.RefExists
	// — bead pg2-oxle0) succeeds for both repos.
	stubGitOpener(t, map[string]*fakeGitReader{
		filepath.Join(root, "bar"): {refExists: map[string]bool{"main": true}},
		filepath.Join(root, "foo"): {refExists: map[string]bool{"main": true}},
	})

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rebase", "--autostash", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rebase", "--autostash", "main"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{Onto: "main"}); err != nil {
		t.Fatalf("Rebase with Onto: %v", err)
	}
	calls := f.Calls()
	// 2 rebase calls; no fetch or pull calls.
	if len(calls) != 2 {
		t.Errorf("expected 2 calls (rebase per repo), got %d", len(calls))
	}
	for _, c := range calls {
		for _, a := range c.Args {
			if a == "fetch" || a == "pull" {
				t.Errorf("rebase <branch> form must not call fetch/pull; got %v", c.Args)
			}
		}
	}
}

// TestRebase_OntoSkipsMissingRef verifies that a repo where the ref does not
// resolve is skipped with a stderr notice rather than aborting the whole run.
func TestRebase_OntoSkipsMissingRef(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	// "bar" ref resolves OK; "foo" ref does NOT (no entry -> RefExists false, nil).
	stubGitOpener(t, map[string]*fakeGitReader{
		filepath.Join(root, "bar"): {refExists: map[string]bool{"feature": true}},
		filepath.Join(root, "foo"): {},
	})

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rebase", "--autostash", "feature"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	// Must not return an error — skip the missing-ref repo.
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{Onto: "feature"}); err != nil {
		t.Fatalf("Rebase with missing ref: expected no error (skip), got %v", err)
	}
	// A notice must appear on stderr.
	if !strings.Contains(errOut.String(), "foo") {
		t.Errorf("expected stderr notice mentioning skipped repo 'foo'; got: %q", errOut.String())
	}
	// No rebase should have been attempted for "foo".
	for _, c := range f.Calls() {
		if len(c.Args) >= 2 && c.Args[1] == filepath.Join(root, "foo") {
			for _, a := range c.Args {
				if a == "--autostash" {
					t.Errorf("should not have rebased foo; got %v", c.Args)
				}
			}
		}
	}
}
