package exec

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// darwinRebuildShaped mimics the part of darwin-rebuild that matters here
// (pg2-sdbhh4.20): a bash script whose long-running work is a PIPELINE inside a
// command substitution (systemConfig=$(nix build --json ... | jq -r ...)), so
// the long child is a grandchild of the process pn started and bash forwards no
// signals to it. $1 is a file the long child writes its pid to (it then execs
// sleep, so the pid is the sleep's), $2 is the sleep duration (unique per test,
// so a leftover is recognisable), and $3 is "held" to leave the sleep's stderr
// on the inherited pipe (as nix's is) or "detached" to point it at /dev/null.
const darwinRebuildShaped = `
pidfile="$1"; dur="$2"; mode="$3"
echo $$ > "$pidfile.leader"
if [ "$mode" = ignoreterm ]; then
  # the long child ignores SIGTERM (the disposition survives exec), so only
  # the SIGKILL escalation can end it
  x=$(sh -c 'trap "" TERM; echo $$ > "$1"; exec sleep "$2"' sh "$pidfile" "$dur" 2>/dev/null | cat)
elif [ "$mode" = detached ]; then
  x=$(sh -c 'echo $$ > "$1"; exec sleep "$2"' sh "$pidfile" "$dur" 2>/dev/null | cat)
else
  x=$(sh -c 'echo $$ > "$1"; exec sleep "$2"' sh "$pidfile" "$dur" | cat)
fi
echo "unreachable: $x"
`

// uniqueSleepDuration returns a sleep argument no other process on the machine
// uses, so the test can positively identify (and only ever signal) its own
// long child.
func uniqueSleepDuration() string {
	return fmt.Sprintf("%d.%09d", 3000+os.Getpid()%1000, time.Now().Nanosecond())
}

// startedSleeper is the long-running grandchild a test started, identified by
// the pid it wrote and the process group the test's own runner call created.
//
// Identity is the process GROUP, not `ps`: the runner is asked for its own
// process group (OwnProcessGroup), so the script is that group's leader (its pid
// is the pgid, written to "<pidfile>.leader") and the sleeper is a member. A
// recycled pid is therefore never mistaken for, or signalled as, the sleeper
// (it would also have to sit in that exact group). `ps` is not used because the
// nix darwin build sandbox refuses to exec it ("/bin/ps: Operation not
// permitted", measured 2026-10-28 for pg2-67p4di), which made every probe read
// "not running"; kill(2) and getpgid(2) work there.
type startedSleeper struct {
	pid    int
	leader int
	dur    string
}

// isOurSleeper reports whether pid is still alive AND is a member of the
// script's process group.
func (s startedSleeper) isOurSleeper() bool {
	if s.pid <= 1 || s.leader <= 1 || syscall.Kill(s.pid, 0) != nil {
		return false
	}
	pgid, err := syscall.Getpgid(s.pid)
	return err == nil && pgid == s.leader
}

// readPidFile returns the pid in path, or 0 when it is absent or incomplete.
func readPidFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return 0
	}
	return pid
}

