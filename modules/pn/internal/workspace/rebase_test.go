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

	// fetch + pull --rebase --autostash migrated onto Fetcher.Fetch +
	// Syncer.Sync (bead pg2-8bfb5).
	mBar := &fakeGitMutator{}
	mFoo := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{
		filepath.Join(root, "bar"): mBar,
		filepath.Join(root, "foo"): mFoo,
	})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	for name, m := range map[string]*fakeGitMutator{"bar": mBar, "foo": mFoo} {
		if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "" {
			t.Errorf("%s: expected exactly one Sync(Onto=\"\") call; got %v", name, m.syncOpts)
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

	m := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{filepath.Join(root, "foo"): m})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(m.syncOpts) != 0 {
		t.Errorf("expected no Sync call when upstream missing; got %v", m.syncOpts)
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

	mBar := &fakeGitMutator{}
	mFoo := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{
		filepath.Join(root, "bar"): mBar,
		filepath.Join(root, "foo"): mFoo,
	})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{Onto: "main"}); err != nil {
		t.Fatalf("Rebase with Onto: %v", err)
	}
	for name, m := range map[string]*fakeGitMutator{"bar": mBar, "foo": mFoo} {
		if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "main" {
			t.Errorf("%s: expected exactly one Sync(Onto=\"main\") call; got %v", name, m.syncOpts)
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

	mBar := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{filepath.Join(root, "bar"): mBar})

	f := exec.NewFakeRunner()
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
	if len(mBar.syncOpts) != 1 || mBar.syncOpts[0].Onto != "feature" {
		t.Errorf("expected bar to be rebased onto feature; got %v", mBar.syncOpts)
	}
}
