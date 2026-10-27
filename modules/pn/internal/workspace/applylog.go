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

// Run log (the apply log and the build log): a retained, unprivileged-readable
// copy of everything a `pn workspace apply` that REBUILDS, or a `pn workspace
// build`, wrote to the operator's terminal (beads pg2-xdvbj, pg2-sdbhh4.14).
// Post-apply verification beads ask "does the apply output show X"; without a
// retained copy that can only be inferred, and a 29,037 s failed build once left
// no human-readable record at all. Both verbs go through the same code path
// (Workspace.rebuild), so both retain through this one mechanism; only the verb
// names differ.
//
// Location: ${XDG_STATE_HOME:-$HOME/.local/state}/pn/apply-logs/<verb>-<UTC
// yyyymmddThhmmssZ>-<pid>.log (<verb> is apply or build), plus a symlink to the
// newest log of each verb: `latest` (the newest APPLY log; pn-workspace-rules
// SKILL.md reads it) and `latest-build` (the newest build log). The directory is a
// subdirectory of pn's state dir on purpose: the Loki collector globs pn/*.jsonl,
// so these logs are never ingested, and `nix-logs` (the wrapper's raw nix JSON,
// root-only for a sudo apply) is untouched. The files are mode 0644 and
// world-readable: nothing sensitive (no substituter URLs, no secrets) may be
// written into the header.
//
// Retention: the newest applyLogKeep files PER VERB PREFIX (by name, which sorts
// by start time) are kept; older ones are removed whenever a new log is opened,
// so a burst of builds never evicts apply logs, nor the reverse. Only runs that
// actually rebuild create a log, so no-op applies never evict real ones.
//
// File layout (plain text; `key: value` lines are machine-readable). Format
// version 2 (a `log_format: 2` header line; logs written before it have no such
// line and are version 1: every key they have below keeps its meaning, the newer
// keys are simply absent, and a reader MUST treat a missing key as unknown):
//
//	--== pn <verb> log ==--
//	started_at: <RFC3339 UTC>
//	terminal: <repo key>
//	command: <argv>
//	log_format: 2                       (v2)
//	trace_id: <32 hex | none>           (v2; the pn.repo span's trace)
//	rebuild_reason: <forced|dirty|head_changed|no_record|unchanged|unknown>  (v2)
//	max_jobs: <value of /etc/nix/nix.conf max-jobs | unknown>  (v2)
//	cores: <value of /etc/nix/nix.conf cores | unknown>        (v2)
//	<further `key: value` lines of the build-as-user apply flow>
//	--== applied refs ==--           (eval-time HEAD of each repo, written just
//	<repo>: <sha>                     before the build; absent if capture failed
//	                                  and for the build log)
//	--== output ==--
//	<combined stdout+stderr of the run, exactly as the terminal got it>
//	--== pn <verb> end ==--
//	ended_at: <RFC3339 UTC>
//	exit_status: <the command's exit code | none>
//	failed_step: ...                    (build-as-user apply only)
//	result: success|failed
//	error: <only when failed>
//	prelude_seconds: <s>                (v2; time before the build command started)
//	build_seconds: <s>                  (v2; the nix build. A legacy single-command
//	                                     apply reports its whole command here)
//	activate_seconds: <s>               (v2; apply only: set-profile plus activate,
//	                                     including the wait for sudo)
//	post_steps_seconds: <s>             (v2; apply only: nvd diff, fsmonitor restart,
//	                                     markApplied)
//	plan: builds=<n> fetches=<m> [download_mib=<x>]   (v2; once the build ran)
//
// The phase lines and `plan:` exist only in the trailer, never in the output
// section (whose contract is verbatim terminal output), and a phase never entered
// has no line. Post-HOOKS are not a phase: they run after the trailer is written,
// and their time is visible only through the pn.hook spans.
//
// A file WITHOUT the `--== pn <verb> end ==--` trailer is a run that was killed
// before pn could finish (SIGKILL, power loss): it must be read as incomplete,
// never as success. exit_status is -1 when the command was killed by a signal
// (including an interrupted run), and `none` when the command never ran.
const (
	applyLogKeep   = 30
	applyLogPrefix = "apply-"
	buildLogPrefix = "build-"
	applyLogSuffix = ".log"

	applyLogLatest = "latest"
	buildLogLatest = "latest-build"

	// applyLogFormat is the `log_format:` value this code writes.
	applyLogFormat = 2

	applyLogHeader  = "--== pn apply log ==--"
	buildLogHeader  = "--== pn build log ==--"
	applyLogRefs    = "--== applied refs ==--"
	applyLogOutput  = "--== output ==--"
	applyLogTrailer = "--== pn apply end ==--"
	buildLogTrailer = "--== pn build end ==--"
)

