package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

// scriptAllInputsRelock scripts what relockAllInputs asks of a FakeRunner for
// dir: the two best-effort fsmonitor probes (the config read exits 1 with no
// output, so no daemon stop follows; rev-parse answers a temp dir), then the
// `nix flake update --refresh` call. nixErrs scripts one failing nix call per
// entry (the relock retries once, so two entries is "failed after one retry");
// nil scripts one successful call. The probes are best-effort, so an unscripted
// one would not fail the run; they are scripted so tests that assert exact call
// lists stay exact.
func scriptAllInputsRelock(t *testing.T, f *exec.FakeRunner, dir string, nixErrs []error) {
	t.Helper()
	notSet := exec.Result{ExitCode: 1}
	f.AddResponse("git", []string{"-C", dir, "config", "--type=bool", "--get", "core.fsmonitor"}, notSet,
		&exec.CommandError{Name: "git", Result: notSet})
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "--absolute-git-dir"},
		exec.Result{Stdout: []byte(t.TempDir() + "\n")}, nil)
	if len(nixErrs) == 0 {
		f.AddResponse("nix", []string{"flake", "update", "--refresh"}, exec.Result{}, nil)
		return
	}
	for _, e := range nixErrs {
		f.AddResponse("nix", []string{"flake", "update", "--refresh"}, exec.Result{ExitCode: 1}, e)
	}
}

type scopeFlow struct {
	name    string
	inPlace bool
}

var scopeFlows = []scopeFlow{{"in-place", true}, {"worktree", false}}

type scopeOpts struct {
	noScript bool // the repo carries no update-locks.sh
	upstream bool // in-place: the repo has an upstream (so it pulls)
}

// scopeEnv is a workspace of fake repos driven through either update flow. Git
// and update-locks.sh are answered by a FakeRunner; nix and the fsmonitor git
// calls by an fsNixRunner in front of it, so a test scripts nix steps (with
// side effects and cancel hooks) and reads one ordered call log.
type scopeEnv struct {
	t       *testing.T
	root    string
	inPlace bool
	repos   []string
	f       *exec.FakeRunner
	r       *fsNixRunner
	logPath string
}

const scopeBranch = "pn-update/TEST"

func (e *scopeEnv) primary(name string) string { return filepath.Join(e.root, name) }

// workDir is where a repo's relock runs: the primary checkout in place, the
// ephemeral worktree otherwise.
func (e *scopeEnv) workDir(name string) string {
	if e.inPlace {
		return e.primary(name)
	}
	return filepath.Join(e.root, ".workforests", updateWorktreesSubdir, name+"-TEST")
}

