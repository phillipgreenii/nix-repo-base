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
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
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
	pruneRunLogs(dir, applyLogPrefix, 1, "apply-1.log")
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

// --- run logs shared by build and apply (bead pg2-sdbhh4.14) ---

// stubNixConf points the header's nix.conf read at a temp file with body.
func stubNixConf(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nix.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := nixConfPath
	nixConfPath = path
	t.Cleanup(func() { nixConfPath = old })
}

// stepClock makes applyLogNow advance by one second per call, so every phase
// gets a positive, deterministic-in-shape elapsed time.
func stepClock(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var n int
	applyLogNow = func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
	t.Cleanup(func() { applyLogNow = time.Now })
}

// logFile reads the log a `latest*` link points at.
func logFile(t *testing.T, link string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(applyLogDir(), link))
	if err != nil {
		t.Fatalf("read %s: %v", link, err)
	}
	return string(b)
}

// logKey is the value of the first `key: value` line in text, or "<absent>".
func logKey(text, key string) string {
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(l, key+": "); ok {
			return v
		}
	}
	return "<absent>"
}

// A build retains its output like apply: a build-*.log, the latest-build link,
// the header context, the plan and phase lines in the trailer only, and the
// output section byte-identical to what the terminal received.
func TestBuildLog_RetainedWithContextAndPhases(t *testing.T) {
	stubNixConf(t, "# comment\nmax-jobs = 3\ncores = 1\nsubstituters = https://secret.example.invalid\n")
	stepClock(t)
	env := newPlanVerbEnv(t, "")
	env.runner.chunks = smallPlanChunks()
	tel, _ := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	out, err := env.run(ctx, "build")
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := applyLogDir()
	if m, _ := filepath.Glob(filepath.Join(dir, "build-*.log")); len(m) != 1 {
		t.Fatalf("want one build log, got %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "apply-*.log")); len(m) != 0 {
		t.Fatalf("a build must not write an apply log: %v", m)
	}
	if _, err := os.Lstat(filepath.Join(dir, "latest")); err == nil {
		t.Error("a build must not repoint `latest` (the latest APPLY log)")
	}
	log := logFile(t, "latest-build")
	if !strings.HasPrefix(log, buildLogHeader+"\n") {
		t.Errorf("build log header marker wrong:\n%s", log)
	}
	header, _, _ := strings.Cut(log, applyLogOutput)
	if got := logKey(header, "trace_id"); len(got) != 32 {
		t.Errorf("trace_id = %q, want 32 hex chars", got)
	}
	for key, want := range map[string]string{
		"log_format": "2", "rebuild_reason": "no_record", "max_jobs": "3", "cores": "1", "terminal": "leaf",
	} {
		if got := logKey(header, key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(log, "secret.example.invalid") || strings.Contains(log, "substituters") {
		t.Errorf("nix.conf text beyond max-jobs/cores leaked into a world-readable log:\n%s", log)
	}
	// Output section: byte-identical to what the writer (the terminal) received,
	// minus the banner printed before the log opened.
	body := logOutputSection2(t, log, buildLogTrailer)
	if !strings.HasPrefix(body, "apply would: ") || !strings.Contains(body, strings.Join(env.runner.chunks, "")) {
		t.Errorf("output section lacks the apply-would line or the child's bytes:\n%q", body)
	}
	if !strings.HasSuffix(out, body+"\n") && !strings.HasSuffix(out, body) {
		t.Errorf("output section is not a suffix of the terminal output.\nterminal:\n%q\nlog:\n%q", out, body)
	}
	tr := strings.SplitN(log, buildLogTrailer+"\n", 2)[1]
	for _, k := range []string{"prelude_seconds", "build_seconds"} {
		if v := logKey(tr, k); v == "<absent>" || v == "0.0" {
			t.Errorf("trailer %s = %q", k, v)
		}
	}
	for _, k := range []string{"activate_seconds", "post_steps_seconds"} {
		if logKey(tr, k) != "<absent>" {
			t.Errorf("a build has no %s phase:\n%s", k, tr)
		}
	}
	if got := logKey(tr, "plan"); got != "builds=24 fetches=3 download_mib=1.5" {
		t.Errorf("plan = %q", got)
	}
	for _, k := range []string{"prelude_seconds", "build_seconds", "plan:"} {
		if strings.Contains(body, k) {
			t.Errorf("trailer-only key %q appeared in the output section", k)
		}
	}
	if logKey(tr, "exit_status") != "0" || logKey(tr, "result") != "success" {
		t.Errorf("trailer:\n%s", tr)
	}
}

// logOutputSection2 is logOutputSection for either verb's trailer marker.
func logOutputSection2(t *testing.T, log, trailer string) string {
	t.Helper()
	_, rest, ok := strings.Cut(log, applyLogOutput+"\n")
	if !ok {
		t.Fatalf("no output marker:\n%s", log)
	}
	body, _, ok := strings.Cut(rest, "\n"+trailer+"\n")
	if !ok {
		t.Fatalf("no trailer %q:\n%s", trailer, log)
	}
	return body
}

// A failed build is retained with its exit status and the plan it printed.
func TestBuildLog_FailedBuildRetainsOutputAndExit(t *testing.T) {
	env := newPlanVerbEnv(t, "")
	env.runner.chunks = []string{"building 'x'\n", "error: boom\n"}
	env.runner.runErr = &exec.CommandError{Result: exec.Result{ExitCode: 7}}
	if _, err := env.run(context.Background(), "build"); err == nil {
		t.Fatal("want a failed build")
	}
	log := logFile(t, "latest-build")
	if !strings.Contains(logOutputSection2(t, log, buildLogTrailer), "error: boom") {
		t.Errorf("failed build output not retained:\n%s", log)
	}
	tr := strings.SplitN(log, buildLogTrailer+"\n", 2)[1]
	if logKey(tr, "exit_status") != "7" || logKey(tr, "result") != "failed" {
		t.Errorf("trailer:\n%s", tr)
	}
}

// An apply log (legacy single-command flow) carries the same header context and
// prelude, build and post_steps phases, but no activate phase; `latest` is the
// apply log and `latest-build` stays absent.
func TestApplyLog_HeaderContextAndPhases(t *testing.T) {
	stubNixConf(t, "max-jobs = auto\nbuild-cores = 8\n")
	stepClock(t)
	env := newPlanVerbEnv(t, "")
	env.runner.chunks = smallPlanChunks()
	tel, _ := telemetrytest.New()
	if out, err := env.run(telemetry.WithTelemetry(context.Background(), tel), "apply"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	log := logFile(t, "latest")
	header, _, _ := strings.Cut(log, applyLogOutput)
	for key, want := range map[string]string{"log_format": "2", "rebuild_reason": "forced", "max_jobs": "auto", "cores": "8"} {
		if got := logKey(header, key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
	if got := logKey(header, "trace_id"); len(got) != 32 {
		t.Errorf("trace_id = %q", got)
	}
	tr := logTrailer(t, log)
	for _, k := range []string{"prelude_seconds", "build_seconds", "post_steps_seconds"} {
		if logKey(tr, k) == "<absent>" {
			t.Errorf("trailer lacks %s:\n%s", k, tr)
		}
	}
	if logKey(tr, "activate_seconds") != "<absent>" {
		t.Errorf("legacy apply has no separate activate phase:\n%s", tr)
	}
	if logKey(tr, "plan") != "builds=24 fetches=3 download_mib=1.5" {
		t.Errorf("plan line wrong:\n%s", tr)
	}
	if _, err := os.Lstat(filepath.Join(applyLogDir(), "latest-build")); err == nil {
		t.Error("an apply must not write latest-build")
	}
}

// Without telemetry the header says so, and an unreadable nix.conf reads unknown.
func TestRunLog_HeaderWithoutTelemetryOrNixConf(t *testing.T) {
	old := nixConfPath
	nixConfPath = filepath.Join(t.TempDir(), "absent.conf")
	t.Cleanup(func() { nixConfPath = old })
	env := newPlanVerbEnv(t, "")
	if _, err := env.run(context.Background(), "build"); err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(logFile(t, "latest-build"), applyLogOutput)
	for key, want := range map[string]string{"trace_id": "none", "max_jobs": "unknown", "cores": "unknown"} {
		if got := logKey(header, key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
}

func TestReadNixConfJobs(t *testing.T) {
	for _, tc := range []struct {
		name, body, jobs, cores string
	}{
		{"plain", "max-jobs = 3\ncores = 1\n", "3", "1"},
		{"last wins and comments", "max-jobs = 2 # old\nmax-jobs = 6\ncores=4\n", "6", "4"},
		{"legacy build-cores", "build-cores = 0\n", "unknown", "0"},
		{"non-token values are ignored", "max-jobs = https://x.invalid/a b\ncores = $(id)\n", "unknown", "unknown"},
		{"other keys never read", "substituters = https://x.invalid\n", "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubNixConf(t, tc.body)
			if j, c := readNixConfJobs(nixConfPath); j != tc.jobs || c != tc.cores {
				t.Errorf("got (%q, %q), want (%q, %q)", j, c, tc.jobs, tc.cores)
			}
		})
	}
}

// Phase accounting, exactly: prelude from Begin, then each phase until the next.
func TestRunLog_PhaseTimesAreExact(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t0 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	now := t0.Add(2 * time.Second) // 2s of prelude already elapsed at open
	applyLogNow = func() time.Time { return now }
	t.Cleanup(func() { applyLogNow = time.Now })
	l, err := openRunLog(runLogOpts{Verb: "apply", Terminal: "leaf", Cmdline: []string{"x"}, Begin: t0})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		phase string
		after time.Duration
	}{{phaseBuild, 5 * time.Second}, {phaseActivate, 3 * time.Second}, {phasePostSteps, 1500 * time.Millisecond}} {
		now = now.Add(step.after)
		l.phase(step.phase)
	}
	now = now.Add(time.Second)
	l.finish(nil)
	b, _ := os.ReadFile(l.path)
	tr := strings.SplitN(string(b), applyLogTrailer+"\n", 2)[1]
	// The build phase starts when phase(build) is called, so the prelude is the
	// 2s before open plus the 5s before that call: 7s.
	for k, want := range map[string]string{
		"prelude_seconds": "7.0", "build_seconds": "3.0", "activate_seconds": "1.5", "post_steps_seconds": "1.0",
	} {
		if got := logKey(tr, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if logKey(tr, "plan") != "<absent>" {
		t.Errorf("no plan was recorded, yet:\n%s", tr)
	}
}

// Retention is per verb: a burst of builds never evicts apply logs, nor the
// reverse, and each keeps exactly applyLogKeep.
func TestRunLog_RetentionIsPerVerb(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "pn", "apply-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := func(prefix string, n int) {
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%s20260101T0000%02dZ-1.log", prefix, i)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(applyLogPrefix, applyLogKeep)
	seed(buildLogPrefix, applyLogKeep)
	applyLogNow = func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { applyLogNow = time.Now })

	// A burst of builds (distinct pids are not available, so distinct start times).
	for i := 0; i < 5; i++ {
		at := time.Date(2026, 6, 1, 12, 0, i, 0, time.UTC)
		applyLogNow = func() time.Time { return at }
		l, err := openRunLog(runLogOpts{Verb: "build", Terminal: "leaf", Cmdline: []string{"x"}})
		if err != nil {
			t.Fatal(err)
		}
		l.finish(nil)
	}
	applyLogs, _ := filepath.Glob(filepath.Join(dir, "apply-*.log"))
	buildLogs, _ := filepath.Glob(filepath.Join(dir, "build-*.log"))
	if len(applyLogs) != applyLogKeep {
		t.Errorf("a burst of builds changed the apply logs: %d, want %d", len(applyLogs), applyLogKeep)
	}
	if len(buildLogs) != applyLogKeep {
		t.Errorf("build logs = %d, want %d", len(buildLogs), applyLogKeep)
	}

	// And the reverse: a burst of applies leaves the build logs alone.
	for i := 0; i < 5; i++ {
		at := time.Date(2026, 6, 1, 13, 0, i, 0, time.UTC)
		applyLogNow = func() time.Time { return at }
		l, err := openApplyLog("leaf", []string{"x"})
		if err != nil {
			t.Fatal(err)
		}
		l.finish(nil)
	}
	applyLogs, _ = filepath.Glob(filepath.Join(dir, "apply-*.log"))
	buildLogs2, _ := filepath.Glob(filepath.Join(dir, "build-*.log"))
	if len(applyLogs) != applyLogKeep || len(buildLogs2) != len(buildLogs) {
		t.Errorf("after an apply burst: apply=%d build=%d (was %d)", len(applyLogs), len(buildLogs2), len(buildLogs))
	}
}

// `latest` follows the newest apply log and `latest-build` the newest build log,
// independently of each other.
func TestRunLog_LatestLinksAreIndependent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	open := func(verb string, sec int) *applyLog {
		at := time.Date(2026, 6, 1, 12, 0, sec, 0, time.UTC)
		applyLogNow = func() time.Time { return at }
		l, err := openRunLog(runLogOpts{Verb: verb, Terminal: "leaf", Cmdline: []string{"x"}})
		if err != nil {
			t.Fatal(err)
		}
		l.finish(nil)
		return l
	}
	t.Cleanup(func() { applyLogNow = time.Now })
	open("apply", 1)
	b := open("build", 2)
	a2 := open("apply", 3)
	for link, want := range map[string]string{"latest": a2.path, "latest-build": b.path} {
		got, err := os.Readlink(filepath.Join(applyLogDir(), link))
		if err != nil || got != filepath.Base(want) {
			t.Errorf("%s -> %q (%v), want %q", link, got, err, filepath.Base(want))
		}
	}
}

// A skipped build-nothing case: a --show-nix-commands dry run runs nothing and
// so retains nothing.
func TestBuildLog_DryRunWritesNoLog(t *testing.T) {
	env := newPlanVerbEnv(t, "")
	var out bytes.Buffer
	if err := env.w.Build(context.Background(), &out, BuildOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(applyLogDir(), "*.log")); len(m) != 0 {
		t.Errorf("dry run wrote a log: %v", m)
	}
}

// Format compatibility: a version 1 log (no log_format, no new header keys, no
// phase lines) is read by the documented reader (cut at the trailer marker, read
// `key: value` lines) exactly as before; the new keys are simply absent.
func TestRunLog_Version1LogStillReadable(t *testing.T) {
	v1 := strings.Join([]string{
		applyLogHeader, "started_at: 2026-10-20T01:02:03Z", "terminal: leaf", "command: sudo darwin-rebuild switch",
		applyLogRefs, "leaf: abc123", applyLogOutput, "building...", "", applyLogTrailer,
		"ended_at: 2026-10-20T01:09:03Z", "exit_status: 0", "result: success", "",
	}, "\n")
	_, trailer, ok := strings.Cut(v1, applyLogTrailer+"\n")
	if !ok {
		t.Fatal("no trailer")
	}
	for k, want := range map[string]string{"exit_status": "0", "result": "success", "ended_at": "2026-10-20T01:09:03Z"} {
		if got := logKey(trailer, k); got != want {
			t.Errorf("v1 %s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"prelude_seconds", "build_seconds", "plan", "error"} {
		if logKey(trailer, k) != "<absent>" {
			t.Errorf("v1 trailer unexpectedly has %s", k)
		}
	}
	header, _, _ := strings.Cut(v1, applyLogOutput)
	if logKey(header, "log_format") != "<absent>" {
		t.Error("a v1 log has no log_format line (that absence IS the version marker)")
	}
	// A v2 log keeps every v1 trailer key at the same place and meaning.
	l, err := openApplyLog("leaf", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	l.setExit(nil)
	l.finish(nil)
	b, _ := os.ReadFile(l.path)
	_, tr2, _ := strings.Cut(string(b), applyLogTrailer+"\n")
	for k, want := range map[string]string{"exit_status": "0", "result": "success"} {
		if got := logKey(tr2, k); got != want {
			t.Errorf("v2 %s = %q, want %q", k, got, want)
		}
	}
}

// Log files stay world-readable (0644) for both verbs.
func TestRunLog_ModeIs0644ForBothVerbs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, verb := range []string{"apply", "build"} {
		l, err := openRunLog(runLogOpts{Verb: verb, Terminal: "leaf", Cmdline: []string{"x"}})
		if err != nil {
			t.Fatal(err)
		}
		l.finish(nil)
		st, _ := os.Stat(l.path)
		if st.Mode().Perm()&0o044 != 0o044 {
			t.Errorf("%s log mode = %v, want 0644", verb, st.Mode().Perm())
		}
	}
}
