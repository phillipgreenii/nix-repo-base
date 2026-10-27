package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Build-as-user apply (bead pg2-sdbhh4.9, ADR 0038). When [workspace].
// activate_command is set and the preflight passes, apply runs the SAME build
// step as `pn workspace build` as the invoking user (one evaluation, the user's
// caches, a wrapped run whose nix JSONL lands in the user log directory), reads
// and validates the system out path, probes the built system, and only then runs
// two root steps through sudo: set the system profile, then activate. Root never
// evaluates. With activate_command unset, none of this code runs and apply is the
// single apply_command flow it always was.
//
// Command sequence (every step is a Runner call, so fake runners pin it):
//
//	0 preflight        no process: activate_command set, nix-env usable, build_command expands
//	                   (the run dir is created first thing in the run, still before any build;
//	                   if that fails the legacy flow runs instead)
//	1 build            user, Dir = private run dir R, WrapNix true: the build_command expansion
//	2 read-result      os.Readlink(R/result) ONCE, then validateBuildOut; no process
//	3 probe            user, Dir = R, unwrapped, 10 s: <out>/sw/bin/darwin-rebuild --help
//	4 set-profile      sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=... nix-env -p <profile> --set <out>
//	5 activate         sudo <out>/sw/bin/darwin-rebuild activate (the activate_command expansion)
//
// The set-profile step is hard-coded here, never templated: it is root-executed
// and security-sensitive, and the drift guard and the fake-runner tests pin it.

const (
	// nixEnvPath is the nix-env the set-profile step runs, by its nix-env symlink
	// name: nix-env is a multi-call binary that dispatches on argv[0], so the
	// resolved path (.../bin/nix) fails with "unrecognised flag '-p'".
	nixEnvPath = "/run/current-system/sw/bin/nix-env"
	// rootHome is root's home on macOS. It is set explicitly because macOS sudo
	// preserves the caller's HOME, which would make root write .nix-defexpr and
	// .local/state/nix into the invoking user's home.
	rootHome = "/var/root"
	// rootPath is the whole PATH of the set-profile step.
	rootPath = "/run/current-system/sw/bin:/usr/bin:/bin"
	// nixStoreDir is the store the out path must be a direct child of.
	nixStoreDir = "/nix/store"
	// staleRunDirAge is how old an abandoned per-run directory must be before the
	// next apply prunes it (a pn killed with SIGKILL cannot clean up after itself).
	staleRunDirAge = 24 * time.Hour
	// runDirPrefix names a per-run directory under apply-build.
	runDirPrefix = "run-"
)

// probeTimeout bounds the version probe. A package variable so a test can shorten it.
var probeTimeout = 10 * time.Second

// Failed-step values of the apply-log trailer in the build-as-user flow.
const (
	failedStepBuild      = "build"
	failedStepReadResult = "read-result"
	failedStepProbe      = "probe"
	failedStepSetProfile = "set-profile"
	failedStepActivate   = "activate"
	failedStepNone       = "none"
)

// asUserSeams are the filesystem facts the preflight and the validation consult,
// injectable so tests need no /run/current-system and no real /nix/store. nil in
// production (the defaults apply).
type asUserSeams struct {
	// nixEnvUsable reports whether the set-profile step's nix-env can run.
	nixEnvUsable func() bool
	// storeDir is the store directory an out path must sit directly under.
	storeDir string
}

// defaultAsUserSeams is the process-wide seam set. It is nil in every shipped
// binary; only the smoke harness's pn build (build tag smokeseams,
// asuser_smokeseams.go) sets it, so a smoke scenario can run the flow against a
// fake store directory without any production knob for a root-executed path.
var defaultAsUserSeams *asUserSeams

// seams returns the effective seam set, or nil for production behaviour.
func (ws *Workspace) seams() *asUserSeams {
	if ws.asUserSeams != nil {
		return ws.asUserSeams
	}
	return defaultAsUserSeams
}

