package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// applyLogRun runs Apply with a REAL child process as the apply command (a shell
// script body), git/nix probes faked. It returns the terminal output, Apply's
// the apply-logs directory, how long Apply took and Apply's error.
func applyLogRun(t *testing.T, scriptBody string, grace time.Duration) (out, logDir string, took time.Duration, err error) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)
	trustWS(t, root)

	script := filepath.Join(t.TempDir(), "fake-rebuild.sh")
	if werr := os.WriteFile(script, []byte("#!/bin/sh\n"+scriptBody), 0o755); werr != nil {
		t.Fatal(werr)
	}
	f, _ := applyTestRunner(t, root)
	h := &hybridRunner{realName: script, real: exec.NewRealRunnerWithKillGrace(grace), fake: f}
	w, oerr := Open(root, h)
	if oerr != nil {
		t.Fatalf("Open: %v", oerr)
	}
	buf := &bytes.Buffer{}
	begin := time.Now()
	err = w.Apply(context.Background(), buf, ApplyOptions{Force: true, ApplyCmd: script})
	// trustWS re-points XDG_STATE_HOME, so resolve the directory only now.
	return buf.String(), applyLogDir(), time.Since(begin), err
}

// onlyApplyLog returns the contents of the single apply-*.log in dir.
func onlyApplyLog(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one apply log in %s, got %v (err %v)", dir, matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logOutputSection is the text between the output marker and the trailer.
func logOutputSection(t *testing.T, log string) string {
	t.Helper()
	_, rest, ok := strings.Cut(log, applyLogOutput+"\n")
	if !ok {
		t.Fatalf("log has no output marker:\n%s", log)
	}
	body, _, ok := strings.Cut(rest, "\n"+applyLogTrailer+"\n")
	if !ok {
		t.Fatalf("log has no trailer:\n%s", log)
	}
	return body
}

func logTrailer(t *testing.T, log string) string {
	t.Helper()
	_, tr, ok := strings.Cut(log, applyLogTrailer+"\n")
	if !ok {
		t.Fatalf("log has no trailer:\n%s", log)
	}
	return tr
}

func TestApplyLog_RetainsOutputForExitZero(t *testing.T) {
	out, dir, _, err := applyLogRun(t, "echo to-stdout\necho to-stderr 1>&2\nexit 0\n", time.Second)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	log := onlyApplyLog(t, dir)
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(logOutputSection(t, log), want) {
			t.Errorf("log output section missing %q:\n%s", want, log)
		}
	}
	tr := logTrailer(t, log)
	for _, want := range []string{"ended_at: ", "exit_status: 0\n", "result: success\n"} {
		if !strings.Contains(tr, want) {
			t.Errorf("trailer missing %q:\n%s", want, tr)
		}
	}
	// Header: start time, terminal, and the applied ref of each repo (leaf -> "l"
	// per applyTestRunner's fake `git rev-parse HEAD`).
	for _, want := range []string{applyLogHeader + "\n", "started_at: ", "terminal: leaf\n", applyLogRefs + "\nleaf: l\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	// The terminal must carry everything the log's output section does.
	if body := logOutputSection(t, log); !strings.HasSuffix(strings.TrimRight(out, "\n"), strings.TrimRight(body, "\n")) {
		t.Errorf("log output section is not what the terminal received.\nterminal:\n%s\nlog body:\n%s", out, body)
	}
	if strings.Contains(out, applyLogHeader) || strings.Contains(out, applyLogTrailer) {
		t.Errorf("log framing leaked onto the terminal:\n%s", out)
	}
}

func TestApplyLog_RetainsOutputForNonZeroExit(t *testing.T) {
	_, dir, _, err := applyLogRun(t, "echo before-failure\necho boom 1>&2\nexit 3\n", time.Second)
	if err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("Apply must fail with 'apply failed', got: %v", err)
	}
	log := onlyApplyLog(t, dir)
	body := logOutputSection(t, log)
	for _, want := range []string{"before-failure", "boom"} {
		if !strings.Contains(body, want) {
			t.Errorf("log output section missing %q:\n%s", want, log)
		}
	}
	tr := logTrailer(t, log)
	for _, want := range []string{"exit_status: 3\n", "result: failed\n", "error: apply failed"} {
		if !strings.Contains(tr, want) {
			t.Errorf("trailer missing %q:\n%s", want, tr)
		}
	}
	if strings.Contains(tr, "result: success") {
		t.Errorf("a failed apply must never read as success:\n%s", tr)
	}
}

