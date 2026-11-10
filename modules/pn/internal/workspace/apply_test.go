package workspace

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
)

// applyLock is the lock with edge leaf→dep alias "dep-input", used by tests
// that need overrideInputArgsFor to emit the override.
const applyLock = `{
  "order": ["dep", "leaf"],
  "repos": {
    "dep":  {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [{"consumer": "leaf", "alias": "dep-input", "target": "dep"}]
}`

// TestApply_RecordsTheOverridesItEmitted is the WIRING test for the pg2-14yqh
// ruling. `pb`'s gate condition 2 is skipped for a repo the apply overrode, so the
// evidence the gate trusts is only as good as its agreement with what nix was
// actually told. This asserts the agreement END TO END: the `--override-input`
// value in the emitted build command and the value recorded in the applied-state
// MUST be the same string, for the same repo.
//
// Deriving the record from the SAME resolution as the flags is what makes that
// hold; a second, independent derivation at markApplied time would re-stat the
// filesystem and could disagree.
func TestApply_RecordsTheOverridesItEmitted(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	if err := e.apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The flag value nix was actually given, read back off the recorded build call.
	var emitted string
	for _, c := range e.f.significant() {
		if !isBuildCall(c) {
			continue
		}
		for i, a := range c.Args {
			if a == "--override-input" && i+2 < len(c.Args) {
				emitted = c.Args[i+2]
			}
		}
	}
	if emitted == "" {
		t.Fatalf("apply emitted no --override-input; calls:\n%s", dumpCalls(e.f.significant()))
	}
	for _, name := range []string{"dep", "leaf"} {
		st, ok, err := readAppliedState(e.w.appliedStateKeyPath(name))
		if err != nil || !ok {
			t.Fatalf("read applied state for %s: ok=%v err=%v", name, ok, err)
		}
		got, wasOverridden := st.OverriddenInputs["dep"]
		if !wasOverridden {
			t.Fatalf("%s record: overridden_inputs = %v, want an entry for dep — the apply DID override it",
				name, st.OverriddenInputs)
		}
		if got != emitted {
			t.Fatalf("%s record: overridden_inputs[dep] = %q but the build command carried %q; the record "+
				"MUST describe the build that ran", name, got, emitted)
		}
	}
}

// calledPkillFsmonitor reports whether the runner saw the
// `pkill -f 'git fsmonitor--daemon'` invocation.
func calledPkillFsmonitor(calls []exec.Call) bool {
	for _, c := range calls {
		if c.Name == "pkill" && len(c.Args) == 2 && c.Args[0] == "-f" && c.Args[1] == "git fsmonitor--daemon" {
			return true
		}
	}
	return false
}

const (
	gitExecA = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.43.0/libexec/git-core\n"
	gitExecC = "/nix/store/cccccccccccccccccccccccccccccccc-git-2.45.0/libexec/git-core\n"
)

// scriptExecPaths scripts `git --exec-path` to answer before then after.
func scriptExecPaths(sr *scriptedRunner, before, after string) {
	sr.add("git", []string{"--exec-path"}, exec.Result{Stdout: []byte(before)}, nil)
	sr.add("git", []string{"--exec-path"}, exec.Result{Stdout: []byte(after)}, nil)
}

// TestApply_RestartsFsmonitorWhenGitVersionChanges asserts that pkill is invoked
// when the git version differs before vs after the rebuild.
func TestApply_RestartsFsmonitorWhenGitVersionChanges(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	// git --exec-path: old then new (changed store path) — FIFO consumption.
	scriptExecPaths(sr, gitExecA, gitExecC)
	sr.add("pkill", []string{"-f", "git fsmonitor--daemon"}, exec.Result{}, nil)
	if err := e.apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !calledPkillFsmonitor(sr.Calls()) {
		t.Errorf("expected pkill of fsmonitor daemon when git version changed; calls:\n%+v", sr.Calls())
	}
}

