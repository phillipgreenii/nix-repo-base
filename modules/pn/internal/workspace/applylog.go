package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Apply log: a retained, unprivileged-readable copy of everything a
// `pn workspace apply` that REBUILDS wrote to the operator's terminal (bead
// pg2-xdvbj). Post-apply verification beads ask "does the apply output show X";
// without a retained copy that can only be inferred.
//
// Location: ${XDG_STATE_HOME:-$HOME/.local/state}/pn/apply-logs/apply-<UTC
// yyyymmddThhmmssZ>-<pid>.log, plus a `latest` symlink to the newest one. It is
// a subdirectory of pn's state dir on purpose: the Loki collector globs
// pn/*.jsonl, so these logs are never ingested, and `nix-logs` (the wrapper's raw
// nix JSON, root-only for a sudo apply) is untouched.
//
// Retention: the newest applyLogKeep files (by name, which sorts by start time)
// are kept; older ones are removed whenever a new log is opened. Only applies
// that actually rebuild create a log, so no-op applies never evict real ones.
//
// File layout (plain text; `key: value` lines are machine-readable):
//
//	--== pn apply log ==--
//	started_at: <RFC3339 UTC>
//	terminal: <repo key>
//	command: <argv>
//	--== applied refs ==--           (eval-time HEAD of each repo, written just
//	<repo>: <sha>                     before the build; absent if capture failed)
//	--== output ==--
//	<combined stdout+stderr of the apply, exactly as the terminal got it>
//	--== pn apply end ==--
//	ended_at: <RFC3339 UTC>
//	exit_status: <apply command's exit code | none>
//	result: success|failed
//	error: <only when failed>
//
// A file WITHOUT the `--== pn apply end ==--` trailer is an apply that was killed
// before pn could finish (SIGKILL, power loss): it must be read as incomplete,
// never as success. exit_status is -1 when the command was killed by a signal
// (including an interrupted apply), and `none` when the command never ran.
const (
	applyLogKeep   = 30
	applyLogPrefix = "apply-"
	applyLogSuffix = ".log"

	applyLogHeader  = "--== pn apply log ==--"
	applyLogRefs    = "--== applied refs ==--"
	applyLogOutput  = "--== output ==--"
	applyLogTrailer = "--== pn apply end ==--"
)

// applyLogNow is the clock; a test seam.
var applyLogNow = time.Now

// applyLogDir is the directory holding apply logs.
func applyLogDir() string {
	return filepath.Join(filepath.Dir(eventlog.DefaultPath()), "apply-logs")
}

// applyLog is an open apply log. A nil *applyLog is valid and a no-op, so a
// failure to retain the log can never fail or alter the apply.
type applyLog struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	exit     string // exit_status line value; "none" until the command ran
	finished bool
	// steps is true for the build-as-user flow, whose trailer also records
	// failed_step. The legacy single-command flow never writes that line, so its
	// log stays exactly as it was.
	steps      bool
	failedStep string
}

// openApplyLog creates the log, writes its header, repoints `latest` and prunes
// old logs. On any failure it returns (nil, err); the caller warns and carries on.
func openApplyLog(terminal string, cmdline []string) (*applyLog, error) {
	return openApplyLogWith(terminal, cmdline, nil)
}

// openApplyLogWith is openApplyLog with extra header lines (`key: value`)
// written after `command:`: the later steps of a multi-step apply.
func openApplyLogWith(terminal string, cmdline []string, extra []string) (*applyLog, error) {
	dir := applyLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	start := applyLogNow().UTC()
	name := fmt.Sprintf("%s%s-%d%s", applyLogPrefix, start.Format("20060102T150405Z"), os.Getpid(), applyLogSuffix)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	l := &applyLog{f: f, path: path, exit: "none"}
	l.writef("%s\nstarted_at: %s\nterminal: %s\ncommand: %s\n",
		applyLogHeader, start.Format(time.RFC3339), terminal, strings.Join(cmdline, " "))
	for _, line := range extra {
		l.writef("%s\n", line)
	}
	repointLatest(dir, name)
	pruneApplyLogs(dir, applyLogKeep, name)
	return l, nil
}