func (ws *Workspace) nixEnvUsable() bool {
	if sm := ws.seams(); sm != nil && sm.nixEnvUsable != nil {
		return sm.nixEnvUsable()
	}
	fi, err := os.Stat(nixEnvPath)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func (ws *Workspace) storeDir() string {
	if sm := ws.seams(); sm != nil && sm.storeDir != "" {
		return sm.storeDir
	}
	return nixStoreDir
}

// asUserPlan is what the preflight decided: apply runs the build-as-user flow.
type asUserPlan struct {
	// activateTmpl is the activate_command template; its {out} is substituted
	// only from the validated out path.
	activateTmpl string
	// fallbackArgs is the apply_command expansion run when the built system
	// cannot be activated this way; nil when apply_command is unset (or could not
	// be expanded, see fallbackErr).
	fallbackArgs []string
	fallbackErr  error
}

// applyBuildDir is the directory holding the per-run directories:
// ${XDG_STATE_HOME:-$HOME/.local/state}/pn/apply-build.
func applyBuildDir() string {
	return filepath.Join(filepath.Dir(eventlog.DefaultPath()), "apply-build")
}

// extendPlan selects the build-as-user flow for apply (planExtender). It leaves
// the plan exactly as the prelude resolved it, so the legacy flow is untouched,
// when activate_command is unset or the caller passed an explicit apply command.
// Otherwise it runs the preflight (no side effects: it runs before the trust
// gate); when that fails it says so in one line and,
// again leaving the plan untouched, lets the legacy apply_command flow run
// BEFORE anything is built.
func (applyVerb) extendPlan(_ context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) error {
	activateTmpl := ws.config.ActivateCommandTemplate()
	if activateTmpl == "" || p.opts.Cmd != "" {
		return nil
	}

	// The prelude expanded apply_command when it is set (the fallback) and
	// build_command when it is not (commandTemplate).
	asUser := &asUserPlan{activateTmpl: activateTmpl}
	if _, err := ws.config.ApplyCommandTemplate(); err != nil {
		asUser.fallbackErr = err
	} else {
		asUser.fallbackArgs = p.cmdArgs
	}

	reason := ""
	var buildArgs []string
	switch {
	case !ws.nixEnvUsable():
		reason = nixEnvPath + " is missing or not executable"
	default:
		var err error
		buildArgs, err = substituteCommand(ws.config.BuildCommandTemplate(), p.vars)
		if err != nil {
			reason = "build_command: " + err.Error()
		}
	}
	if reason != "" {
		if asUser.fallbackArgs == nil {
			return fmt.Errorf("apply: the build-as-user flow (activate_command) is unavailable (%s) and there is no apply_command to fall back to: %w", reason, asUser.fallbackErr)
		}
		fmt.Fprintf(out, "apply: build-as-user flow unavailable (%s); running apply_command instead: %s\n", reason, strings.Join(asUser.fallbackArgs, " "))
		return nil
	}

	p.asUser = asUser
	p.cmdArgs = buildArgs
	return nil
}

// dryRunLines are the extra lines of a --show-nix-commands dry run in the
// build-as-user flow: the build itself is the command line above them.
func (applyVerb) dryRunLines(p *rebuildPlan) []string {
	if p.asUser == nil {
		return nil
	}
	var lines []string
	for _, l := range p.asUser.describeSteps(p) {
		lines = append(lines, "# "+l)
	}
	return lines
}

// describeSteps renders the steps that follow the build, with <out> standing
// for the out path the build will produce.
func (a *asUserPlan) describeSteps(p *rebuildPlan) []string {
	lines := []string{
		"set-profile command: " + strings.Join(setProfileArgv("<out>"), " "),
	}
	if act, err := substituteActivateCommand(a.activateTmpl, withOut(p.vars, "<out>")); err == nil {
		lines = append(lines, "activate command: "+strings.Join(act, " "))
	} else {
		lines = append(lines, "activate command: (invalid: "+err.Error()+")")
	}
	if a.fallbackArgs != nil {
		lines = append(lines, "fallback command (if the built system cannot be activated this way): "+strings.Join(a.fallbackArgs, " "))
	}
	return lines
}

func withOut(v templateVars, out string) templateVars {
	v.Out = out
	return v
}

// setProfileArgv is the argv of the set-profile step, sudo included. It is
// hard-coded: see the package comment.
func setProfileArgv(out string) []string {
	return []string{
		"sudo", "env", "-i",
		"HOME=" + rootHome,
		"NIX_REMOTE=daemon",
		"PATH=" + rootPath,
		nixEnvPath, "-p", systemProfileLink, "--set", out,
	}
}

// outPathRe is built per store directory: exactly one path element under it,
// with a 32-character nix base32 hash (no e, o, u or t) and a name.
func outPathRe(storeDir string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(storeDir) + `/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`)
}

