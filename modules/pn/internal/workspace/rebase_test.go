package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// scriptNoRebase scripts ONE round of the rebase-in-progress probe
// (rebaseInProgress) answering "no rebase": both state paths are absolute and
// nonexistent. The FakeRunner consumes responses FIFO, so call this once per
// probe the code under test is expected to make (pre-probe, then post-probe).
func scriptNoRebase(t *testing.T, f *exec.FakeRunner, repoDir string) {
	t.Helper()
	absent := t.TempDir()
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--git-path", name},
			exec.Result{Stdout: []byte(filepath.Join(absent, name) + "\n")}, nil)
	}
}

// scriptRebaseInProgress scripts ONE probe round answering "rebase in
// progress": rebase-merge resolves to an existing directory (the probe
// short-circuits, so rebase-apply is never asked).
func scriptRebaseInProgress(t *testing.T, f *exec.FakeRunner, repoDir string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "rebase-merge")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--git-path", "rebase-merge"},
		exec.Result{Stdout: []byte(state + "\n")}, nil)
}

func abortArgs(repoDir string) []string {
	return []string{"-C", repoDir, "rebase", "--abort"}
}

// newRebaseFixture builds a one-repo workspace "foo" with an upstream, the
// given mutator, and a FakeRunner.
func newRebaseFixture(t *testing.T, m *fakeGitMutator) (w *Workspace, f *exec.FakeRunner, repoDir string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir = filepath.Join(root, "foo")
	stubGitOpener(t, map[string]*fakeGitReader{repoDir: {hasUpstreamVal: true}})
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{repoDir: m})
	f = exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, f, repoDir
}

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
	// One pre-probe per repo (a successful Sync is not post-probed).
	scriptNoRebase(t, f, filepath.Join(root, "bar"))
	scriptNoRebase(t, f, filepath.Join(root, "foo"))
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
// Default path: always try the sync; roll back a conflicting rebase.
//
// The old ahead-and-behind refusal (bd tc-0q3cp) is intentionally gone: a
// diverged branch often rebases cleanly (ahead 1 / behind 1 is the operator's
// real case), so the divergence predicate is no longer part of the abort
// decision. The in-progress probe is the authoritative signal instead.
// ---------------------------------------------------------------------------

// TestRebase_DivergedSyncsWithoutRefusal is the operator's real case: the
// branch is ahead 1 / behind 1 and a plain pull --rebase succeeds. No error,
// no abort, and no divergence query (rev-list) at all.
func TestRebase_DivergedSyncsWithoutRefusal(t *testing.T) {
	m := &fakeGitMutator{}
	w, f, repoDir := newRebaseFixture(t, m)
	// Even if rev-list were queried it would report divergence; it must not be.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"},
		exec.Result{Stdout: []byte("1\t1\n")}, nil)
	scriptNoRebase(t, f, repoDir)

	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{}); err != nil {
		t.Fatalf("expected a diverged-but-clean repo to rebase without error, got: %v", err)
	}
	if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "" {
		t.Errorf("expected exactly one Sync(Onto=\"\") call; got %v", m.syncOpts)
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must not run after a successful sync")
	}
	if calledWith(f, "git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}) {
		t.Errorf("divergence must no longer be queried in the default path")
	}
}

// TestRebase_AheadOnlyStillSyncs verifies the common post-drain-land steady
// state (ahead-only) syncs normally.
func TestRebase_AheadOnlyStillSyncs(t *testing.T) {
	m := &fakeGitMutator{}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir)

	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(m.syncOpts) != 1 || m.syncOpts[0].Onto != "" {
		t.Errorf("expected exactly one Sync(Onto=\"\") call for an ahead-only repo; got %v", m.syncOpts)
	}
}

// TestRebase_ConflictIsRolledBack: Sync's process fails AND a rebase is in
// progress afterwards -> `rebase --abort` runs, and the error wraps the Sync
// error and says it was rolled back / nothing was rebased.
func TestRebase_ConflictIsRolledBack(t *testing.T) {
	syncFail := errors.New("exit status 1")
	m := &fakeGitMutator{syncWaitErr: syncFail}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir)         // pre-probe
	scriptRebaseInProgress(t, f, repoDir) // post-probe
	f.AddResponse("git", abortArgs(repoDir), exec.Result{}, nil)

	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected an error for a conflicting rebase")
	}
	if !calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("expected `git rebase --abort` after the conflict")
	}
	if !errors.Is(err, syncFail) {
		t.Errorf("error must wrap the Sync error with %%w; got: %v", err)
	}
	for _, want := range []string{"foo", "rolled back", "nothing was rebased"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q; got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "STILL mid-rebase") {
		t.Errorf("error must not claim mid-rebase after a successful abort; got: %v", err)
	}
}