func (l *applyLog) writef(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_, _ = fmt.Fprintf(l.f, format, a...)
	}
}

// Write implements io.Writer. It NEVER returns an error: it sits in an
// io.MultiWriter beside the terminal, and a log failure must not truncate or fail
// what the operator sees.
func (l *applyLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && !l.finished {
		_, _ = l.f.Write(p)
	}
	return len(p), nil
}

// tee returns a writer that sends to out AND the log. Nil-safe.
func (l *applyLog) tee(out io.Writer) io.Writer {
	if l == nil {
		return out
	}
	return io.MultiWriter(out, l)
}

// recordRefs writes the refs section and opens the output section. Called once,
// immediately before the apply command starts; refs is nil when capture failed.
func (l *applyLog) recordRefs(refs map[string]string) {
	if l == nil {
		return
	}
	var b strings.Builder
	if len(refs) > 0 {
		names := make([]string, 0, len(refs))
		for n := range refs {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString(applyLogRefs + "\n")
		for _, n := range names {
			fmt.Fprintf(&b, "%s: %s\n", n, refs[n])
		}
	}
	b.WriteString(applyLogOutput + "\n")
	l.writef("%s", b.String())
}

// setExit records the apply command's outcome: nil = exit 0.
func (l *applyLog) setExit(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		l.exit = "0"
		return
	}
	var ce *exec.CommandError
	if errors.As(err, &ce) {
		l.exit = fmt.Sprint(ce.Result.ExitCode)
	}
	// Otherwise the command never ran (could not start): stays "none".
}

// useSteps switches the log to the build-as-user flow's trailer, which adds
// failed_step (none until a step fails).
func (l *applyLog) useSteps() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = true
	l.failedStep = failedStepNone
}

// fail records the step that failed and, when err is (or wraps) a subprocess
// failure, its exit status (-1 for a signal). A step that is not a process
// (read-result) passes a nil err and leaves exit_status as it was.
func (l *applyLog) fail(step string, err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.failedStep = step
	l.mu.Unlock()
	if err != nil {
		l.setExit(err)
	}
}

// failExit is fail for a step that ran a process and failed by judgement of its
// output (the probe): exit_status is that process's exit code.
func (l *applyLog) failExit(step string, code int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failedStep = step
	l.exit = fmt.Sprint(code)
}

// finish writes the trailer and closes the file. runErr is Apply's final error.
func (l *applyLog) finish(runErr error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil || l.finished {
		return
	}
	l.finished = true
	result := "success"
	if runErr != nil {
		result = "failed"
	}
	var b strings.Builder
	// Start the trailer on its own line even if the output lacked a final newline.
	fmt.Fprintf(&b, "\n%s\nended_at: %s\nexit_status: %s\n",
		applyLogTrailer, applyLogNow().UTC().Format(time.RFC3339), l.exit)
	if l.steps {
		fmt.Fprintf(&b, "failed_step: %s\n", l.failedStep)
	}
	fmt.Fprintf(&b, "result: %s\n", result)
	if runErr != nil {
		fmt.Fprintf(&b, "error: %s\n", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
	_, _ = l.f.WriteString(b.String())
	_ = l.f.Close()
	l.f = nil
}

// repointLatest atomically makes <dir>/latest a symlink to name. Best-effort.
func repointLatest(dir, name string) {
	tmp := filepath.Join(dir, fmt.Sprintf(".latest-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, "latest")); err != nil {
		_ = os.Remove(tmp)
	}
}

// pruneApplyLogs removes all but the newest `keep` apply logs in dir (newest =
// highest name; names embed a UTC start time). `protect` is never removed.
// Best-effort; files not matching apply-*.log are never touched.
func pruneApplyLogs(dir string, keep int, protect string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, applyLogPrefix) && strings.HasSuffix(n, applyLogSuffix) {
			names = append(names, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for i, n := range names {
		if i >= keep && n != protect {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}
