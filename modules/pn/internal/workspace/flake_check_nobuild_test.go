package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// scriptRunner answers every command through fn and records the calls. It is for
// the argument plumbing of flake-check --no-build; what nix actually does with
// those arguments is pinned by the real-nix test in cmd/pn.
type scriptRunner struct {
	mu    sync.Mutex
	calls []exec.Call
	fn    func(name string, args []string) (exec.Result, error)
}

func (s *scriptRunner) Run(_ context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, exec.Call{Name: name, Args: append([]string{}, args...), Opts: opts})
	s.mu.Unlock()
	return s.fn(name, args)
}

func (s *scriptRunner) nixCalls() []exec.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []exec.Call
	for _, c := range s.calls {
		if c.Name == "nix" {
			out = append(out, c)
		}
	}
	return out
}

func okNix(string, []string) (exec.Result, error) {
	return exec.Result{Stdout: []byte(`{"x":"/nix/store/aaa-x.drv"}`)}, nil
}

func failNix(stderr string, code int) (exec.Result, error) {
	res := exec.Result{ExitCode: code, Stderr: []byte(stderr)}
	return res, &exec.CommandError{Name: "nix", Result: res}
}

func withRunner(w *Workspace, fn func(string, []string) (exec.Result, error)) *scriptRunner {
	r := &scriptRunner{fn: fn}
	w.runner = r
	return r
}

// stateHome points the experiment log at a fresh directory and returns the log path.
func stateHome(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	return filepath.Join(state, "pn", noBuildLogName)
}

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

func TestFlakeCheckNoBuild_EvaluatesDrvPathsWithSiblingOverridesAndBuildsNothing(t *testing.T) {
	logPath := stateHome(t)
	w, root := selectFixture(t, true)
	r := withRunner(w, okNix)
	sys, err := nixSystem()
	if err != nil {
		t.Skip(err)
	}
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Terminal: "a"}); err != nil {
		t.Fatalf("FlakeCheck --no-build: %v\n%s", err, out.String())
	}
	calls := r.nixCalls()
	if len(calls) != 9 {
		t.Fatalf("want 3 repos x 3 sets = 9 nix calls, got %d", len(calls))
	}
	for _, c := range calls {
		if c.Args[0] != "eval" {
			t.Errorf("only `nix eval` may run, got %v", c.Args)
		}
		if !hasArg(c.Args, "--no-write-lock-file") {
			t.Errorf("flake.lock must never be written: %v", c.Args)
		}
		for _, banned := range []string{"build", "check", "--no-build"} {
			if hasArg(c.Args, banned) {
				t.Errorf("%q must not be passed to nix: %v", banned, c.Args)
			}
		}
		if !c.Opts.WrapNix {
			t.Errorf("a nix run of this length belongs under pg-nix-log-wrapped (WrapNix)")
		}
	}
	// Topological order from the lock (c, b, a), and per repo checks, packages, devShells.
	wantDirs := []string{"c", "c", "c", "b", "b", "b", "a", "a", "a"}
	wantSets := []string{"checks", "packages", "devShells"}
	for i, c := range calls {
		if c.Opts.Dir != filepath.Join(root, wantDirs[i]) {
			t.Errorf("call %d dir = %s, want %s", i, c.Opts.Dir, wantDirs[i])
		}
		want := ".#" + wantSets[i%3] + "." + sys
		if !hasArg(c.Args, want) {
			t.Errorf("call %d should evaluate %s: %v", i, want, c.Args)
		}
		if !hasArg(c.Args, noBuildApply) {
			t.Errorf("call %d must apply the drvPath mapping: %v", i, c.Args)
		}
	}
	// The override set is the one flake-check derives: repo a is overridden to b and c, never to itself.
	aOverrides := w.overrideInputArgsFor("a", overrideOpts{ExcludeRepo: "a"})
	if len(aOverrides) != 6 {
		t.Fatalf("fixture: want 6 override args for a, got %v", aOverrides)
	}
	if !containsSeq(calls[6].Args, aOverrides) {
		t.Errorf("repo a must be evaluated with %v, got %v", aOverrides, calls[6].Args)
	}
	for _, o := range aOverrides {
		if strings.HasSuffix(o, filepath.Join(root, "a")) {
			t.Errorf("a repo must not override itself: %v", aOverrides)
		}
	}
	// One log line per repo, caller manual.
	recs := readLog(t, logPath)
	if len(recs) != 3 {
		t.Fatalf("want one log line per evaluated repo (3), got %d", len(recs))
	}
	for _, rec := range recs {
		if rec["outcome"] != "ok" || rec["caller"] != "manual" || rec["nix_exit"] != float64(0) {
			t.Errorf("unexpected record %v", rec)
		}
	}
}