// validateBuildOut checks the out path BEFORE any root process is started. It
// validates the INVOKED path, never its realpath: <out>/sw/bin/darwin-rebuild
// is a symlink into a separate store package, so a "realpath stays under out"
// check would always fail. The string itself is what the root steps receive,
// as an argv element, never in the environment.
func validateBuildOut(out, storeDir string) error {
	if !filepath.IsAbs(out) {
		return fmt.Errorf("out path %q is not absolute", out)
	}
	if filepath.Clean(out) != out {
		return fmt.Errorf("out path %q is not in clean form", out)
	}
	if !outPathRe(storeDir).MatchString(out) {
		return fmt.Errorf("out path %q is not a single store path directly under %s", out, storeDir)
	}
	fi, err := os.Lstat(out)
	if err != nil {
		return fmt.Errorf("out path: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("out path %q is not a directory (it is %s)", out, fi.Mode().Type())
	}
	activate := filepath.Join(out, "activate")
	fi, err = os.Lstat(activate)
	if err != nil {
		return fmt.Errorf("built system has no activate script: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable regular file (mode %s)", activate, fi.Mode())
	}
	rebuild := filepath.Join(out, "sw", "bin", "darwin-rebuild")
	fi, err = os.Stat(rebuild) // follows symlinks: the invoked path must resolve to an executable file
	if err != nil {
		return fmt.Errorf("built system has no sw/bin/darwin-rebuild: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s does not resolve to an executable regular file (mode %s)", rebuild, fi.Mode())
	}
	return nil
}

// readBuildResult reads the build's out path from <runDir>/result, one symlink
// level and exactly once, and validates it. The returned string is the only
// thing every later step uses; the link is never read again.
func readBuildResult(runDir, storeDir string) (string, error) {
	link := filepath.Join(runDir, "result")
	fi, err := os.Lstat(link)
	if err != nil {
		return "", fmt.Errorf("the build left no ./result in its working directory (is build_command a result-leaving `darwin-rebuild build`?): %w", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%s is not a symlink (is build_command a result-leaving `darwin-rebuild build`?)", link)
	}
	out, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", link, err)
	}
	if err := validateBuildOut(out, storeDir); err != nil {
		return "", err
	}
	return out, nil
}

// parseDarwinRebuildUsage decides whether a probe run of
// `<out>/sw/bin/darwin-rebuild --help` shows a script that can `activate`.
// darwin-rebuild's showSyntax writes its usage to STDERR and exits 1 (so exit 1
// is the ACCEPTED outcome); anything else is a probe failure: another exit
// code, output on stdout, no usage line, or an action list without activate,
// switch and build. A nil error means usable.
func parseDarwinRebuildUsage(exitCode int, stdout, stderr []byte) error {
	if exitCode != 1 {
		return fmt.Errorf("--help exited %d, want 1", exitCode)
	}
	if len(stdout) != 0 {
		return errors.New("--help wrote to stdout, want usage on stderr only")
	}
	first, _, _ := strings.Cut(string(stderr), "\n")
	if !strings.HasPrefix(first, "darwin-rebuild") {
		return fmt.Errorf("first usage line %q does not start with darwin-rebuild", first)
	}
	open := strings.Index(first, "{")
	closeIdx := strings.Index(first, "}")
	if open < 0 || closeIdx < open {
		return fmt.Errorf("first usage line %q has no {action | ...} group", first)
	}
	actions := map[string]bool{}
	for _, a := range strings.Split(first[open+1:closeIdx], "|") {
		actions[strings.TrimSpace(a)] = true
	}
	for _, need := range []string{"activate", "switch", "build"} {
		if !actions[need] {
			return fmt.Errorf("usage line %q lists no %q action", first, need)
		}
	}
	return nil
}

// probeActivate runs the version probe and returns "" when the built system can
// be activated through its own darwin-rebuild, else the reason it cannot.
func (ws *Workspace) probeActivate(ctx context.Context, runDir, out string) (res exec.Result, reason string) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res, err := ws.runner.Run(pctx, filepath.Join(out, "sw", "bin", "darwin-rebuild"), []string{"--help"}, exec.RunOptions{Dir: runDir})
	if err != nil {
		// A non-zero exit is the NORMAL outcome (exit 1); only a failure to run
		// at all (not found, not executable, timeout) is a probe failure by itself.
		var ce *exec.CommandError
		if !errors.As(err, &ce) {
			return res, "the probe could not run: " + err.Error()
		}
		if pctx.Err() != nil {
			return res, fmt.Sprintf("the probe did not finish within %s", probeTimeout)
		}
	}
	if err := parseDarwinRebuildUsage(res.ExitCode, res.Stdout, res.Stderr); err != nil {
		return res, err.Error()
	}
	return res, ""
}