// Phase names, in the order the trailer prints them.
const (
	phasePrelude   = "prelude"
	phaseBuild     = "build"
	phaseActivate  = "activate"
	phasePostSteps = "post_steps"
)

var phaseOrder = []string{phasePrelude, phaseBuild, phaseActivate, phasePostSteps}

// nixConfPath is the nix.conf read for the header's max-jobs and cores; a test
// seam. It is read as a plain file: never `nix config show`, never a subprocess.
var nixConfPath = "/etc/nix/nix.conf"

// applyLogNow is the clock; a test seam.
var applyLogNow = time.Now

// applyLogDir is the directory holding apply logs.
func applyLogDir() string {
	return filepath.Join(filepath.Dir(eventlog.DefaultPath()), "apply-logs")
}

// logKind is what differs between the apply log and the build log.
type logKind struct {
	verb, prefix, link, header, trailer string
}

var (
	applyLogKind = logKind{"apply", applyLogPrefix, applyLogLatest, applyLogHeader, applyLogTrailer}
	buildLogKind = logKind{"build", buildLogPrefix, buildLogLatest, buildLogHeader, buildLogTrailer}
)

// kindFor maps a verb name to its logKind; anything but "build" is apply.
func kindFor(verb string) logKind {
	if verb == "build" {
		return buildLogKind
	}
	return applyLogKind
}

// applyLog is an open run log (apply or build; the name is historical). A nil
// *applyLog is valid and a no-op, so a failure to retain the log can never fail
// or alter the run.
type applyLog struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	kind     logKind
	exit     string // exit_status line value; "none" until the command ran
	finished bool
	// steps is true for the build-as-user flow, whose trailer also records
	// failed_step. The legacy single-command flow never writes that line, so its
	// log stays exactly as it was.
	steps      bool
	failedStep string
	// Phase timing (trailer only): cur is the phase running since curStart;
	// elapsed sums every closed phase.
	cur      string
	curStart time.Time
	elapsed  map[string]time.Duration
	// plan is the build plan the run printed, once the build step ran.
	plan *planStats
}

// runLogOpts are the inputs of openRunLog.
type runLogOpts struct {
	Verb     string // "apply" or "build"
	Terminal string
	Cmdline  []string
	// Extra are further header lines (`key: value`) written after the common ones:
	// the later steps of a multi-step apply.
	Extra []string
	// Begin is when the verb started; the prelude phase is measured from it. The
	// zero value means "now".
	Begin time.Time
	// TraceID is the pn.repo span's trace id ("" when telemetry is off).
	TraceID string
	// Reason is the rebuild decision (forced, dirty, head_changed, no_record,
	// unchanged) or "" when it could not be determined.
	Reason string
}

// openApplyLog creates an apply log with no extra context. It is what the unit
// tests of the file format use; production goes through openRunLog.
func openApplyLog(terminal string, cmdline []string) (*applyLog, error) {
	return openRunLog(runLogOpts{Verb: "apply", Terminal: terminal, Cmdline: cmdline})
}

// openRunLog creates the log, writes its header, repoints the verb's latest link
// and prunes that verb's old logs. On any failure it returns (nil, err); the
// caller warns and carries on.
func openRunLog(o runLogOpts) (*applyLog, error) {
	kind := kindFor(o.Verb)
	dir := applyLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	start := applyLogNow().UTC()
	name := fmt.Sprintf("%s%s-%d%s", kind.prefix, start.Format("20060102T150405Z"), os.Getpid(), applyLogSuffix)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	begin := o.Begin
	if begin.IsZero() {
		begin = start
	}
	l := &applyLog{f: f, path: path, kind: kind, exit: "none", cur: phasePrelude, curStart: begin, elapsed: map[string]time.Duration{}}
	trace := o.TraceID
	if trace == "" {
		trace = "none"
	}
	reason := o.Reason
	if reason == "" {
		reason = "unknown"
	}
	maxJobs, cores := readNixConfJobs(nixConfPath)
	l.writef("%s\nstarted_at: %s\nterminal: %s\ncommand: %s\nlog_format: %d\ntrace_id: %s\nrebuild_reason: %s\nmax_jobs: %s\ncores: %s\n",
		kind.header, start.Format(time.RFC3339), o.Terminal, strings.Join(o.Cmdline, " "),
		applyLogFormat, trace, reason, maxJobs, cores)
	for _, line := range o.Extra {
		l.writef("%s\n", line)
	}
	repointLatest(dir, kind.link, name)
	pruneRunLogs(dir, kind.prefix, applyLogKeep, name)
	return l, nil
}