// TestApply_RestartsFsmonitorWhenGitBinaryChangesSameVersion asserts that pkill
// is invoked when the git binary changes (its --exec-path / store path) even
// though the version string is identical. A same-version Nix rebuild swaps the
// binary store path, and the running daemon would otherwise keep executing the
// stale binary — keying on the version string alone misses this case.
func TestApply_RestartsFsmonitorWhenGitBinaryChangesSameVersion(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	// git --exec-path: same version 2.54.0, different store path before/after.
	scriptExecPaths(sr,
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-git-2.54.0/libexec/git-core\n",
		"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-git-2.54.0/libexec/git-core\n")
	sr.add("pkill", []string{"-f", "git fsmonitor--daemon"}, exec.Result{}, nil)
	if err := e.apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !calledPkillFsmonitor(sr.Calls()) {
		t.Errorf("expected pkill of fsmonitor daemon when git binary path changed (same version); calls:\n%+v", sr.Calls())
	}
}

// TestApply_NoFsmonitorRestartWhenGitUnchanged asserts that pkill is NOT invoked
// when the git version is identical before and after the rebuild.
func TestApply_NoFsmonitorRestartWhenGitUnchanged(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	// git --exec-path: same path both times — git binary unchanged.
	scriptExecPaths(sr, gitExecA, gitExecA)
	if err := e.apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calledPkillFsmonitor(sr.Calls()) {
		t.Errorf("pkill must not run when git version is unchanged; calls:\n%+v", sr.Calls())
	}
}

// seedSkippableState records applied state for both repos so needsRebuild finds
// a clean tree whose HEAD matches the recorded ref (the fake answers `rev-parse`
// with "h", and the scripted answers below give leaf "abc").
func seedSkippableState(t *testing.T, e *asUserEnv, sr *scriptedRunner) {
	t.Helper()
	for i := 0; i < 3; i++ {
		sr.add("git", []string{"-C", e.leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	}
	if err := writeAppliedState(e.leafDir, AppliedState{AppliedRef: "abc"}); err != nil {
		t.Fatalf("seed applied state: %v", err)
	}
	if err := writeAppliedState(e.depDir, AppliedState{AppliedRef: "h"}); err != nil {
		t.Fatalf("seed applied state: %v", err)
	}
}

// TestApply_NoFsmonitorRestartOnSkippedRebuild asserts that the skip-rebuild
// (no-op) path neither checks the git version nor kills the daemon.
func TestApply_NoFsmonitorRestartOnSkippedRebuild(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	seedSkippableState(t, e, sr)
	if err := e.w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, c := range sr.Calls() {
		if isBuildCall(c) {
			t.Fatalf("the rebuild was not skipped; calls:\n%+v", sr.Calls())
		}
	}
	if calledPkillFsmonitor(sr.Calls()) {
		t.Errorf("pkill must not run on the skip-rebuild path; calls:\n%+v", sr.Calls())
	}
	for _, c := range sr.Calls() {
		if c.Name == "git" && len(c.Args) == 1 && c.Args[0] == "--exec-path" {
			t.Errorf("git --exec-path must not be probed on the skip-rebuild path; calls:\n%+v", sr.Calls())
		}
	}
}

// TestApply_UntrustedAborts is the regression test for bead pg2-x2q6o: the build
// and activate commands are argv taken from the discovered pn-workspace.toml and
// executed by Apply, so an untrusted workspace root MUST abort before ANY
// execution — not even the nix daemon check or the build runs. Closes the
// residual the ADR-0019 hook trust gate left open.
func TestApply_UntrustedAborts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty trust store ⇒ untrusted
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), asUserTOML)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	withAsUserSeams(t, w)
	var out bytes.Buffer
	err = w.Apply(context.Background(), &out, ApplyOptions{Force: true, Builder: "darwin-rebuild"})
	if !errors.Is(err, trust.ErrUntrusted) {
		t.Fatalf("Apply of an untrusted workspace must abort with ErrUntrusted; got %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("untrusted apply must not exec anything (not even the daemon check or the build); got %d calls: %+v", len(f.Calls()), f.Calls())
	}
	if strings.Contains(out.String(), "darwin-rebuild build") {
		t.Errorf("an untrusted apply printed the build command:\n%s", out.String())
	}
}

func TestApply_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), asUserTOML)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	withAsUserSeams(t, w)
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "darwin-rebuild build --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing the build command:\n%s", out.String())
	}
	if strings.Contains(out.String(), "nix fmt") {
		t.Errorf("dry-run output should not contain 'nix fmt' (fmt is now a separate command):\n%s", out.String())
	}
}

