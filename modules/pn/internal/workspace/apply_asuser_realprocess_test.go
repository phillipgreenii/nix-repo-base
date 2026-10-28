package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pnexec "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// A real-process test (V-3) of the kill behaviour of apply's user build
// (bead pg2-sdbhh4.9 using pg2-sdbhh4.20's mechanism): the build is the real
// os/exec runner running a bash script shaped like darwin-rebuild, whose long
// work is a pipeline inside $(...) that bash never forwards a signal to. A
// pn-initiated cancel must leave no descendant behind, and the run dir must be
// removed. Everything else the apply runs (git, the root steps) is the fake.
//
// The script writes its own pid (the process group leader: the user build asks
// for its own group) to "$pidfile.leader". A descendant is identified by being
// alive AND in that group, never with `ps`, which the nix darwin build sandbox
// refuses to exec ("/bin/ps: Operation not permitted", measured 2026-10-28 for
// pg2-67p4di); kill(2) and getpgid(2) work there.
const buildShapedScript = `pidfile="$1"; dur="$2"
echo $$ > "$pidfile.leader"
x=$(sh -c 'echo $$ > "$1"; exec sleep "$2"' sh "$pidfile" "$dur" | cat)
echo "unreachable: $x"
`

func TestAsUser_CancelDuringTheBuildLeavesNoDescendant(t *testing.T) {
	e := newAsUserEnv(t, asUserTOML, nil)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "sleeper.pid")
	dur := fmt.Sprintf("%d.%09d", 3000+os.Getpid()%1000, time.Now().Nanosecond())
	real := pnexec.NewRealRunnerWithKillGrace(300 * time.Millisecond)

	var gotOpts pnexec.RunOptions
	e.f.build = func(ctx context.Context, opts pnexec.RunOptions) (pnexec.Result, error) {
		gotOpts = opts
		// The real runner, with the options pn itself chose for the user build.
		return real.Run(ctx, "bash", []string{"-c", buildShapedScript, "bash", pidfile, dur}, opts)
	}
	t.Cleanup(func() { // never leave the long sleep behind, whatever the test did
		if pid := readPid(pidfile); pid > 1 && ourSleeper(pid, readPid(pidfile+".leader")) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.apply(ctx, io.Discard) }()

	var pid, leader int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pid, leader = readPid(pidfile), readPid(pidfile+".leader"); pid > 1 && leader > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 1 || leader <= 1 {
		cancel()
		t.Fatal("the build's long child never started")
	}
	if !ourSleeper(pid, leader) {
		cancel()
		t.Fatalf("pid %d is not our sleeper", pid)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "apply failed: build:") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("apply did not return after the cancel")
	}

	if !gotOpts.OwnProcessGroup {
		t.Error("the user build did not ask for its own process group")
	}
	gone := false
	for i := 0; i < 200 && !gone; i++ {
		gone = !ourSleeper(pid, leader)
		if !gone {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !gone {
		t.Errorf("a descendant of the cancelled build (pid %d, sleep %s) is still running", pid, dur)
	}
	if n := len(e.f.rootCalls()); n != 0 {
		t.Errorf("a root step ran after the cancel: %d", n)
	}
	if dirs := e.runDirs(t); len(dirs) != 0 {
		t.Errorf("run dirs left after the cancel: %v", dirs)
	}
}

func readPid(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// ourSleeper reports whether pid is alive AND a member of the process group led
// by leader (the build script), so a recycled pid is never mistaken for, or
// signalled as, the sleeper.
func ourSleeper(pid, leader int) bool {
	if pid <= 1 || leader <= 1 || syscall.Kill(pid, 0) != nil {
		return false
	}
	pgid, err := syscall.Getpgid(pid)
	return err == nil && pgid == leader
}
