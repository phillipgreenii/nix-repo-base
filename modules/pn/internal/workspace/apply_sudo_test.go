package workspace

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Tests of how apply uses the sudo session (sudo_session.go). The fake answers
// every sudo call; nothing real runs.

func indexOfCall(cs []exec.Call, pred func(exec.Call) bool) int {
	for i, c := range cs {
		if pred(c) {
			return i
		}
	}
	return -1
}

func isSessionArgv(argv string) func(exec.Call) bool {
	return func(c exec.Call) bool {
		return isSudoSessionCall(c.Name, c.Args) && strings.Join(c.Args, " ") == argv
	}
}

func TestAsUser_SudoAuthRunsBeforeTheBuildAndBeforeCaptureHeads(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	var steps []string
	e.w.stepRecorder = func(_ string, _ bool, step string) { steps = append(steps, step) }
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	sudoIdx, headsIdx := -1, -1
	for i, s := range steps {
		switch s {
		case stepSudoAuth:
			sudoIdx = i
		case stepCaptureHeads:
			headsIdx = i
		}
	}
	if sudoIdx < 0 || headsIdx < 0 || sudoIdx > headsIdx {
		t.Errorf("steps = %v, want %s before %s", steps, stepSudoAuth, stepCaptureHeads)
	}
	e.f.mu.Lock()
	all := append([]exec.Call{}, e.f.calls...)
	e.f.mu.Unlock()
	first := indexOfCall(all, func(c exec.Call) bool { return c.Name == "sudo" })
	heads := indexOfCall(all, func(c exec.Call) bool {
		return c.Name == "git" && slices.Contains(c.Args, "rev-parse") && slices.Contains(c.Args, "HEAD")
	})
	build := indexOfCall(all, isBuildCall)
	if first < 0 || heads < 0 || build < 0 || first > heads || first > build {
		t.Errorf("first sudo call %d, captureHeads git call %d, build call %d; want sudo first:\n%s", first, heads, build, dumpCalls(all))
	}
}

func TestAsUser_RootStepsDoNotPromptAgain(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	prompts := 0
	for _, c := range e.f.sessionCalls() {
		if strings.Join(c.Args, " ") == "-v" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Errorf("prompting sudo -v ran %d times, want exactly 1", prompts)
	}
	root := e.f.rootCalls()
	if len(root) != 2 {
		t.Fatalf("root calls = %d, want 2:\n%s", len(root), dumpCalls(root))
	}
	if got := argvOf(root[0]); got != strings.Join(setProfileArgv(e.sys.out, activationPlatform{rootHome: "/var/root"}), " ") {
		t.Errorf("set-profile argv = %s", got)
	}
	if got := argvOf(root[1]); got != "sudo "+e.sys.out+"/sw/bin/darwin-rebuild activate" {
		t.Errorf("activate argv = %s", got)
	}
}

func TestAsUser_SessionIsReleasedRightAfterActivate(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	calls := append([]exec.Call{}, e.f.calls...)
	e.f.mu.Unlock()
	act := indexOfCall(calls, func(c exec.Call) bool {
		return c.Name == "sudo" && len(c.Args) == 2 && c.Args[1] == "activate"
	})
	kill := indexOfCall(calls, isSessionArgv("-k"))
	if act < 0 || kill < act {
		t.Fatalf("activate at %d, sudo -k at %d:\n%s", act, kill, dumpCalls(calls))
	}
	// Every call after activate (git --exec-path, the fsmonitor and status git
	// calls of markApplied, ...) belongs to the post-activate work; none may come
	// before sudo -k. Only refresh calls of the loop may sit between the two.
	for i := act + 1; i < kill; i++ {
		if !isSudoSessionCall(calls[i].Name, calls[i].Args) {
			t.Errorf("call %d (%s) ran between activate (%d) and sudo -k (%d):\n%s", i, argvOf(calls[i]), act, kill, dumpCalls(calls))
		}
	}
	if kill+1 >= len(calls) {
		t.Fatalf("no post-activate call follows sudo -k; the test would be vacuous:\n%s", dumpCalls(calls))
	}
}