func newScopeEnv(t *testing.T, inPlace bool, repos []string, o scopeOpts) *scopeEnv {
	t.Helper()
	t.Setenv("UL_LIB_DIR", "")
	prev := updateRunStampFn
	updateRunStampFn = func() string { return "TEST" }
	t.Cleanup(func() { updateRunStampFn = prev })

	root := t.TempDir()
	var toml strings.Builder
	toml.WriteString("[workspace]\nterminal = \"" + repos[len(repos)-1] + "\"\n")
	for _, n := range repos {
		toml.WriteString("\n[repos." + n + "]\nurl = \"github:owner/" + n + "\"\n")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml.String())

	f := exec.NewFakeRunner()
	e := &scopeEnv{
		t: t, root: root, inPlace: inPlace, repos: repos, f: f,
		r: &fsNixRunner{real: f}, logPath: filepath.Join(t.TempDir(), "events.jsonl"),
	}
	readers := map[string]*fakeGitReader{}
	for _, n := range repos {
		d := e.workDir(n)
		if o.noScript {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			mkUpdateLocks(t, d)
		}
		writeFile(t, filepath.Join(d, "flake.lock"), relockLock(1, map[string]string{"sib": revA}))
		p := e.primary(n)
		if inPlace {
			readers[p] = &fakeGitReader{hasUpstreamVal: o.upstream}
			f.AddResponse("git", []string{"-C", p, "diff", "--quiet"}, exec.Result{}, nil)
			f.AddResponse("git", []string{"-C", p, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
			if o.upstream {
				f.AddResponse("git", []string{"-C", p, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
			}
			continue
		}
		wt := e.workDir(n)
		f.AddResponse("git", []string{"-C", p, "worktree", "add", "-b", scopeBranch, wt, "main"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", p, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
		f.AddResponse("git", []string{"-C", p, "diff", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", p, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", p, "merge", "--ff-only", scopeBranch}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", p, "worktree", "remove", wt}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", p, "branch", "-D", scopeBranch}, exec.Result{}, nil)
	}
	if inPlace {
		stubGitOpener(t, readers)
	}
	return e
}

// ulOK scripts one update-locks.sh run that exits 0 and prints stdout.
func (e *scopeEnv) ulOK(stdout string) {
	e.f.AddResponse("./update-locks.sh", nil, exec.Result{Stdout: []byte(stdout)}, nil)
}

// ulExit scripts one update-locks.sh run that exits with code.
func (e *scopeEnv) ulExit(code int) {
	res := exec.Result{ExitCode: code}
	e.f.AddResponse("./update-locks.sh", nil, res, &exec.CommandError{Name: "./update-locks.sh", Result: res})
}

// update runs Update at scope against the workspace, with an event log.
func (e *scopeEnv) update(ctx context.Context, scope UpdateScope) (string, error) {
	e.t.Helper()
	w, err := Open(e.root, e.r)
	if err != nil {
		e.t.Fatalf("Open: %v", err)
	}
	e.t.Cleanup(w.Close)
	lw, err := eventlog.New(e.logPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = lw.Close() }()
	opts := UpdateOptions{InPlace: e.inPlace, Scope: scope, Log: lw}
	if scope == ScopeFull {
		opts.ULLibDir = "/nix/store/x/lib/scripts"
	}
	var out bytes.Buffer
	err = w.Update(ctx, &out, opts)
	return out.String(), err
}

// events reads back the JSONL event log.
func (e *scopeEnv) events() []map[string]any {
	e.t.Helper()
	data, err := os.ReadFile(e.logPath)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			e.t.Fatalf("bad event line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// event returns the first event of kind (for project_result, of repo name).
func (e *scopeEnv) event(kind, name string) map[string]any {
	e.t.Helper()
	for _, m := range e.events() {
		if m["kind"] == kind && (name == "" || m["name"] == name) {
			return m
		}
	}
	e.t.Fatalf("no %s event for %q in %v", kind, name, e.events())
	return nil
}

func (e *scopeEnv) callNames() []string {
	var out []string
	for _, c := range e.r.calls {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

// indexOfCall is the position of the first call named name whose args start with
// prefix, or -1.
func (e *scopeEnv) indexOfCall(name string, prefix ...string) int {
	for i, c := range e.r.calls {
		if c.Name == name && len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) {
			return i
		}
	}
	return -1
}

func (e *scopeEnv) countCalls(name string, prefix ...string) int {
	n := 0
	for _, c := range e.r.calls {
		if c.Name == name && len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

// assertOneAllInputsRelock asserts exactly one nix call ran and it was the
// all-inputs relock: no UL_LIB_DIR resolution and no effectiveLock evaluation.
func (e *scopeEnv) assertOneAllInputsRelock() {
	e.t.Helper()
	nix := e.r.nixCalls()
	if len(nix) != 1 || !slices.Equal(nix[0].Args, []string{"flake", "update", "--refresh"}) {
		e.t.Fatalf("nix calls = %+v, want exactly one `flake update --refresh`", nix)
	}
}

func twoFailures() []nixStep { return []nixStep{failStep("boom"), failStep("boom")} }

func TestUpdate_ScopeFlake_InPlace_RelocksAllSkipsUpdateLocks(t *testing.T) {
	e := newScopeEnv(t, true, []string{"foo"}, scopeOpts{})
	out, err := e.update(context.Background(), ScopeFlake)
	if err != nil {
		t.Fatalf("Update: %v\n%s", err, out)
	}
	if e.countCalls("./update-locks.sh") != 0 {
		t.Errorf("scope flake must NOT run update-locks.sh; calls=%v", e.callNames())
	}
	if e.countCalls("nix", "run", ulLibResolverRef) != 0 {
		t.Errorf("scope flake must NOT resolve UL_LIB_DIR")
	}
	e.assertOneAllInputsRelock()
	if !strings.Contains(out, "scope flake — skipping update-locks.sh") {
		t.Errorf("missing the flake banner; got:\n%s", out)
	}
}

func TestUpdateViaWorktree_ScopeFlake_SkipsUpdateLocksAndULLibDir(t *testing.T) {
	e := newScopeEnv(t, false, []string{"foo"}, scopeOpts{})
	out, err := e.update(context.Background(), ScopeFlake)
	if err != nil {
		t.Fatalf("Update: %v\n%s", err, out)
	}
	if e.countCalls("./update-locks.sh") != 0 {
		t.Errorf("scope flake must NOT run update-locks.sh; calls=%v", e.callNames())
	}
	if e.countCalls("nix", "run", ulLibResolverRef) != 0 {
		t.Errorf("scope flake must NOT resolve UL_LIB_DIR (an empty env is not fatal)")
	}
	e.assertOneAllInputsRelock()
	if !strings.Contains(out, "scope flake — skipping update-locks.sh") {
		t.Errorf("missing the flake banner; got:\n%s", out)
	}
}

func TestUpdate_ScopeFull_NoUpdateLocksScriptStillRelocks(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{noScript: true})
			out, err := e.update(context.Background(), ScopeFull)
			if err != nil {
				t.Fatalf("Update: %v\n%s", err, out)
			}
			if !strings.Contains(out, "  ⊘ foo: no update-locks.sh — skipping its per-package steps (flake inputs relocked next)") {
				t.Errorf("missing the no-script line; got:\n%s", out)
			}
			e.assertOneAllInputsRelock()
		})
	}
}

// The worktree variant is named separately so a failure points at the flow.
func TestUpdateViaWorktree_ScopeFull_NoUpdateLocksScriptStillRelocks(t *testing.T) {
	e := newScopeEnv(t, false, []string{"foo"}, scopeOpts{noScript: true})
	out, err := e.update(context.Background(), ScopeFull)
	if err != nil {
		t.Fatalf("Update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no update-locks.sh — skipping its per-package steps (flake inputs relocked next)") {
		t.Errorf("missing the no-script line; got:\n%s", out)
	}
	e.assertOneAllInputsRelock()
}

func TestUpdate_ScopeFull_RunsUpdateLocksBeforeRelock(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.ulOK("")
			if out, err := e.update(context.Background(), ScopeFull); err != nil {
				t.Fatalf("Update: %v\n%s", err, out)
			}
			ul := e.indexOfCall("./update-locks.sh")
			rl := e.indexOfCall("nix", "flake", "update", "--refresh")
			if ul < 0 || rl < 0 || ul >= rl {
				t.Errorf("update-locks.sh at %d, relock at %d; want the script first; calls=%v", ul, rl, e.callNames())
			}
			e.assertOneAllInputsRelock()
		})
	}
}

func TestUpdate_ScopeFull_Success(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.ulOK("")
			if out, err := e.update(context.Background(), ScopeFull); err != nil {
				t.Fatalf("Update: %v\n%s", err, out)
			}
			pr := e.event("project_result", "foo")
			if pr["level"] != "info" {
				t.Errorf("level = %v, want info", pr["level"])
			}
			if _, has := pr["relock_failed"]; has {
				t.Errorf("a clean run must not carry relock_failed: %v", pr)
			}
		})
	}
}

func TestUpdate_ScopeFull_RelockFailureWarnsAndIntegrates(t *testing.T) {
	rows := []struct {
		name      string
		ulOut     string
		transient bool
		noScript  bool
		wantWarn  string
	}{
		{"relock-failure", "", false, false, "  ⚠ foo: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved:"},
		{"with-transient", "UL_RESULT transient=1\n", true, false, "  ⚠ foo: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved:"},
		{"no-script", "", false, true, "  ⚠ foo: nix flake update (all inputs) failed after one retry — flake inputs not moved:"},
	}
	for _, fl := range scopeFlows {
		for _, row := range rows {
			t.Run(fl.name+"/"+row.name, func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{noScript: row.noScript})
				if !row.noScript {
					e.ulOK(row.ulOut)
				}
				e.r.steps = twoFailures()
				out, err := e.update(context.Background(), ScopeFull)
				if err != nil {
					t.Fatalf("a full-scope relock failure must not fail the run: %v\n%s", err, out)
				}
				pr := e.event("project_result", "foo")
				if pr["level"] != "warn" || pr["outcome"] != "ok" || pr["relock_failed"] != true {
					t.Errorf("project_result = %v, want level warn, outcome ok, relock_failed true", pr)
				}
				if row.transient && pr["transient"] != float64(1) {
					t.Errorf("transient = %v, want 1 alongside relock_failed", pr["transient"])
				}
				if end := e.event("run_end", ""); end["status"] != "ok" {
					t.Errorf("run_end status = %v, want ok", end["status"])
				}
				if !strings.Contains(out, row.wantWarn) {
					t.Errorf("missing the relock warning %q; got:\n%s", row.wantWarn, out)
				}
				if row.noScript && strings.Contains(out, "update-locks.sh commits kept") {
					t.Errorf("a repo without update-locks.sh must not claim kept commits; got:\n%s", out)
				}
				if fl.inPlace {
					if !strings.Contains(out, "  [1/1] foo: ok, relock failed (") {
						t.Errorf("missing the in-place end line; got:\n%s", out)
					}
					return
				}
				if !strings.Contains(out, "  ⚠ foo — ok, relock failed; re-run 'pn workspace update --scope flake' to move flake inputs") {
					t.Errorf("missing the worktree summary line; got:\n%s", out)
				}
				if e.indexOfCall("git", "-C", e.primary("foo"), "merge", "--ff-only") < 0 {
					t.Errorf("the worktree variant must still integrate; calls=%v", e.callNames())
				}
			})
		}
	}
}