// makeRunDir creates this run's private directory R (mode 0700, owned by the
// invoking user) and prunes abandoned ones. The build runs with R as its working
// directory: `darwin-rebuild build` prints no out path and accepts no
// --out-link, and nix writes ./result into the CURRENT directory, so with cwd = R
// the link is private to this run. Nobody else knows R, so no other pn run and
// no agent running darwin-rebuild by hand can rewrite R/result, which is the pin.
func makeRunDir() (string, error) {
	parent := applyBuildDir()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	pruneStaleRunDirs(parent, staleRunDirAge, time.Now())
	return os.MkdirTemp(parent, runDirPrefix)
}

// pruneStaleRunDirs removes run-* directories under parent last modified more
// than maxAge before now: what a pn killed by SIGKILL left behind. Best-effort,
// and nothing that is not a run-* directory is touched.
func pruneStaleRunDirs(parent string, maxAge time.Duration, now time.Time) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), runDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, e.Name()))
	}
}

// applyAsUser runs the build-as-user flow (steps 1 to 5 above) and returns the
// apply's error, already wrapped with the step that failed. It records the
// failed step and its exit status in the apply log.
func (ws *Workspace) applyAsUser(ctx context.Context, out io.Writer, alog *applyLog, p *rebuildPlan) error {
	alog.useSteps()

	runDir, err := makeRunDir()
	if err != nil {
		// Still before anything is built, so the legacy flow is a clean fallback.
		return ws.applyFallback(ctx, out, alog, p, "cannot create the per-run build directory: "+err.Error(), failedStepBuild, nil)
	}
	defer func() { _ = os.RemoveAll(runDir) }()

	// 1. build, as the user, in R. Its output is tee'd into the apply log.
	if err := ws.runPlannedIn(ctx, out, "apply", p, runDir, true); err != nil {
		alog.fail(failedStepBuild, err)
		return fmt.Errorf("apply failed: build: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return cancelledBeforeRoot(err)
	}

	// 2. read the out path once and validate it. The root's build key from the
	// single-flight lock is still held here (it is released when rebuild returns).
	ws.recordStep("apply", false, stepReadResult)
	built, err := readBuildResult(runDir, ws.storeDir())
	if err != nil {
		alog.fail(failedStepReadResult, nil)
		return fmt.Errorf("apply failed: read build result: %w", err)
	}
	fmt.Fprintf(out, "apply: built %s\n", built)

	// 3. probe the built system's own darwin-rebuild.
	ws.recordStep("apply", false, stepProbe)
	if res, reason := ws.probeActivate(ctx, runDir, built); reason != "" {
		if err := ctx.Err(); err != nil {
			return cancelledBeforeRoot(err)
		}
		return ws.applyFallback(ctx, out, alog, p, reason, failedStepProbe, &res)
	}

	activate, err := substituteActivateCommand(p.asUser.activateTmpl, withOut(p.vars, built))
	if err != nil {
		alog.fail(failedStepActivate, nil)
		return fmt.Errorf("apply failed: activate_command: %w", err)
	}

	// Between the build and the first root step a cancel aborts: nothing root has run.
	if err := ctx.Err(); err != nil {
		return cancelledBeforeRoot(err)
	}

	// 4 and 5, as root. Once set-profile starts, a pn-initiated cancel lets both
	// steps run to completion (profile set but not activated is the worst
	// half-state, and an activation interrupted halfway is worse than a finished
	// one); the cancel is reported afterwards. A terminal Ctrl-C still reaches the
	// root steps through the process group, as it does with `switch`. These steps
	// stay in pn's own group (OwnProcessGroup is never set): sudo reads the
	// password from the terminal, which a background group cannot (SIGTTIN).
	fmt.Fprintln(out, "apply: waiting for sudo (set the system profile, then activate)")
	rootCtx := context.WithoutCancel(ctx)

	ws.recordStep("apply", false, stepSetProfile)
	sp := setProfileArgv(built)
	if _, err := ws.runner.Run(rootCtx, sp[0], sp[1:], rootStepOptions(out)); err != nil {
		alog.fail(failedStepSetProfile, err)
		return fmt.Errorf("apply failed: set system profile: %w", err)
	}

	ws.recordStep("apply", false, stepActivate)
	if _, err := ws.runner.Run(rootCtx, activate[0], activate[1:], rootStepOptions(out)); err != nil {
		alog.fail(failedStepActivate, err)
		return fmt.Errorf("apply failed: activate: %w", err)
	}
	alog.setExit(nil)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apply cancelled, but set-profile and activate had already started and ran to completion (%s is the running system): %w", built, err)
	}
	return nil
}

