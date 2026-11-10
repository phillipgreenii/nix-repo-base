package workspace

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	all := e.f.withSession()
	first := indexOfCall(all, func(c exec.Call) bool { return c.Name == "sudo" })
	build := indexOfCall(all, isBuildCall)
	if first < 0 || build < 0 || first > build {
		t.Errorf("first sudo call %d, build call %d; want sudo first:\n%s", first, build, dumpCalls(all))
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
	// The next git call after activate belongs to the post steps and markApplied.
	nextGit := indexOfCall(calls[act:], func(c exec.Call) bool { return c.Name == "git" })
	if nextGit >= 0 && act+nextGit < kill {
		t.Errorf("sudo -k (%d) came after the post-activate git call (%d)", kill, act+nextGit)
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
// verify) have been made, by polling the recorded calls without sleeping.
func waitForRefreshes(t *testing.T, f *asUserFake, n int) {
	t.Helper()
	sig := make(chan struct{})
	go func() {
		for {
			cnt := 0
			for _, c := range f.sessionCalls() {
				if strings.Join(c.Args, " ") == "-n -v" {
					cnt++
				}
			}
			if cnt >= n+2 {
				close(sig)
				return
			}
			runtimeGosched()
		}
	}()
	select {
	case <-sig:
	case <-afterFiveSeconds():
		t.Fatal("refresh calls did not arrive")
	}
}

func TestAsUser_LostSessionPrintsNoticeBeforeRootSteps(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	fastSudo(t)
	nv := 0
	e.f.sudoSession = func(_ context.Context, args []string) (exec.Result, error) {
		if strings.Join(args, " ") == "-n -v" {
			nv++ // only the apply goroutine and the loop call this, never concurrently
			if nv >= 3 {
				return cmdErr("sudo", args, 1)
			}
		}
		return e.f.defaultSudoSession(args)
	}
	e.f.build = func(_ context.Context, o exec.RunOptions) (exec.Result, error) {
		expectNoLoops(t) // the loop ended itself: the session is lost
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

func runtimeGosched() { runtime.Gosched() }

func afterFiveSeconds() <-chan time.Time { return time.After(5 * time.Second) }