func TestUpdate_ScopeFull_InterruptDuringRelockIsNotAWarning(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.ulOK("")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := failStep("interrupted")
			st.cancel = cancel
			e.r.steps = []nixStep{st}
			_, err := e.update(ctx, ScopeFull)
			if err == nil || !strings.Contains(err.Error(), "update interrupted") {
				t.Fatalf("error = %v, want one containing %q", err, "update interrupted")
			}
			pr := e.event("project_result", "foo")
			if pr["level"] != "error" || pr["failed_step"] != "relock-flake" {
				t.Errorf("project_result = %v, want level error at relock-flake", pr)
			}
			for _, m := range e.events() {
				if m["kind"] == "project_result" && m["level"] == "warn" {
					t.Errorf("an interrupt must not be a warning: %v", m)
				}
			}
			if last := e.r.calls[len(e.r.calls)-1]; last.Name != "nix" {
				t.Errorf("a call followed the cancelled relock: %v", e.callNames())
			}
		})
	}
}

func TestUpdate_ScopeFull_RelockRestoreFailureFailsRepo(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			prev := writeLockFile
			writeLockFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
			t.Cleanup(func() { writeLockFile = prev })
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.ulOK("")
			e.r.steps = twoFailures()
			if _, err := e.update(context.Background(), ScopeFull); err == nil {
				t.Fatal("a failed lock restore must fail the run even in full scope")
			}
			pr := e.event("project_result", "foo")
			if pr["failed_step"] != "relock-flake" || pr["level"] != "error" {
				t.Errorf("project_result = %v, want an error at relock-flake", pr)
			}
		})
	}
}