// A grandchild that holds the output pipe open for a while but within the kill
// grace must not lose lines: its output is in the log AND on the terminal.
func TestApplyLog_GrandchildWithinGraceLosesNoLines(t *testing.T) {
	body := "(sleep 0.2; echo late-line-from-grandchild) &\necho main-line\nexit 0\n"
	out, dir, _, err := applyLogRun(t, body, 5*time.Second)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	log := onlyApplyLog(t, dir)
	for _, want := range []string{"main-line", "late-line-from-grandchild"} {
		if !strings.Contains(logOutputSection(t, log), want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
		if !strings.Contains(out, want) {
			t.Errorf("terminal missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(logTrailer(t, log), "result: success") {
		t.Errorf("want success trailer:\n%s", log)
	}
}

// A grandchild that outlives the grace must not hang the apply, and what was
// written before the pipe was abandoned must be retained; the log still ends in
// a complete trailer (exit 0 and exit non-zero).
func TestApplyLog_GrandchildPastGraceDoesNotHang(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		want string
	}{{"exit0", 0, "result: success"}, {"exit3", 3, "result: failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf("(sleep 5) &\necho line-before-exit\nexit %d\n", tc.exit)
			_, dir, took, _ := applyLogRun(t, body, 300*time.Millisecond)
			if took > 4*time.Second {
				t.Fatalf("Apply took %s: it waited for the lingering grandchild", took)
			}
			log := onlyApplyLog(t, dir)
			if !strings.Contains(logOutputSection(t, log), "line-before-exit") {
				t.Errorf("log lost the command's own output:\n%s", log)
			}
			if !strings.Contains(logTrailer(t, log), tc.want) {
				t.Errorf("trailer missing %q:\n%s", tc.want, log)
			}
		})
	}
}

func TestApplyLog_LatestSymlinkPointsAtNewestLog(t *testing.T) {
	_, dir, _, err := applyLogRun(t, "echo hi\n", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target, lerr := os.Readlink(filepath.Join(dir, "latest"))
	if lerr != nil {
		t.Fatalf("no latest symlink: %v", lerr)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	if len(matches) != 1 || filepath.Base(matches[0]) != target {
		t.Errorf("latest -> %q, but log is %v", target, matches)
	}
}

func TestApplyLog_LogDirOutsideNixStoreAndReadable(t *testing.T) {
	_, dir, _, err := applyLogRun(t, "echo hi\n", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(dir, "/nix/store") {
		t.Errorf("log dir %s is in the nix store", dir)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	st, serr := os.Stat(m[0])
	if serr != nil {
		t.Fatal(serr)
	}
	if st.Mode().Perm()&0o044 != 0o044 {
		t.Errorf("log mode %v is not group/other readable", st.Mode().Perm())
	}
}

// An apply that skips the rebuild must not create a log (or it would evict real
// ones from the bounded set).
func TestApplyLog_SkippedRebuildWritesNoLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	if err := writeAppliedState(leafDir, AppliedState{AppliedRef: "abc"}); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(applyLogDir(), "apply-*.log")); len(m) != 0 {
		t.Errorf("skipped apply wrote a log: %v", m)
	}
}

// A log that cannot be created must not fail the apply or change what the
// terminal shows beyond one warning line.
func TestApplyLog_UnwritableStateDirDoesNotFailApply(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applySingleRepoTOML)
	trustWS(t, root) // re-points XDG_STATE_HOME at a fresh temp dir
	// A regular file where the log directory must go makes MkdirAll fail.
	if err := os.MkdirAll(filepath.Dir(applyLogDir()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(applyLogDir(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake-rebuild.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho still-applied\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, _ := applyTestRunner(t, root)
	h := &hybridRunner{realName: script, real: exec.NewRealRunner(), fake: f}
	w, err := Open(root, h)
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{Force: true, ApplyCmd: script}); err != nil {
		t.Fatalf("Apply must still succeed: %v", err)
	}
	if !strings.Contains(out.String(), "still-applied") || !strings.Contains(out.String(), "could not retain the apply log") {
		t.Errorf("want command output plus a warning, got:\n%s", out.String())
	}
}

// Retention rule: only the newest applyLogKeep apply-*.log files survive a new
// log being opened; unrelated files are never touched; the new log survives.
func TestApplyLog_RetentionKeepsNewestN(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "pn", "apply-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const existing = applyLogKeep + 15
	for i := 0; i < existing; i++ {
		name := fmt.Sprintf("apply-20260101T0000%02dZ-1.log", i) // ascending start times
		if i >= 60 {
			t.Fatal("test fixture exceeds 60 seconds")
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyLogNow = func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { applyLogNow = time.Now })

	l, err := openApplyLog("leaf", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	l.finish(nil)

	logs, _ := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	if len(logs) != applyLogKeep {
		t.Fatalf("after opening a new log want exactly %d apply logs, got %d", applyLogKeep, len(logs))
	}
	if _, err := os.Stat(l.path); err != nil {
		t.Errorf("the new log was pruned: %v", err)
	}
	// The oldest existing ones are the ones removed; the newest existing survive.
	if _, err := os.Stat(filepath.Join(dir, "apply-20260101T000000Z-1.log")); !os.IsNotExist(err) {
		t.Errorf("oldest log should have been pruned (err=%v)", err)
	}
	newest := fmt.Sprintf("apply-20260101T0000%02dZ-1.log", existing-1)
	if _, err := os.Stat(filepath.Join(dir, newest)); err != nil {
		t.Errorf("newest pre-existing log %s should survive: %v", newest, err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated file must never be pruned: %v", err)
	}
}

// pruneApplyLogs never removes the protected log even if its name sorts oldest
// (a clock that went backwards).
func TestApplyLog_PruneNeverRemovesProtected(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"apply-1.log", "apply-2.log", "apply-3.log"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneApplyLogs(dir, 1, "apply-1.log")
	left, _ := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	if len(left) != 2 { // apply-3 (newest kept) + apply-1 (protected)
		t.Errorf("want newest + protected to survive, got %v", left)
	}
}

// A file with no trailer is how a killed apply looks; it must not parse as
// success. (Documents the contract; the trailer is written only by finish.)
func TestApplyLog_UnfinishedLogHasNoTrailer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	l, err := openApplyLog("leaf", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Write([]byte("partial output\n"))
	b, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), applyLogTrailer) || strings.Contains(string(b), "result: success") {
		t.Errorf("an unfinished log must carry no trailer:\n%s", b)
	}
	l.finish(fmt.Errorf("interrupted"))
	b, _ = os.ReadFile(l.path)
	if !strings.Contains(string(b), "result: failed") || !strings.Contains(string(b), "exit_status: none") {
		t.Errorf("finish(err) before the command ran must read failed / none:\n%s", b)
	}
}