func TestFlakeCheckNoBuild_SelectsOneRepoByWorktreePathAndRev(t *testing.T) {
	stateHome(t)
	w, root := selectFixture(t, true)
	r := withRunner(w, func(name string, args []string) (exec.Result, error) {
		if name == "git" {
			return exec.Result{Stdout: []byte("0123456789abcdef0123456789abcdef01234567\n")}, nil
		}
		return okNix(name, args)
	})
	wt := filepath.Join(root, "a", ".worktrees", "drain-x")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: wt, Rev: "HEAD", Caller: CallerFF1c, Terminal: "a"})
	if err != nil {
		t.Fatalf("FlakeCheck: %v\n%s", err, out.String())
	}
	calls := r.nixCalls()
	if len(calls) != 3 {
		t.Fatalf("one repo, three sets: got %d nix calls", len(calls))
	}
	for _, c := range calls {
		if c.Opts.Dir != wt {
			t.Errorf("a path argument evaluates THAT worktree: dir = %s, want %s", c.Opts.Dir, wt)
		}
		wantRef := "git+file://" + wt + "?rev=0123456789abcdef0123456789abcdef01234567#"
		found := false
		for _, a := range c.Args {
			if strings.HasPrefix(a, wantRef) {
				found = true
			}
		}
		if !found {
			t.Errorf("--rev must give the pure ref %s...: %v", wantRef, c.Args)
		}
		if !containsSeq(c.Args, w.overrideInputArgsFor("a", overrideOpts{ExcludeRepo: "a"})) {
			t.Errorf("the selected repo still gets its sibling overrides: %v", c.Args)
		}
	}
	if recs := readLog(t, noBuildLogPath()); len(recs) != 1 || recs[0]["caller"] != "ff-1c" || recs[0]["repo"] != "a" {
		t.Errorf("want one ff-1c line for repo a, got %v", recs)
	}
}

func TestFlakeCheckNoBuild_NoIFDPassesTheNixOption(t *testing.T) {
	stateHome(t)
	w, _ := selectFixture(t, true)
	r := withRunner(w, okNix)
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: "b", NoIFD: true, Terminal: "a"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.nixCalls() {
		if !containsSeq(c.Args, []string{"--option", "allow-import-from-derivation", "false"}) {
			t.Errorf("--no-ifd must reach nix: %v", c.Args)
		}
	}
}

func TestFlakeCheckNoBuild_AFailureFailsTheCheckAndIsLoggedWithoutRetry(t *testing.T) {
	logPath := stateHome(t)
	w, _ := selectFixture(t, true)
	longMsg := "error: " + strings.Repeat("x", 400)
	r := withRunner(w, func(name string, args []string) (exec.Result, error) {
		if hasArg(args, ".#packages."+mustSystem(t)) {
			return failNix("error:\n       … while evaluating\n\n"+longMsg+"\n", 1)
		}
		return okNix(name, args)
	})
	var out, errOut bytes.Buffer
	err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: "a", Terminal: "a"})
	if err == nil || !strings.Contains(err.Error(), "failed in 1 project(s): a") {
		t.Fatalf("want a failure naming a, got %v", err)
	}
	if got := len(r.nixCalls()); got != 3 {
		t.Errorf("no retry: want exactly 3 nix calls, got %d", got)
	}
	recs := readLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("want 1 line, got %d", len(recs))
	}
	rec := recs[0]
	if rec["outcome"] != "failed" || rec["nix_exit"] != float64(1) {
		t.Errorf("outcome/nix_exit: %v", rec)
	}
	if !reflect.DeepEqual(rec["failed_sets"], []any{"packages"}) {
		t.Errorf("failed_sets = %v, want [packages]", rec["failed_sets"])
	}
	fe, _ := rec["first_error"].(string)
	if !strings.HasPrefix(fe, "error: xxx") || len([]rune(fe)) != firstErrorMax {
		t.Errorf("first_error must be the first `error: <message>` line cut to %d characters, got %d: %q", firstErrorMax, len([]rune(fe)), fe)
	}
}