func TestUpdate_ScopeFlake_RelockFailureFailsRepo(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.r.steps = twoFailures()
			out, err := e.update(context.Background(), ScopeFlake)
			if err == nil {
				t.Fatal("expected the run to fail")
			}
			if pr := e.event("project_result", "foo"); pr["failed_step"] != "relock-flake" {
				t.Errorf("failed_step = %v, want relock-flake", pr["failed_step"])
			}
			if fl.inPlace {
				if !strings.Contains(out, "update FAILED at step relock-flake") {
					t.Errorf("missing the in-place failure line; got:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, "failed at relock-flake") || !strings.Contains(out, "failed@relock-flake") {
				t.Errorf("missing the worktree failure lines; got:\n%s", out)
			}
			if e.indexOfCall("git", "-C", e.primary("foo"), "merge", "--ff-only") >= 0 {
				t.Errorf("a failed repo must not be integrated; calls=%v", e.callNames())
			}
		})
	}
}

func TestUpdate_RelockNoSpaceAbortsRun(t *testing.T) {
	for _, scope := range []UpdateScope{ScopeFull, ScopeFlake} {
		for _, fl := range scopeFlows {
			t.Run(string(scope)+"/"+fl.name, func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"aaa", "zzz"}, scopeOpts{})
				if scope == ScopeFull {
					e.ulOK("")
				}
				e.r.steps = []nixStep{noSpaceStep()}
				_, err := e.update(context.Background(), scope)
				if err == nil || !strings.Contains(err.Error(), "aborted") {
					t.Fatalf("error = %v, want one containing aborted", err)
				}
				for _, c := range e.callNames() {
					if strings.Contains(c, e.root+"/zzz") || strings.Contains(c, "zzz-TEST") {
						t.Fatalf("the second repo must never be attempted; saw %q", c)
					}
				}
				if pr := e.event("project_result", "aaa"); pr["failed_step"] != "relock-flake" {
					t.Errorf("project_result failed_step = %v, want relock-flake", pr["failed_step"])
				}
				if end := e.event("run_end", ""); end["failed_step"] != "relock-flake" {
					t.Errorf("run_end failed_step = %v, want relock-flake", end["failed_step"])
				}
				if !strings.Contains(err.Error(), "relock-flake") {
					t.Errorf("the error should name the step: %v", err)
				}
			})
		}
	}
}