// TestRebase_SyncFailsWithoutRebaseState: Sync fails (network/hook/refusal)
// and no rebase is in progress afterwards -> no abort, original error.
func TestRebase_SyncFailsWithoutRebaseState(t *testing.T) {
	syncFail := errors.New("could not resolve host")
	m := &fakeGitMutator{syncWaitErr: syncFail}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir) // pre-probe
	scriptNoRebase(t, f, repoDir) // post-probe

	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected the sync error")
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must NOT run when no rebase is in progress")
	}
	if !errors.Is(err, syncFail) || !strings.Contains(err.Error(), "git pull --rebase --autostash in foo") {
		t.Errorf("expected the original sync error unchanged; got: %v", err)
	}
	if strings.Contains(err.Error(), "rolled back") {
		t.Errorf("must not claim a rollback when none happened; got: %v", err)
	}
}

// TestRebase_SyncStartFailureNotAborted: Sync() itself failing means the
// process never started: original error, no probe round beyond the pre-probe,
// no abort.
func TestRebase_SyncStartFailureNotAborted(t *testing.T) {
	startFail := errors.New("fork/exec: no such file")
	m := &fakeGitMutator{syncErr: startFail}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir) // pre-probe only

	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil || !errors.Is(err, startFail) {
		t.Fatalf("expected the start error to be returned; got: %v", err)
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must NOT run when Sync never started")
	}
}

// TestRebase_AlreadyInProgressIsNotTouched: a rebase already underway before
// the sync is the operator's own work -- neither Sync nor abort may run.
func TestRebase_AlreadyInProgressIsNotTouched(t *testing.T) {
	m := &fakeGitMutator{}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptRebaseInProgress(t, f, repoDir) // pre-probe

	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected an error when a rebase is already in progress")
	}
	if !strings.Contains(err.Error(), "rebase already in progress in foo") {
		t.Errorf("unexpected error text: %v", err)
	}
	if len(m.syncOpts) != 0 {
		t.Errorf("Sync must not run when a rebase is already in progress; got %v", m.syncOpts)
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must NEVER run on the operator's own in-progress rebase")
	}
}

// TestRebase_AbortFailureReportsStillMidRebase: when the rollback itself
// fails the error must NOT say "rolled back"; it names the recovery commands
// and carries the abort's stderr.
func TestRebase_AbortFailureReportsStillMidRebase(t *testing.T) {
	syncFail := errors.New("exit status 1")
	m := &fakeGitMutator{syncWaitErr: syncFail}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir)
	scriptRebaseInProgress(t, f, repoDir)
	f.AddResponse("git", abortArgs(repoDir),
		exec.Result{ExitCode: 128, Stderr: []byte("fatal: cannot abort: index.lock exists")},
		&exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128, Stderr: []byte("fatal: cannot abort: index.lock exists")}})

	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if strings.Contains(err.Error(), "rolled back") {
		t.Errorf("must NEVER say rolled back when the abort failed; got: %v", err)
	}
	for _, want := range []string{
		"STILL mid-rebase", "autostash",
		"git -C " + repoDir + " status", "git -C " + repoDir + " stash list",
		"rebase --continue", "rebase --abort", "index.lock exists",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q; got: %v", want, err)
		}
	}
	if !errors.Is(err, syncFail) {
		t.Errorf("error should still wrap the Sync error; got: %v", err)
	}
}

// TestRebase_CancelledContextDoesNotAbort: if the context was cancelled when
// the Sync failed, the repo may be mid-rebase for a reason we cannot judge;
// do not probe or abort, and say so honestly.
func TestRebase_CancelledContextDoesNotAbort(t *testing.T) {
	syncFail := errors.New("signal: killed")
	ctx, cancel := context.WithCancel(context.Background())
	m := &fakeGitMutator{syncWaitErr: syncFail, onSync: cancel}
	w, f, repoDir := newRebaseFixture(t, m)
	scriptNoRebase(t, f, repoDir) // pre-probe only

	err := w.Rebase(ctx, &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must NOT run after cancellation")
	}
	if !strings.Contains(err.Error(), "may be mid-rebase") || !errors.Is(err, syncFail) {
		t.Errorf("expected an honest mid-rebase warning wrapping the sync error; got: %v", err)
	}
	if strings.Contains(err.Error(), "rolled back") {
		t.Errorf("must not claim a rollback; got: %v", err)
	}
}