func TestFlakeCheckNoBuild_ASetTheFlakeDoesNotProvideIsSkippedNotFailed(t *testing.T) {
	stateHome(t)
	w, _ := selectFixture(t, true)
	withRunner(w, func(name string, args []string) (exec.Result, error) {
		if hasArg(args, ".#devShells."+mustSystem(t)) {
			return failNix("error: flake 'git+file:///x' does not provide attribute 'devShells."+mustSystem(t)+"'\n", 1)
		}
		return okNix(name, args)
	})
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: "a", Terminal: "a"}); err != nil {
		t.Fatalf("an absent set is not a failure: %v", err)
	}
	if !strings.Contains(out.String(), "not provided by the flake, skipped") {
		t.Errorf("the skip should be reported:\n%s", out.String())
	}
}

func TestFlakeCheckNoBuild_AnUnwritableStateDirectoryDoesNotFailTheCheck(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "state-is-a-file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)
	w, _ := selectFixture(t, true)
	withRunner(w, okNix)
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: "a", Terminal: "a"}); err != nil {
		t.Fatalf("an unwritable log directory must not fail the check: %v", err)
	}
	if !strings.Contains(errOut.String(), "cannot log the run") {
		t.Errorf("the problem should be reported once on stderr: %q", errOut.String())
	}
}

func TestFlakeCheckNoBuild_UsageErrors(t *testing.T) {
	stateHome(t)
	w, _ := selectFixture(t, true)
	r := withRunner(w, okNix)
	cases := []struct {
		name string
		opts FlakeCheckOptions
	}{
		{"unknown caller", FlakeCheckOptions{NoBuild: true, Caller: "cron", Terminal: "a"}},
		{"rev without a repo", FlakeCheckOptions{NoBuild: true, Rev: "HEAD", Terminal: "a"}},
		{"not a workspace repo", FlakeCheckOptions{NoBuild: true, Repo: t.TempDir(), Terminal: "a"}},
		{"repo selection without --no-build", FlakeCheckOptions{Repo: "a", Terminal: "a"}},
		{"--rev without --no-build", FlakeCheckOptions{Rev: "HEAD", Terminal: "a"}},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		err := w.FlakeCheck(context.Background(), &out, &errOut, tc.opts)
		var ue *UsageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: want a UsageError, got %v", tc.name, err)
		}
	}
	if len(r.nixCalls()) != 0 {
		t.Errorf("a usage error runs nothing")
	}
}

func TestFlakeCheckNoBuild_UnresolvableRevIsAUsageError(t *testing.T) {
	stateHome(t)
	w, _ := selectFixture(t, true)
	withRunner(w, func(name string, args []string) (exec.Result, error) {
		if name == "git" {
			return exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}}
		}
		return okNix(name, args)
	})
	var out, errOut bytes.Buffer
	err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{NoBuild: true, Repo: "a", Rev: "nope", Terminal: "a"})
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %v", err)
	}
}

func TestFirstErrorLine(t *testing.T) {
	cases := map[string]string{
		"error:\n  … trace\n\nerror: boom\n":      "error: boom",
		"warning: x\nerror: first\nerror: second": "error: first",
		"no error prefix here\n":                  "no error prefix here",
		"":                                        "",
	}
	for in, want := range cases {
		if got := firstErrorLine([]byte(in)); got != want {
			t.Errorf("firstErrorLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetAbsent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"error: flake 'git+file:///x' does not provide attribute 'devShells.aarch64-darwin'\n", true},
		{"error: flake 'x' does not provide attribute 'a'\nerror: boom\n", false},
		{"error: boom\n", false},
		{"", false},
	}
	for _, c := range cases {
		if got := setAbsent([]byte(c.in)); got != c.want {
			t.Errorf("setAbsent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPureRefEncodesTheCharactersThatSplitARef(t *testing.T) {
	got := pureRef("/a b/c?d#e", "abc")
	want := "git+file:///a%20b/c%3Fd%23e?rev=abc"
	if got != want {
		t.Errorf("pureRef = %q, want %q", got, want)
	}
}

func mustSystem(t *testing.T) string {
	t.Helper()
	s, err := nixSystem()
	if err != nil {
		t.Skip(err)
	}
	return s
}

func hasArg(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// containsSeq reports whether sub appears as a contiguous run in ss.
func containsSeq(ss, sub []string) bool {
	for i := 0; i+len(sub) <= len(ss); i++ {
		if reflect.DeepEqual(ss[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}