func TestUpdate_ScopeFull_UpdateLocksFailureSkipsRelock(t *testing.T) {
	for _, fl := range scopeFlows {
		for _, code := range []int{1, ulExitAbort} {
			t.Run(fl.name+"/exit"+string(rune('0'+code/10))+string(rune('0'+code%10)), func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
				e.ulExit(code)
				if _, err := e.update(context.Background(), ScopeFull); err == nil {
					t.Fatal("expected the run to fail")
				}
				if n := e.countCalls("nix", "flake", "update"); n != 0 {
					t.Errorf("relock calls = %d, want 0 after an update-locks failure", n)
				}
				if pr := e.event("project_result", "foo"); pr["failed_step"] != "update-locks" {
					t.Errorf("failed_step = %v, want update-locks", pr["failed_step"])
				}
				if code == ulExitAbort {
					if end := e.event("run_end", ""); end["failed_step"] != "update-locks" {
						t.Errorf("run_end failed_step = %v, want update-locks", end["failed_step"])
					}
				}
			})
		}
	}
	t.Run("in-place/pull-fails", func(t *testing.T) {
		e := newScopeEnv(t, true, []string{"foo"}, scopeOpts{upstream: true})
		// Replace the scripted successful pull with a failing one (consumed first).
		e.f.Reset()
		p := e.primary("foo")
		e.f.AddResponse("git", []string{"-C", p, "diff", "--quiet"}, exec.Result{}, nil)
		e.f.AddResponse("git", []string{"-C", p, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		res := exec.Result{ExitCode: 1}
		e.f.AddResponse("git", []string{"-C", p, "pull", "--rebase", "--autostash"}, res, &exec.CommandError{Name: "git", Result: res})
		if _, err := e.update(context.Background(), ScopeFull); err == nil {
			t.Fatal("expected the run to fail")
		}
		if n := e.countCalls("nix", "flake", "update"); n != 0 {
			t.Errorf("relock calls = %d, want 0 after a failed pull", n)
		}
		if pr := e.event("project_result", "foo"); pr["failed_step"] != "pull" {
			t.Errorf("failed_step = %v, want pull", pr["failed_step"])
		}
	})
}

func TestUpdate_ScopeFlake_RelockedBannerAtUpdateLevel(t *testing.T) {
	e := newScopeEnv(t, true, []string{"foo"}, scopeOpts{})
	foo := e.primary("foo")
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{foo: {}})
	e.r.steps = []nixStep{{mutate: func() {
		writeFile(t, filepath.Join(foo, "flake.lock"), relockLock(2, map[string]string{"sib": revB}))
	}}}
	out, err := e.update(context.Background(), ScopeFlake)
	if err != nil {
		t.Fatalf("Update: %v\n%s", err, out)
	}
	want := "  ⊘ foo: scope flake — skipping update-locks.sh (flake inputs relocked, per-package locks untouched)\n"
	if !strings.Contains(out, want) {
		t.Errorf("missing the relocked=yes flake banner %q; got:\n%s", want, out)
	}
}

func TestUpdate_RunStartRecordsScopeAndRoot(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			if out, err := e.update(context.Background(), ScopeFlake); err != nil {
				t.Fatalf("Update: %v\n%s", err, out)
			}
			rs := e.event("run_start", "")
			if rs["scope"] != "flake" || rs["root"] != e.root {
				t.Errorf("run_start scope/root = %v / %v, want flake / %s", rs["scope"], rs["root"], e.root)
			}
		})
	}
}