// TestRebase_ProbeErrorAfterFailureDoesNotAbort: a post-probe error is
// "state unknown" -> do not abort, return the original error. A pre-probe
// error proceeds to Sync.
func TestRebase_ProbeErrorDoesNotAbort(t *testing.T) {
	syncFail := errors.New("exit status 1")
	m := &fakeGitMutator{syncWaitErr: syncFail}
	w, f, repoDir := newRebaseFixture(t, m)
	// No probe responses scripted: the FakeRunner errors on both probes.
	err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil || !errors.Is(err, syncFail) {
		t.Fatalf("expected the original sync error; got: %v", err)
	}
	if len(m.syncOpts) != 1 {
		t.Errorf("a pre-probe error must proceed to Sync; got %d calls", len(m.syncOpts))
	}
	if calledWith(f, "git", abortArgs(repoDir)) {
		t.Errorf("rebase --abort must not run when the rebase state is unknown")
	}
}

// TestRebaseInProgress_RelativeGitPathAnchoredOnRepoDir: git answers with a
// path relative to the -C directory; the probe must anchor it on repoDir (not
// this process's cwd).
func TestRebaseInProgress_RelativeGitPathAnchoredOnRepoDir(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "foo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git", "rebase-apply"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--git-path", "rebase-merge"},
		exec.Result{Stdout: []byte(".git/rebase-merge\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--git-path", "rebase-apply"},
		exec.Result{Stdout: []byte(".git/rebase-apply\n")}, nil)
	ws := &Workspace{root: root, runner: f}

	got, err := ws.rebaseInProgress(context.Background(), repoDir)
	if err != nil || !got {
		t.Fatalf("expected in-progress via rebase-apply (relative path), got %v, %v", got, err)
	}
}

// TestRebase_OntoIgnoresDivergence verifies the --onto path runs no
// in-progress probe, no rollback and no divergence query: Onto is the
// deliberate, operator-directed reconciliation mechanism and still leaves a
// repo mid-rebase on conflict.
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

	// Nothing scripted at all -- the Onto path must never run any raw git.
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
	if n := len(f.Calls()); n != 0 {
		t.Errorf("--onto must run no probe/abort/rev-list; got %d runner calls: %v", n, f.Calls())
	}
}

// TestRebase_OntoConflictLeavesMidRebase pins the deliberate asymmetry: a
// failing --onto sync returns its error and does NOT abort.
func TestRebase_OntoConflictLeavesMidRebase(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")
	stubGitOpener(t, map[string]*fakeGitReader{repoDir: {refExists: map[string]bool{"main": true}}})
	m := &fakeGitMutator{syncWaitErr: errors.New("exit status 1")}
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{repoDir: m})
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{Onto: "main"}); err == nil {
		t.Fatalf("expected the onto sync error")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("--onto must not probe or abort; got %v", f.Calls())
	}
}

// TestRebase_RealGit_ConflictingDivergedRebaseIsRestored drives real git: a
// genuinely conflicting ahead-1/behind-1 branch. Rebase must fail, and the
// repo must be restored (same branch tip, clean tree, no rebase state dir).
func TestRebase_RealGit_ConflictingDivergedRebaseIsRestored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")
	initRealRepo(t, repoDir)
	bare := setupLocalBareRemoteNamed(t, repoDir, "origin")

	// A peer pushes a conflicting change to README.md.
	peer := filepath.Join(t.TempDir(), "peer")
	runGitT(t, root, "clone", "-q", bare, peer)
	runGitT(t, peer, "config", "user.email", "p@p")
	runGitT(t, peer, "config", "user.name", "p")
	addCommit(t, peer, "README.md", "peer\n", "peer change")
	runGitT(t, peer, "push", "-q", "origin", "main")

	// Local diverges with a conflicting change to the same file.
	tip := addCommit(t, repoDir, "README.md", "local\n", "local change")

	w, err := Open(root, exec.NewRealRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Rebase(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, RebaseOptions{})
	if err == nil {
		t.Fatalf("expected the conflicting rebase to fail")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("expected a rolled-back error; got: %v", err)
	}
	if got := headRev(t, repoDir); got != tip {
		t.Errorf("branch tip not restored: want %s, got %s", tip, got)
	}
	if st := runGitT(t, repoDir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean after rollback:\n%s", st)
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		p := runGitT(t, repoDir, "rev-parse", "--git-path", d)
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoDir, p)
		}
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("%s still present after rollback", d)
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
