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
// Ahead-and-behind guard after fetch (bd tc-0q3cp, duplicating bd tc-p08nv's
// `pnwf sync-fetch` preflight down into `pn workspace rebase` itself so a
// bare/direct invocation is protected too).
// ---------------------------------------------------------------------------

// TestRebase_RefusesAheadAndBehindAfterFetch verifies that when a repo's
// branch is genuinely diverged from its upstream right after the fetch (both
// ahead and behind), Rebase returns an error naming the repo and the
// ahead/behind counts instead of running `pull --rebase --autostash` into a
// content conflict.
func TestRebase_RefusesAheadAndBehindAfterFetch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	stubGitOpener(t, map[string]*fakeGitReader{repoDir: {hasUpstreamVal: true}})

	m := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{repoDir: m})

	f := exec.NewFakeRunner()
	// Ahead 24, behind 18 -- the exact shape of the tc-0q3cp incident.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"},
		exec.Result{Stdout: []byte("24\t18\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	err = w.Rebase(context.Background(), &out, &errOut, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected Rebase to refuse an ahead-and-behind repo, got no error")
	}
	for _, want := range []string{"foo", "24", "18"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q; got: %v", want, err)
		}
	}
	if len(m.syncOpts) != 0 {
		t.Errorf("expected no Sync (pull --rebase) call for a diverged repo; got %v", m.syncOpts)
	}
}

// TestRebase_AheadOnlyStillSyncs verifies the guard does not fire for a
// one-sided divergence (ahead-only, the common post-drain-land steady state):
// pull --rebase must still run normally.
func TestRebase_AheadOnlyStillSyncs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	stubGitOpener(t, map[string]*fakeGitReader{repoDir: {hasUpstreamVal: true}})

	m := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{repoDir: m})

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"},
		exec.Result{Stdout: []byte("3\t0\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "" {
		t.Errorf("expected exactly one Sync(Onto=\"\") call for an ahead-only repo; got %v", m.syncOpts)
	}
}

// TestRebase_OntoIgnoresDivergence verifies the ahead-and-behind guard does
// NOT apply to the explicit --onto path: Onto is itself the deliberate
// reconciliation mechanism, so it must still run even when the repo is
// genuinely diverged.
func TestRebase_OntoIgnoresDivergence(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	stubGitOpener(t, map[string]*fakeGitReader{repoDir: {refExists: map[string]bool{"origin/main": true}}})

	m := &fakeGitMutator{}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{repoDir: m})

	// No rev-list stubbed at all -- the Onto path must never even query it.
	f := exec.NewFakeRunner()

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Rebase(context.Background(), &out, &errOut, RebaseOptions{Onto: "origin/main"}); err != nil {
		t.Fatalf("Rebase with Onto: %v", err)
	}
	if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "origin/main" {
		t.Errorf("expected exactly one Sync(Onto=\"origin/main\") call; got %v", m.syncOpts)
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