func TestUpdate_RecordsScopeAndRelockSpans(t *testing.T) {
	cases := []struct {
		scope UpdateScope
		kind  string
	}{
		{ScopeSiblings, "siblings"},
		{ScopeFlake, "flake"},
		{ScopeFull, "flake"},
	}
	for _, fl := range scopeFlows {
		for _, c := range cases {
			t.Run(fl.name+"/"+string(c.scope), func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
				if c.scope == ScopeFull {
					e.ulOK("")
				}
				tel, rec := telemetrytest.New()
				ctx := telemetry.WithTelemetry(context.Background(), tel)
				ctx, verb := tel.StartVerb(ctx, "workspace update")
				out, err := e.update(ctx, c.scope)
				verb.End(0, nil)
				if err != nil {
					t.Fatalf("Update: %v\n%s", err, out)
				}
				if c.scope != ScopeSiblings {
					// siblings alone derives the edge lock; the others must not
					// evaluate it (or anything else) through nix.
					e.assertOneAllInputsRelock()
				}
				vs := rec.ByName("pn.verb")
				if len(vs) != 1 || attrs(vs[0])["pn.update.scope"].AsString() != string(c.scope) {
					t.Errorf("pn.verb pn.update.scope = %v, want %s", vs, c.scope)
				}
				repos, relocks := rec.ByName("pn.repo"), rec.ByName("pn.relock")
				if len(repos) != 1 || len(relocks) != 1 {
					t.Fatalf("pn.repo=%d pn.relock=%d, want 1 and 1", len(repos), len(relocks))
				}
				if relocks[0].Parent().SpanID() != repos[0].SpanContext().SpanID() {
					t.Errorf("pn.relock is not a child of pn.repo")
				}
				if got := attrs(relocks[0])["pn.relock.kind"].AsString(); got != c.kind {
					t.Errorf("pn.relock.kind = %q, want %q", got, c.kind)
				}
			})
		}
	}
}

func TestUpdate_RelockProgressLines(t *testing.T) {
	const started = "    [foo] relock all flake inputs (nix flake update --refresh): started"
	for _, fl := range scopeFlows {
		for _, fail := range []bool{false, true} {
			name := fl.name + "/ok"
			finished := "    [foo] relock all flake inputs (nix flake update --refresh): finished, ok ("
			if fail {
				name = fl.name + "/failed"
				finished = "    [foo] relock all flake inputs (nix flake update --refresh): finished, failed ("
			}
			t.Run(name, func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
				if fail {
					e.r.steps = twoFailures()
				}
				out, _ := e.update(context.Background(), ScopeFlake)
				s, f := strings.Index(out, started), strings.Index(out, finished)
				if s < 0 || f < 0 || s > f {
					t.Errorf("want %q then %q; got:\n%s", started, finished, out)
				}
			})
		}
	}
}

func TestUpgrade_RunsTrailingRelock(t *testing.T) {
	e := newScopeEnv(t, true, []string{"foo"}, scopeOpts{})
	e.ulOK("")
	w, err := Open(e.root, e.r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)
	// Apply is not scripted and fails; only the update phase is under test.
	err = w.Upgrade(context.Background(), &bytes.Buffer{}, UpgradeOptions{InPlace: true, ULLibDir: "/x"})
	if err != nil && strings.Contains(err.Error(), "upgrade: update:") {
		t.Fatalf("the update phase failed: %v", err)
	}
	ul := e.indexOfCall("./update-locks.sh")
	rl := e.indexOfCall("nix", "flake", "update", "--refresh")
	if ul < 0 || rl < 0 || ul >= rl {
		t.Errorf("update-locks.sh at %d, relock at %d; want the script, then one relock; calls=%v", ul, rl, e.callNames())
	}
	if n := e.countCalls("nix", "flake", "update", "--refresh"); n != 1 {
		t.Errorf("relock calls = %d, want 1", n)
	}
}