// rootStepOptions are the RunOptions of a root step: not wrapped (they run no
// nix evaluation), output tee'd to the terminal and the apply log, cwd "/".
func rootStepOptions(out io.Writer) exec.RunOptions {
	return exec.RunOptions{Dir: "/", Stdout: out, Stderr: out}
}

func cancelledBeforeRoot(err error) error {
	return fmt.Errorf("apply cancelled before any root step started: %w", err)
}

// applyFallback runs the legacy apply_command flow instead of the build-as-user
// flow, announcing why. failedStep is what the trailer records if the apply has
// no apply_command to fall back to. The fallback re-evaluates as root, exactly
// as apply always did; it does not record the build plan a second time.
func (ws *Workspace) applyFallback(ctx context.Context, out io.Writer, alog *applyLog, p *rebuildPlan, reason, failedStep string, probe *exec.Result) error {
	what := "the built system has no usable 'darwin-rebuild activate'"
	if failedStep != failedStepProbe {
		what = "the build-as-user flow cannot start"
	}
	if p.asUser.fallbackArgs == nil {
		if probe != nil && probe.Signal == "" {
			alog.failExit(failedStep, probe.ExitCode)
		} else {
			alog.fail(failedStep, nil)
		}
		return fmt.Errorf("apply failed: %s: %s (%s) and there is no apply_command to fall back to (%v)", failedStep, what, reason, p.asUser.fallbackErr)
	}
	fmt.Fprintf(out, "apply: %s (%s); falling back to: %s (root re-evaluates)\n",
		what, reason, strings.Join(p.asUser.fallbackArgs, " "))
	fp := *p
	fp.cmdArgs = p.asUser.fallbackArgs
	fp.asUser = nil
	if err := ws.runPlannedIn(ctx, out, "apply", &fp, p.terminalRepoDir, false); err != nil {
		alog.fail(failedStepActivate, err)
		return fmt.Errorf("apply failed: %w", err)
	}
	alog.setExit(nil)
	return nil
}