func TestAllRepoDirs_SkipsMissingClones(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf") // cloned
	// "dep" declared but NOT cloned on disk.
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	got := w.allRepoDirs(nil)
	leafDir := filepath.Join(root, "leaf")
	want := []repoDir{{name: "leaf", keyPath: leafDir, gitDir: leafDir}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allRepoDirs should skip missing clones: got %#v want %#v", got, want)
	}
}

// TestAllRepoDirs_OverrideKeepsCanonicalStoreKey pins the pg2-k43p.3 fix: under
// an override, gitDir follows the override checkout but keyPath stays canonical
// so the applied-state store is keyed the same way Info reads it.
func TestAllRepoDirs_OverrideKeepsCanonicalStoreKey(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	override := t.TempDir() // stand-in coordinated-worktree checkout of "leaf"
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	got := w.allRepoDirs(map[string]string{"leaf": override})
	// name is the [repos.<key>] key regardless of the override: markApplied maps it
	// through the lock's edge set, which is keyed on the repo key, not on a path.
	want := []repoDir{{name: "leaf", keyPath: filepath.Join(root, "leaf"), gitDir: override}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("override must keep the canonical store key while pointing git at the override: got %#v want %#v", got, want)
	}
}

// argsHaveConsecutive reports whether args contains a, immediately followed by b.
func argsHaveConsecutive(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// statusPorcelainCallStats counts, across recorded calls, how many were
// `git ... status --porcelain` (all) and how many of those disabled the
// filesystem monitor via `-c core.fsmonitor=false` (fsmonitorOff).
func statusPorcelainCallStats(calls []exec.Call) (all, fsmonitorOff int) {
	for _, c := range calls {
		if c.Name != "git" || !argsHaveConsecutive(c.Args, "status", "--porcelain") {
			continue
		}
		all++
		if argsHaveConsecutive(c.Args, "-c", "core.fsmonitor=false") {
			fsmonitorOff++
		}
	}
	return all, fsmonitorOff
}

// TestApply_StatusProbesDisableFsmonitor is the regression test for bead
// pg2-0sa8p: apply's state probes (needsRebuild + markApplied) must run
// `git status --porcelain` with the filesystem monitor disabled
// (`-c core.fsmonitor=false`), so a wedged `git fsmonitor--daemon` can never
// hang apply on the `.git/fsmonitor--daemon.ipc` socket. Every status probe
// apply issues must carry the flag.
func TestApply_StatusProbesDisableFsmonitor(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	// git --exec-path identical before/after ⇒ no fsmonitor daemon restart.
	scriptExecPaths(sr, gitExecA, gitExecA)
	// needsRebuild consumes the first (dirty ⇒ rebuild), markApplied the second —
	// exercising BOTH probe sites.
	status := []string{"-C", e.leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}
	sr.add("git", status, exec.Result{Stdout: []byte("M file\n")}, nil)
	sr.add("git", status, exec.Result{Stdout: []byte("")}, nil)

	if err := e.w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	built := false
	for _, c := range sr.Calls() {
		built = built || isBuildCall(c)
	}
	if !built {
		t.Fatalf("the dirty tree did not trigger a build; calls:\n%+v", sr.Calls())
	}

	all, off := statusPorcelainCallStats(sr.Calls())
	if all == 0 {
		t.Fatalf("expected apply to issue git status --porcelain probes; calls:\n%+v", sr.Calls())
	}
	if off != all {
		t.Errorf("all %d git status --porcelain probes must disable fsmonitor (-c core.fsmonitor=false); only %d did; calls:\n%+v", all, off, sr.Calls())
	}
}

// TestApply_CapturesHeadBeforeBuild is the regression test for bead pg2-0782j:
// each repo's HEAD recorded into applied-state (AppliedRef) MUST be captured
// BEFORE the build invocation runs (eval-time HEAD — the HEAD nix actually
// evaluated when it resolved this repo's --override-input from its local
// clone), never by re-reading git AFTER the build completes. A commit landing
// in the local clone during the build window (which can be minutes wide) must
// not be recorded as applied when the activated system was evaluated before
// that commit existed.
//
// Force: true makes needsRebuild return immediately without touching git (see
// its early `if force { return true, nil }`), so across the whole Apply call
// there is exactly ONE `git -C leafDir rev-parse HEAD` invocation either way —
// this test does not change how many times that call happens, only WHEN. That
// is deliberate: a value-only assertion (AppliedRef == the scripted value)
// would pass against BOTH the pre-fix and post-fix code. Only the call's INDEX
// relative to the build command's index distinguishes "before the build"
// (fixed) from "after the build" (the bug).
func TestApply_CapturesHeadBeforeBuild(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	// git --exec-path identical before/after ⇒ no fsmonitor daemon restart.
	scriptExecPaths(sr, gitExecA, gitExecA)
	if err := e.apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls := sr.Calls()
	headIdx, buildIdx := -1, -1
	for i, c := range calls {
		if c.Name == "git" && len(c.Args) == 4 && c.Args[0] == "-C" && c.Args[1] == e.leafDir &&
			c.Args[2] == "rev-parse" && c.Args[3] == "HEAD" {
			headIdx = i
		}
		if isBuildCall(c) {
			buildIdx = i
		}
	}
	if headIdx == -1 {
		t.Fatalf("expected a `git -C %s rev-parse HEAD` call; calls:\n%+v", e.leafDir, calls)
	}
	if buildIdx == -1 {
		t.Fatalf("expected the build (darwin-rebuild build) call; calls:\n%+v", calls)
	}
	if headIdx > buildIdx {
		t.Errorf("`git rev-parse HEAD` for %s ran AFTER the build (call index %d vs build's index %d) — "+
			"AppliedRef must be captured BEFORE the build runs (eval-time HEAD), not by re-reading HEAD "+
			"once the build has already completed; calls:\n%+v", e.leafDir, headIdx, buildIdx, calls)
	}
}

// applyWithLingeringGrandchild runs Apply with a user build that is a REAL
// script: it backgrounds a grandchild holding the inherited stdout for longer
// than the (shortened) kill grace, then exits with `exitCode`. Everything else
// the apply runs is the fake.
func applyWithLingeringGrandchild(t *testing.T, exitCode int) (*Workspace, string, error) {
	t.Helper()
	e, sr := newScriptedEnv(t, asUserTOML)
	sr.add("git", []string{"-C", e.leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)
	e.realBuild(exec.NewRealRunnerWithKillGrace(300*time.Millisecond),
		"(sleep 3) &\necho activated\nexit "+strconv.Itoa(exitCode)+"\n")
	out := &bytes.Buffer{}
	err := e.apply(context.Background(), out)
	return e.w, out.String(), err
}

// pg2-gb5ng: a build that exits 0 while a descendant keeps its output open past
// WaitDelay is a SUCCESSFUL build: it warns, and markApplied still runs.
func TestApply_ExitZeroWithLingeringGrandchildStillMarksApplied(t *testing.T) {
	w, out, err := applyWithLingeringGrandchild(t, 0)
	if err != nil {
		t.Fatalf("Apply must succeed when the command exited 0, got: %v", err)
	}
	if !strings.Contains(out, "background process kept its output open") {
		t.Errorf("apply output must carry the warning; got:\n%s", out)
	}
	st, ok, rerr := readAppliedState(w.appliedStateKeyPath("leaf"))
	if rerr != nil || !ok {
		t.Fatalf("markApplied did not run: applied-state ok=%v err=%v", ok, rerr)
	}
	if st.AppliedRef != "l" {
		t.Errorf("AppliedRef = %q, want %q", st.AppliedRef, "l")
	}
}

// A real non-zero exit with a lingering descendant still fails the apply and
// records nothing.
func TestApply_NonZeroExitWithLingeringGrandchildStillFails(t *testing.T) {
	w, out, err := applyWithLingeringGrandchild(t, 3)
	if err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("Apply must fail with 'apply failed', got: %v", err)
	}
	if strings.Contains(out, "treating the command as succeeded") {
		t.Errorf("failure must not be reported as success:\n%s", out)
	}
	if _, ok, rerr := readAppliedState(w.appliedStateKeyPath("leaf")); rerr != nil || ok {
		t.Errorf("applied-state must not be recorded for a failed apply: ok=%v err=%v", ok, rerr)
	}
}