func TestClassifyRelockErr(t *testing.T) {
	wrap := func(errs ...error) error {
		return errors.Join(errs...)
	}
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	plain := errors.New("boom")
	cases := []struct {
		name  string
		ctx   context.Context
		scope UpdateScope
		err   error
		want  relockOutcome
	}{
		{"canceled error, live ctx, full", live, ScopeFull, context.Canceled, relockInterrupt},
		{"canceled error, live ctx, flake", live, ScopeFlake, fmt.Errorf("nix flake update --refresh: %w", context.Canceled), relockInterrupt},
		{"deadline error, live ctx, full", live, ScopeFull, context.DeadlineExceeded, relockInterrupt},
		{"cancelled ctx with a plain error", cancelled, ScopeFull, plain, relockInterrupt},
		{"wrapped no-space, full", live, ScopeFull, fmt.Errorf("%w: x", errRelockNoSpace), relockAbortRun},
		{"wrapped no-space, flake", live, ScopeFlake, fmt.Errorf("%w: x", errRelockNoSpace), relockAbortRun},
		{"wrapped restore failure, full", live, ScopeFull, fmt.Errorf("%w: disk", errRelockRestore), relockFailRepo},
		{"wrapped restore failure, flake", live, ScopeFlake, fmt.Errorf("%w: disk", errRelockRestore), relockFailRepo},
		{"plain error, full warns", live, ScopeFull, plain, relockWarn},
		{"plain error, flake fails the repo", live, ScopeFlake, plain, relockFailRepo},
		// Precedence when one error wraps several: interrupt, then no-space, then restore.
		{"canceled and no-space", live, ScopeFull, wrap(context.Canceled, errRelockNoSpace), relockInterrupt},
		{"no-space and restore", live, ScopeFull, wrap(errRelockNoSpace, errRelockRestore), relockAbortRun},
		{"restore and plain in full", live, ScopeFull, wrap(errRelockRestore, plain), relockFailRepo},
	}
	for _, c := range cases {
		if got := classifyRelockErr(c.ctx, c.scope, c.err); got != c.want {
			t.Errorf("%s: classifyRelockErr = %d, want %d", c.name, got, c.want)
		}
	}
}

// An interrupt that surfaces only as the relock's error (the outer context is
// still live) must end the repo AND the run the same way in both flows.
func TestUpdate_RelockErrorOnlyInterruptStopsRun(t *testing.T) {
	for _, scope := range []UpdateScope{ScopeFull, ScopeFlake} {
		for _, fl := range scopeFlows {
			t.Run(string(scope)+"/"+fl.name, func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"aaa", "zzz"}, scopeOpts{})
				if scope == ScopeFull {
					e.ulOK("")
				}
				step := nixStep{res: exec.Result{ExitCode: 1}, err: context.Canceled}
				e.r.steps = []nixStep{step, step}
				_, err := e.update(context.Background(), scope)
				if err == nil || !strings.Contains(err.Error(), "update interrupted") || !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want update interrupted wrapping context.Canceled", err)
				}
				for _, c := range e.callNames() {
					if strings.Contains(c, e.root+"/zzz") || strings.Contains(c, "zzz-TEST") {
						t.Fatalf("the run must stop after the interrupt; saw %q", c)
					}
				}
				pr := e.event("project_result", "aaa")
				if pr["level"] != "error" || pr["failed_step"] != "relock-flake" {
					t.Errorf("project_result = %v, want an error at relock-flake", pr)
				}
			})
		}
	}
}

func TestUpdate_InPlace_ScopeSiblingsRelockFailureFailsRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"

