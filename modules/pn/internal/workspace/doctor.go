// internal/workspace/doctor.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

type Severity int

const (
	SevWarning Severity = iota
	SevError
)

func (s Severity) String() string {
	if s == SevError {
		return "ERROR"
	}
	return "WARN"
}

// Finding is one issue (or skipped check) the doctor reports. fix is non-nil
// only when the finding is safely auto-fixable.
type Finding struct {
	CheckID  string
	Repo     string // "" for workspace-level findings
	Severity Severity
	Message  string
	Manual   string // copy-pasteable command for non-auto-fixable findings
	Fixable  bool
	Skipped  bool
	Applied  bool
	fix      func(ctx context.Context) error
}

type DoctorReport struct {
	Mode     string
	Findings []Finding
	Skipped  []string // check IDs skipped (e.g. --offline)
	Plan     []string // populated on --dry-run: what would be fixed
}

func (r *DoctorReport) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SevError && !f.Skipped {
			return true
		}
	}
	return false
}

func (r *DoctorReport) hasAny() bool {
	for _, f := range r.Findings {
		if !f.Skipped {
			return true
		}
	}
	return false
}

// ExitCode maps the report to 0 (clean), 1 (errors, or any finding under strict).
// Code 2 (doctor itself failed) is returned by Doctor's error path, not here.
func (r *DoctorReport) ExitCode(strict bool) int {
	if r.HasErrors() {
		return 1
	}
	if strict && r.hasAny() {
		return 1
	}
	return 0
}

type DoctorOptions struct {
	Fix      bool
	DryRun   bool
	Offline  bool
	JSON     bool
	Strict   bool
	Terminal string
}

// doctorEnv is the shared context passed to every check.
type doctorEnv struct {
	ws       *Workspace
	mode     string // "primary" | "worktree"
	terminal string // resolved terminal repo key ("" if none)
	offline  bool
	refRev   map[string]string
	skipped  map[string]bool
	lock     *Lock // effective lock (derived if the disk lock is stale)
}

type check struct {
	id  string
	run func(ctx context.Context, env *doctorEnv) []Finding
}

// runChecks executes each check and concatenates findings, in registry order.
func runChecks(ctx context.Context, env *doctorEnv, checks []check) []Finding {
	var out []Finding
	for _, c := range checks {
		out = append(out, c.run(ctx, env)...)
	}
	return out
}

// Doctor audits the workspace rooted at root. Phase 1 reads toml/lock raw so a
// malformed file is diagnosable before Open() would fail; Phase 2 opens the
// workspace, runs the check registry, optionally applies fixes, and returns the
// report. The returned error is non-nil only when the doctor itself cannot run
// (mapped to exit 2 by the CLI).
func Doctor(ctx context.Context, root string, runner exec.Runner, opts DoctorOptions) (*DoctorReport, error) {
	report := &DoctorReport{}

	// --- Phase 1: structural, raw reads (no Open) ---
	tomlPath := filepath.Join(root, ConfigFileName)
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			CheckID: "toml-present", Severity: SevError,
			Message: fmt.Sprintf("%s missing or unreadable: %v", ConfigFileName, err),
		})
		return report, nil // cannot proceed; report carries the error finding
	}
	if _, perr := ParseConfig(data); perr != nil {
		report.Findings = append(report.Findings, Finding{
			CheckID: "toml-valid", Severity: SevError,
			Message: fmt.Sprintf("%s invalid: %v", ConfigFileName, perr),
		})
		return report, nil
	}

	// --- Phase 2: open + run checks ---
	ws, err := Open(root, runner)
	if err != nil {
		// Open can still fail (e.g. malformed lock). Surface as a structural finding.
		report.Findings = append(report.Findings, structuralOpenFailure(err))
		return report, nil
	}
	defer ws.Close()

	mode := ws.workspaceMode(ctx)
	report.Mode = mode
	refRev, skipped := ws.resolveRefRevs(ctx, mode, opts.Offline)
	effLock, _, _ := ws.effectiveLock(ctx) // best-effort; nil-safe checks handle a bad lock
	env := &doctorEnv{
		ws:       ws,
		mode:     mode,
		terminal: ws.resolveTerminalForDoctor(opts.Terminal),
		offline:  opts.Offline,
		refRev:   refRev,
		skipped:  skipped,
		lock:     effLock,
	}

	checks := ws.registerChecks()
	report.Findings = append(report.Findings, runChecks(ctx, env, checks)...)
	report.Skipped = collectSkipped(report.Findings)
	sortFindings(report.Findings)

	if opts.Fix {
		applyFixes(ctx, env, report, opts) // defined in doctor_fix.go (Task 12)
	}
	return report, nil
}

// resolveTerminalForDoctor returns the effective terminal: the --terminal flag
// if set, else workspace.terminal (may be "").
func (ws *Workspace) resolveTerminalForDoctor(flag string) string {
	if flag != "" {
		return flag
	}
	return ws.config.Workspace.Terminal
}

func osStderr() *os.File { return os.Stderr }

func structuralOpenFailure(err error) Finding {
	return Finding{
		CheckID: "lock-valid", Severity: SevError,
		Message: fmt.Sprintf("workspace failed to open (likely a malformed lock): %v", err),
		Manual:  "regenerate the lock:  pn workspace lock",
	}
}

func collectSkipped(findings []Finding) []string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range findings {
		if f.Skipped && !seen[f.CheckID] {
			seen[f.CheckID] = true
			ids = append(ids, f.CheckID)
		}
	}
	sort.Strings(ids)
	return ids
}

// sortFindings orders errors before warnings, then by repo, then check id.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity > fs[j].Severity // SevError(1) before SevWarning(0)
		}
		if fs[i].Repo != fs[j].Repo {
			return fs[i].Repo < fs[j].Repo
		}
		return fs[i].CheckID < fs[j].CheckID
	})
}

// registerChecks returns the check registry. Checks are appended here as they
// are implemented (Tasks 7–11). Phase-1 structural toml checks already ran in
// Doctor(); lock-level structural checks (which need an opened workspace) live
// in the registry.
//
// If ws.registerChecksFn is non-nil (test override), that is called instead so
// tests can stub the re-run inside applyFixes.
func (ws *Workspace) registerChecks() []check {
	if ws.registerChecksFn != nil {
		return ws.registerChecksFn()
	}
	return []check{
		{id: "lock", run: ws.checkLock},
		{id: "repos", run: ws.checkRepos},
		{id: "branches", run: ws.checkBranches},
		{id: "terminal", run: ws.checkTerminal},
		{id: "flake-lock", run: ws.checkFlakeLockFresh},
	}
}