// readNixConfJobs returns the `max-jobs` and `cores` values of the nix.conf at
// path ("unknown" for a missing file or key). It reads that one file and nothing
// else, and only ever returns a bare token (digits, `auto`): the log is
// world-readable, so no other text from the file can leak into it. The last
// assignment wins, as in nix; `build-cores` is the legacy name of `cores`.
func readNixConfJobs(path string) (maxJobs, cores string) {
	maxJobs, cores = "unknown", "unknown"
	data, err := os.ReadFile(path)
	if err != nil {
		return maxJobs, cores
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !bareToken(val) {
			continue
		}
		switch key {
		case "max-jobs":
			maxJobs = val
		case "cores", "build-cores":
			cores = val
		}
	}
	return maxJobs, cores
}

// bareToken reports whether v is a non-empty run of letters and digits.
func bareToken(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
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

// phase ends the running phase and starts the named one (a no-op when it is
// already running). Nil-safe. Phase times go in the trailer only.
func (l *applyLog) phase(name string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished || l.cur == name {
		return
	}
	l.closePhaseLocked()
	l.cur = name
}

// closePhaseLocked adds the running phase's time to elapsed and restarts the clock.
func (l *applyLog) closePhaseLocked() {
	now := applyLogNow()
	if l.cur != "" {
		l.elapsed[l.cur] += now.Sub(l.curStart)
	}
	l.curStart = now
}

// recordPlan keeps the build plan for the trailer's `plan:` line. Nil-safe.
func (l *applyLog) recordPlan(st planStats) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.plan = &st
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
	l.closePhaseLocked()
	l.cur = ""
	result := "success"
	if runErr != nil {
		result = "failed"
	}
	var b strings.Builder
	// Start the trailer on its own line even if the output lacked a final newline.
	fmt.Fprintf(&b, "\n%s\nended_at: %s\nexit_status: %s\n",
		l.kind.trailer, applyLogNow().UTC().Format(time.RFC3339), l.exit)
	if l.steps {
		fmt.Fprintf(&b, "failed_step: %s\n", l.failedStep)
	}
	fmt.Fprintf(&b, "result: %s\n", result)
	if runErr != nil {
		fmt.Fprintf(&b, "error: %s\n", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
	for _, name := range phaseOrder {
		if d, ok := l.elapsed[name]; ok {
			fmt.Fprintf(&b, "%s_seconds: %.1f\n", name, d.Seconds())
		}
	}
	if l.plan != nil {
		fmt.Fprintf(&b, "plan: builds=%d fetches=%d", l.plan.Builds, l.plan.Fetches)
		if l.plan.HasDownload {
			fmt.Fprintf(&b, " download_mib=%.1f", l.plan.DownloadMiB)
		}
		b.WriteString("\n")
	}
	_, _ = l.f.WriteString(b.String())
	_ = l.f.Close()
	l.f = nil
}

// repointLatest atomically makes <dir>/<link> a symlink to name. Best-effort.
func repointLatest(dir, link, name string) {
	tmp := filepath.Join(dir, fmt.Sprintf(".%s-%d", link, os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, link)); err != nil {
		_ = os.Remove(tmp)
	}
}

// pruneRunLogs removes all but the newest `keep` logs of one verb (files named
// <prefix>*.log) in dir (newest = highest name; names embed a UTC start time).
// `protect` is never removed. Best-effort; files of another prefix, and anything
// else in dir, are never touched, so one verb cannot evict the other's logs.
func pruneRunLogs(dir, prefix string, keep int, protect string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, applyLogSuffix) {
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