[repos.sib]
url = "github:owner/sib"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["sib", "foo"],
  "repos": {
    "sib": {"flake_path": "flake.nix", "remote_url": "github:owner/sib"},
    "foo": {"flake_path": "flake.nix", "remote_url": "github:owner/foo"}
  },
  "edges": [{"consumer": "foo", "alias": "sib", "target": "sib"}]
}`)
	foo, sib := filepath.Join(root, "foo"), filepath.Join(root, "sib")
	stubGitOpener(t, map[string]*fakeGitReader{foo: {}, sib: {}})
	f := exec.NewFakeRunner()
	for _, d := range []string{sib, foo} {
		mkRepoDir(t, root, filepath.Base(d))
		f.AddResponse("git", []string{"-C", d, "diff", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	}
	writeFile(t, filepath.Join(foo, "flake.lock"), relockLock(1, map[string]string{"sib": revA}))
	f.AddResponse("nix", []string{"flake", "update", "--refresh", "sib"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})

	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lw.Close() }()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{InPlace: true, Scope: ScopeSiblings, Log: lw}); err == nil {
		t.Fatal("expected the run to fail")
	}
	if !strings.Contains(out.String(), "update FAILED at step relock-siblings") {
		t.Errorf("missing the failure line; got:\n%s", out.String())
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), `"failed_step":"relock-siblings"`) {
		t.Errorf("project_result lacks failed_step relock-siblings:\n%s", data)
	}
}

// ulWith scripts one update-locks.sh run that prints stdout and exits with
// code (a non-zero code returns a CommandError).
func (e *scopeEnv) ulWith(stdout string, code int) {
	res := exec.Result{Stdout: []byte(stdout), ExitCode: code}
	var err error
	if code != 0 {
		err = &exec.CommandError{Name: "./update-locks.sh", Result: res}
	}
	e.f.AddResponse("./update-locks.sh", nil, res, err)
}

const ulStepsStdout = "UL_STEP name=alpha outcome=ok start_ms=1700000000000 ms=1500\n" +
	"noise\nUL_STEP name=beta outcome=skipped start_ms=1700000002000 ms=250\n" +
	"UL_RESULT transient=0\n"

// TestUpdate_RecordsULStepsFromUpdateLocksStdout: the UL_STEP lines on
// update-locks.sh's stdout become pn.ul.step spans under the repo's pn.repo and
// steps / steps_ms on project_result, in both flows and on every outcome that
// follows the script (ok, a CommandError, exit 77).
func TestUpdate_RecordsULStepsFromUpdateLocksStdout(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		outcome string
		wantErr bool
	}{
		{"ok", 0, "ok", false},
		{"command-error", 1, "failed", true},
		{"exit-77", ulExitAbort, "aborted", true},
	}
	for _, fl := range scopeFlows {
		for _, c := range cases {
			t.Run(fl.name+"/"+c.name, func(t *testing.T) {
				e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
				e.ulWith(ulStepsStdout, c.code)
				tel, rec := telemetrytest.New()
				ctx := telemetry.WithTelemetry(context.Background(), tel)
				ctx, verb := tel.StartVerb(ctx, "workspace update")
				_, err := e.update(ctx, ScopeFull)
				verb.End(0, nil)
				if (err != nil) != c.wantErr {
					t.Fatalf("Update err = %v, wantErr %v", err, c.wantErr)
				}
				spans, repos := rec.ByName("pn.ul.step"), rec.ByName("pn.repo")
				if len(spans) != 2 || len(repos) != 1 {
					t.Fatalf("pn.ul.step=%d pn.repo=%d, want 2 and 1", len(spans), len(repos))
				}
				for _, s := range spans {
					if s.Parent().SpanID() != repos[0].SpanContext().SpanID() {
						t.Errorf("%v is not a child of pn.repo", attrs(s))
					}
				}
				if got := attrs(spans[0])["pn.ul.step.name"].AsString(); got != "alpha" {
					t.Errorf("first step name = %q, want alpha", got)
				}
				pr := e.event("project_result", "foo")
				if pr["outcome"] != c.outcome {
					t.Errorf("outcome = %v, want %s", pr["outcome"], c.outcome)
				}
				steps, _ := pr["steps"].([]any)
				if len(steps) != 2 {
					t.Fatalf("steps = %v, want 2", pr["steps"])
				}
				first, _ := steps[0].(map[string]any)
				if first["name"] != "alpha" || first["outcome"] != "ok" || first["ms"] != float64(1500) {
					t.Errorf("steps[0] = %v", first)
				}
				if pr["steps_ms"] != float64(1750) {
					t.Errorf("steps_ms = %v, want 1750", pr["steps_ms"])
				}
			})
		}
	}
}

// TestUpdate_NoULStepsLeavesEventUnchanged: no UL_STEP lines, no steps fields.
func TestUpdate_NoULStepsLeavesEventUnchanged(t *testing.T) {
	for _, fl := range scopeFlows {
		t.Run(fl.name, func(t *testing.T) {
			e := newScopeEnv(t, fl.inPlace, []string{"foo"}, scopeOpts{})
			e.ulOK("UL_RESULT transient=0\n")
			if _, err := e.update(context.Background(), ScopeFull); err != nil {
				t.Fatalf("Update: %v", err)
			}
			pr := e.event("project_result", "foo")
			if _, has := pr["steps"]; has {
				t.Errorf("project_result carries steps without UL_STEP lines: %v", pr)
			}
			if _, has := pr["steps_ms"]; has {
				t.Errorf("project_result carries steps_ms without UL_STEP lines: %v", pr)
			}
		})
	}
}