// waitForSleeper blocks until the script's long child has written its pid (the
// leader pid is written first, before the long child starts).
func waitForSleeper(t *testing.T, pidfile, dur string) startedSleeper {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pid := readPidFile(pidfile); pid > 1 {
			if leader := readPidFile(pidfile + ".leader"); leader > 1 {
				return startedSleeper{pid: pid, leader: leader, dur: dur}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", pidfile)
	return startedSleeper{}
}

// guardSleeper registers the test's safety net: whatever happens, the one
// process this test started is killed at cleanup, and only if it is verifiably
// in the test's own process group.
func guardSleeper(t *testing.T, pidfile, dur string) {
	t.Helper()
	t.Cleanup(func() {
		s := startedSleeper{pid: readPidFile(pidfile), leader: readPidFile(pidfile + ".leader"), dur: dur}
		if s.isOurSleeper() {
			_ = syscall.Kill(s.pid, syscall.SIGKILL)
		}
	})
}

// gone polls briefly for the process to disappear (a killed process is a zombie
// until its new parent reaps it).
func (s startedSleeper) gone(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if !s.isOurSleeper() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A pn-initiated stop (context cancel) must reach a grandchild that a
// darwin-rebuild-shaped script runs in a pipeline inside $(...) (pg2-sdbhh4.20).
// Real processes, the real runner, a short injected kill grace.
func TestRealRunner_CancelReachesGrandchildInPipeline(t *testing.T) {
	for _, mode := range []string{"held", "detached", "ignoreterm"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "pid")
			dur := uniqueSleepDuration()
			guardSleeper(t, pidfile, dur)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type out struct{ err error }
			done := make(chan out, 1)
			go func() {
				_, err := NewRealRunnerWithKillGrace(300*time.Millisecond).Run(ctx, "bash",
					[]string{"-c", darwinRebuildShaped, "bash", pidfile, dur, mode}, RunOptions{OwnProcessGroup: true})
				done <- out{err}
			}()
			sleeper := waitForSleeper(t, pidfile, dur)
			if !sleeper.isOurSleeper() {
				t.Fatalf("the long child (pid %d) is not running before the cancel", sleeper.pid)
			}
			start := time.Now()
			cancel()
			select {
			case o := <-done:
				if o.err == nil {
					t.Error("a cancelled command must return an error")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after cancel")
			}
			if !sleeper.gone(2 * time.Second) {
				t.Errorf("the grandchild (pid %d, sleep %s) survived the cancel and the kill grace", sleeper.pid, dur)
			}
			if elapsed := time.Since(start); mode == "ignoreterm" && elapsed < 250*time.Millisecond {
				t.Errorf("a SIGTERM-ignoring grandchild died after %v, before the 300ms grace ended: SIGKILL came too early", elapsed)
			}
		})
	}
}

// The group is opt-in: a call that does not ask for it (a sudo child, a hook)
// stays in pn's own process group, so it keeps the terminal and SIGINT-from-tty
// behaviour it has today; a call that asks leads a NEW group.
func TestRealRunner_OwnProcessGroupIsOptIn(t *testing.T) {
	r := NewRealRunnerWithKillGrace(300 * time.Millisecond)
	// The pgid is read with getpgid(2) on the live child, not with `ps` (which
	// the nix darwin sandbox refuses to exec): the script writes its pid, then
	// becomes a long sleep that the test kills once it has read the pgid.
	pgidOf := func(own bool) (pgid, pid int) {
		t.Helper()
		pidfile := filepath.Join(t.TempDir(), "pid")
		done := make(chan error, 1)
		go func() {
			_, err := r.Run(context.Background(), "sh", []string{"-c", `echo $$ > "$1.leader"; echo $$ > "$1"; exec sleep 30`, "sh", pidfile},
				RunOptions{OwnProcessGroup: own})
			done <- err
		}()
		pid = waitForSleeper(t, pidfile, "").pid
		var err error
		pgid, err = syscall.Getpgid(pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-done
		if err != nil {
			t.Fatalf("Getpgid(%d): %v", pid, err)
		}
		return pgid, pid
	}
	if pgid, _ := pgidOf(false); pgid != syscall.Getpgrp() {
		t.Errorf("without OwnProcessGroup the child's pgid = %d, want pn's own %d", pgid, syscall.Getpgrp())
	}
	if pgid, pid := pgidOf(true); pgid != pid || pgid == syscall.Getpgrp() {
		t.Errorf("with OwnProcessGroup the child's pgid = %d (pid %d, pn's group %d), want its own pid", pgid, pid, syscall.Getpgrp())
	}
}

// An interactive Ctrl-C reaches pn alone once the child is in its own group;
// pn's handler (internal/cli/root.go: signal.NotifyContext on SIGINT) cancels
// the context, and the child must then see exactly ONE SIGTERM and no SIGINT.
// The test signals only itself (its own pid) and the child it started.
func TestRealRunner_CtrlCReachesChildExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "signals"), filepath.Join(dir, "ready")
	script := `trap 'echo TERM >> "$1"' TERM; trap 'echo INT >> "$1"' INT; touch "$2"; while :; do sleep 0.05; done`

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := NewRealRunnerWithKillGrace(300*time.Millisecond).Run(ctx, "bash",
			[]string{"-c", script, "bash", marker, ready}, RunOptions{OwnProcessGroup: true})
		done <- err
	}()
	waitForFile(t, ready)
	// What the terminal does on Ctrl-C, reduced to the part that reaches pn.
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("an interrupted command must return an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the interrupt")
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the child saw no signal at all: %v", err)
	}
	if string(got) != "TERM\n" {
		t.Errorf("the child saw signals %q, want exactly one TERM", got)
	}
}