func TestAsUser_SudoAuthFailureFailsBeforeAnyBuild(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	e.f.sudoSession = func(_ context.Context, args []string) (exec.Result, error) {
		if strings.Join(args, " ") == "-v" {
			return cmdErr("sudo", args, 1)
		}
		return e.f.defaultSudoSession(args)
	}
	err := e.apply(context.Background(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "sudo authentication") {
		t.Fatalf("err = %v, want a sudo authentication failure", err)
	}
	if indexOfCall(e.f.significant(), isBuildCall) >= 0 {
		t.Error("the build ran after a failed sudo authentication")
	}
	if got := trailerValue(t, e.applyLogText(t), "failed_step"); got != "sudo_auth" {
		t.Errorf("failed_step = %q, want sudo_auth", got)
	}
	if e.applied(t) {
		t.Error("an apply that failed to authenticate was marked applied")
	}
}

// The time spent in the sudo prompt is sudo_auth_seconds; the git and profile
// reads that follow it are prelude, not sudo_auth.
func TestAsUser_SudoPromptTimeLandsInSudoAuthNotPrelude(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	applyLogNow = func() time.Time { return now }
	t.Cleanup(func() { applyLogNow = time.Now })
	e.f.sudoSession = func(_ context.Context, args []string) (exec.Result, error) {
		if strings.Join(args, " ") == "-v" {
			now = now.Add(100 * time.Second) // the operator takes 100 s to type the password
		}
		return e.f.defaultSudoSession(args)
	}
	// Everything after the prompt (git binary identity, captureHeads) takes
	// 40 s, and must not be billed to sudo_auth.
	e.f.onGit = func() { now = now.Add(10 * time.Second) }
	if err := e.apply(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	log := e.applyLogText(t)
	auth, err := strconv.ParseFloat(trailerValue(t, log, "sudo_auth_seconds"), 64)
	if err != nil {
		t.Fatalf("sudo_auth_seconds: %v\n%s", err, log)
	}
	if auth < 99 || auth > 101 {
		t.Errorf("sudo_auth_seconds = %v, want about 100", auth)
	}
	pre, err := strconv.ParseFloat(trailerValue(t, log, "prelude_seconds"), 64)
	if err != nil {
		t.Fatalf("prelude_seconds: %v\n%s", err, log)
	}
	if pre < 10 || pre > 100 {
		t.Errorf("prelude_seconds = %v, want the git time (>= 10) and not the prompt time", pre)
	}
}

func TestAsUser_SkippedApplyNeverPromptsForSudo(t *testing.T) {
	e, sr := newScriptedEnv(t, asUserTOML)
	seedSkippableState(t, e, sr)
	if err := e.w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.withSession()); n != 0 {
		t.Fatalf("a skipped apply ran commands:\n%s", dumpCalls(e.f.withSession()))
	}
}

func TestAsUser_BuildFailureStopsTheRefreshAndInvalidates(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	fastSudo(t)
	e.f.build = func(context.Context, exec.RunOptions) (exec.Result, error) {
		waitForRefreshes(t, e.f, 3)
		return cmdErr("darwin-rebuild", nil, 2)
	}
	if err := e.apply(context.Background(), io.Discard); err == nil {
		t.Fatal("expected the build to fail")
	}
	s := e.f.sessionCalls()
	if got := argvOf(s[len(s)-1]); got != "sudo -k" {
		t.Errorf("last session call = %s, want sudo -k", got)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("root steps ran after a failed build: %d", n)
	}
	expectNoLoops(t)
}

func TestAsUser_CancelDuringBuildStopsTheRefreshAndInvalidates(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	fastSudo(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.f.build = func(c context.Context, _ exec.RunOptions) (exec.Result, error) {
		waitForRefreshes(t, e.f, 2)
		cancel()
		<-c.Done()
		return cmdErr("darwin-rebuild", nil, -1)
	}
	if err := e.apply(ctx, io.Discard); err == nil {
		t.Fatal("expected a cancelled apply to fail")
	}
	s := e.f.sessionCalls()
	if got := argvOf(s[len(s)-1]); got != "sudo -k" {
		t.Errorf("last session call = %s, want sudo -k", got)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("root steps ran after a cancel: %d", n)
	}
	expectNoLoops(t)
}

// waitForRefreshes blocks until n refresh calls (beyond the probe and the
// verify) have been made, polling the recorded calls from the test goroutine.
func waitForRefreshes(t *testing.T, f *asUserFake, n int) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for {
		cnt := 0
		for _, c := range f.sessionCalls() {
			if strings.Join(c.Args, " ") == "-n -v" {
				cnt++
			}
		}
		if cnt >= n+2 {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("refresh calls did not arrive")
		}
	}
}

func TestAsUser_LostSessionPrintsNoticeBeforeRootSteps(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	fastSudo(t)
	nv := 0
	refreshFailed := make(chan struct{})
	var failOnce sync.Once
	e.f.sudoSession = func(_ context.Context, args []string) (exec.Result, error) {
		if strings.Join(args, " ") == "-n -v" {
			nv++ // only the apply goroutine and the loop call this, never concurrently
			if nv >= 3 {
				failOnce.Do(func() { close(refreshFailed) })
				return cmdErr("sudo", args, 1)
			}
		}
		return e.f.defaultSudoSession(args)
	}
	e.f.build = func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
		// Order the build after the failed refresh: once the loop goroutine has
		// made the failing call it is alive, so expectNoLoops then returns only
		// after Lost() is set (the loop records it before it exits). Without
		// the first wait expectNoLoops passes when the loop was not scheduled yet.
		select {
		case <-refreshFailed:
		case <-time.After(5 * time.Second):
			t.Error("the refresh never failed")
		}
		expectNoLoops(t)
		return exec.Result{}, os.Symlink(e.sys.out, filepath.Join(o.Dir, "result"))
	}
	var out bytes.Buffer
	var atSetProfile string
	e.f.setProfile = func(context.Context, []string) (exec.Result, error) {
		atSetProfile = out.String()
		return exec.Result{}, nil
	}
	if err := e.apply(context.Background(), &out); err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	if !strings.Contains(atSetProfile, "sudo authentication expired during the build") {
		t.Errorf("no expiry notice before set-profile:\n%s", atSetProfile)
	}
	if strings.Contains(out.String(), "running the root steps on the authenticated sudo session") {
		t.Errorf("the authenticated notice was printed for a lost session:\n%s", out.String())
	}
}

func TestAsUser_ShowNixCommandsListsTheSudoAuthLine(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	var out bytes.Buffer
	if err := e.w.Apply(context.Background(), &out, ApplyOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	line := "# sudo -v (before the build; kept fresh with sudo -n -v while it runs; released with sudo -k)"
	i := strings.Index(got, line)
	if i < 0 {
		t.Fatalf("dry run lacks the sudo line:\n%s", got)
	}
	if j := strings.Index(got, "# 0 sudo-auth"); j < 0 || strings.Index(got, "# set-profile command") < i {
		t.Errorf("sudo lines missing or after set-profile:\n%s", got)
	}
	if n := len(e.f.withSession()); n != 0 {
		t.Errorf("a dry run ran %d commands", n)
	}
}
