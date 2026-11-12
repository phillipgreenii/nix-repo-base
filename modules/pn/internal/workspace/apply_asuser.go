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

// Build-as-user apply (ADR 0038). This is the one apply flow: [workspace].
// activate_command is required. Apply runs the SAME build step as
// `pn workspace build` as the invoking user (one evaluation, the user's caches, a
// wrapped run whose nix JSONL lands in the user log directory), reads and
// validates the system out path, probes the built system, and only then runs two
// root steps through sudo: set the system profile, then activate. Root never
// evaluates. A failed preflight, run directory or probe is an error, never a
// fallback to a root build. The platform specific parts (the activate program
// checks, the probe, root's home) are an activationPlatform chosen from the
// activate_command program: darwin-rebuild (darwin) or switch-to-configuration
// (nixos).
//
// Command sequence (every step is a Runner call, so fake runners pin it):
//
//	0 preflight        no process: activate_command set and recognised, nix-env usable,
//	                   build_command expands (the run dir is created first thing in the
//	                   run, still before any build; if that fails apply fails)
//	1 sudo-auth        sudo -n -v, else sudo -v once; then sudo -n -v every minute while
//	                   the build runs; sudo -k right after activate (ADR 0042)
//	2 build            user, Dir = private run dir R, WrapNix true: the build_command expansion
//	3 read-result      os.Readlink(R/result) ONCE, then validateBuildOut; no process
//	4 probe            user, Dir = R, unwrapped, 10 s: darwin: <out>/sw/bin/darwin-rebuild --help;
//	                   nixos: <out>/bin/switch-to-configuration with no arguments (usage, exit 1)
//	5 set-profile      sudo env -i HOME=<root home> NIX_REMOTE=daemon PATH=... nix-env -p <profile> --set <out>
//	6 activate         darwin: sudo <out>/sw/bin/darwin-rebuild activate (the activate_command expansion);
//	                   nixos: that expansion rewritten by the platform to nixos-rebuild's argv:
//	                   sudo env -i PATH=... NIXOS_INSTALL_BOOTLOADER=0 [systemd-run ... --unit=...]
//	                   <out>/bin/switch-to-configuration switch (ADR 0043)
//
// The set-profile step is hard-coded here, never templated: it is root-executed
// and security-sensitive, and the drift guard and the fake-runner tests pin it.

const (
	// nixEnvPath is the nix-env the set-profile step runs, by its nix-env symlink
	// name: nix-env is a multi-call binary that dispatches on argv[0], so the
	// resolved path (.../bin/nix) fails with "unrecognised flag '-p'".
	// The path exists on darwin and on NixOS (checked on a NixOS host: a symlink to nix-env).
	nixEnvPath = "/run/current-system/sw/bin/nix-env"
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
	failedStepSudoAuth   = "sudo_auth"
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
	// systemdPresent reports whether the host runs systemd (the NixOS activation
	// then runs under systemd-run). nil means the production check. It is
	// consulted only for a platform that rewrites its activation (never darwin).
	systemdPresent func() bool
}

// systemdRunDir exists on a host booted with systemd; nixos-rebuild makes the
// same test before it uses systemd-run.
const systemdRunDir = "/run/systemd/system"

// productionSystemdPresent is the seam default. A package variable so the unit
// tests can replace it with a stub that fails the test when it is reached.
var productionSystemdPresent = func() bool { return systemdPresent(systemdRunDir) }

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

func (ws *Workspace) systemdPresent() bool {
	if sm := ws.seams(); sm != nil && sm.systemdPresent != nil {
		return sm.systemdPresent()
	}
	return productionSystemdPresent()
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
	// platform is the activationPlatform selected from activateTmpl.
	platform activationPlatform
	// systemd is whether the host runs systemd, decided once in the preflight
	// (only for a platform that wraps its activation), so the dry-run lines, the
	// apply-log header and the run all use the same argv.
	systemd bool
}

// activateArgv is the argv of the activate step, sudo included, for the out
// path out: the activate_command expansion, then the platform's rewrite (none
// for darwin, so its output is exactly substituteActivateCommand's).
func (a *asUserPlan) activateArgv(v templateVars, out string) ([]string, error) {
	argv, err := substituteActivateCommand(a.activateTmpl, withOut(v, out))
	if err != nil {
		return nil, err
	}
	if a.platform.wrapActivate != nil {
		argv = a.platform.wrapActivate(argv, a.systemd)
	}
	return argv, nil
}

// applyBuildDir is the directory holding the per-run directories:
// ${XDG_STATE_HOME:-$HOME/.local/state}/pn/apply-build.
func applyBuildDir() string {
	return filepath.Join(filepath.Dir(eventlog.DefaultPath()), "apply-build")
}

// errActivateCommandUnset is returned when apply has no activate_command: the
// build-as-user flow is the only apply flow.
var errActivateCommandUnset = errors.New("apply failed: workspace.activate_command is not set in pn-workspace.toml: apply builds as you and activates as root, so it needs the activation command (darwin: 'sudo {out}/sw/bin/darwin-rebuild activate'; nixos: 'sudo {out}/bin/switch-to-configuration switch'); a leftover apply_command is ignored (the old apply flow is removed)")

// extendPlan runs apply's preflight (planExtender): activate_command must be set
// and name a recognised program, nix-env must be usable and build_command must
// expand. The preflight has no side effects and runs before the trust gate; any
// failure fails the apply before anything is built.
func (applyVerb) extendPlan(_ context.Context, ws *Workspace, _ io.Writer, p *rebuildPlan) error {
	activateTmpl := ws.config.ActivateCommandTemplate()
	if activateTmpl == "" {
		return errActivateCommandUnset
	}
	platform, err := platformForActivate(activateTmpl)
	if err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	if !ws.nixEnvUsable() {
		return fmt.Errorf("apply failed: the build-as-user flow cannot start (%s is missing or not executable)", nixEnvPath)
	}
	// The prelude already expanded build_command (commandTemplate), so p.cmdArgs is
	// the user build.
	plan := &asUserPlan{activateTmpl: activateTmpl, platform: platform}
	if platform.wrapActivate != nil {
		plan.systemd = ws.systemdPresent()
	}
	p.asUser = plan
	return nil
}

// dryRunLines are the extra lines of a --show-nix-commands dry run in the
// build-as-user flow: the build itself is the command line above them.
func (applyVerb) dryRunLines(p *rebuildPlan) []string {
	lines := []string{"# sudo -v (before the build; kept fresh with sudo -n -v while it runs; released with sudo -k)"}
	for _, l := range p.asUser.describeSteps(p) {
		lines = append(lines, "# "+l)
	}
	return lines
}

// describeSteps renders the steps that follow the build, with <out> standing
// for the out path the build will produce.
func (a *asUserPlan) describeSteps(p *rebuildPlan) []string {
	lines := []string{
		"0 sudo-auth   sudo -v, then sudo -n -v every " + sudoRefreshInterval.String() + " while the build runs",
		"set-profile command: " + strings.Join(setProfileArgv("<out>", a.platform), " "),
	}
	if act, err := a.activateArgv(p.vars, "<out>"); err == nil {
		lines = append(lines, "activate command: "+strings.Join(act, " "))
	} else {
		lines = append(lines, "activate command: (invalid: "+err.Error()+")")
	}
	return lines
}

func withOut(v templateVars, out string) templateVars {
	v.Out = out
	return v
}

// setProfileArgv is the argv of the set-profile step, sudo included. It is
// hard-coded: see the package comment.
func setProfileArgv(out string, p activationPlatform) []string {
	return []string{
		"sudo", "env", "-i",
		"HOME=" + p.rootHome,
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
func validateBuildOut(out, storeDir string, p activationPlatform) error {
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
	return p.checkOut(out)
}

// readBuildResult reads the build's out path from <runDir>/result, one symlink
// level and exactly once, and validates it. The returned string is the only
// thing every later step uses; the link is never read again.
func readBuildResult(runDir, storeDir string, p activationPlatform) (string, error) {
	// Both `darwin-rebuild build` and `nixos-rebuild build` leave ./result in their
	// working directory (nixos-rebuild-ng sets no_link only for switch, boot, test
	// and dry-activate).
	link := filepath.Join(runDir, "result")
	fi, err := os.Lstat(link)
	if err != nil {
		return "", fmt.Errorf("the build left no ./result in its working directory (is build_command a result-leaving rebuild build, such as `darwin-rebuild build` or `nixos-rebuild build`?): %w", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%s is not a symlink (is build_command a result-leaving rebuild build, such as `darwin-rebuild build` or `nixos-rebuild build`?)", link)
	}
	out, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", link, err)
	}
	if err := validateBuildOut(out, storeDir, p); err != nil {
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
	return ws.probeUsage(ctx, runDir, filepath.Join(out, "sw", "bin", "darwin-rebuild"), []string{"--help"}, parseDarwinRebuildUsage)
}

// probeUsage runs prog with args as the user in runDir (unwrapped, bounded by
// probeTimeout) and hands the outcome to parse. "" means usable, else the reason.
func (ws *Workspace) probeUsage(ctx context.Context, runDir, prog string, args []string, parse func(exitCode int, stdout, stderr []byte) error) (res exec.Result, reason string) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res, err := ws.runner.Run(pctx, prog, args, exec.RunOptions{Dir: runDir})
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
	if err := parse(res.ExitCode, res.Stdout, res.Stderr); err != nil {
		return res, err.Error()
	}
	return res, ""
}

// detachedActivateNote is appended to an activate failure on the systemd path:
// the activation runs in a transient unit that pn does not own, so a failed or
// interrupted client does not prove the activation stopped. exitCode is the
// client's exit status (best effort: whether systemd-run --pipe forwards the
// unit's status through sudo is not verifiable without activating).
func (a *asUserPlan) detachedActivateNote(exitCode int) string {
	if !a.systemd {
		return ""
	}
	note := fmt.Sprintf(" (activation may still be running in unit %[1]s, or this one failed: systemctl status %[1]s; journalctl -u %[1]s)", nixosUnit)
	if exitCode == 4 {
		note += " (switch-to-configuration exit 4 means some units failed but the system HAS switched: run `systemctl --failed`, do not re-run apply)"
	}
	return note
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

// applyAsUser runs the build-as-user flow (steps 2 to 6 above) and returns the
// apply's error, already wrapped with the step that failed. It records the
// failed step and its exit status in the apply log.
func (ws *Workspace) applyAsUser(ctx context.Context, out io.Writer, alog *applyLog, p *rebuildPlan) error {
	alog.useSteps()

	runDir, err := makeRunDir()
	if err != nil {
		alog.fail(failedStepBuild, nil)
		return fmt.Errorf("apply failed: cannot create the per-run build directory: %w", err)
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
	built, err := readBuildResult(runDir, ws.storeDir(), p.asUser.platform)
	if err != nil {
		alog.fail(failedStepReadResult, nil)
		return fmt.Errorf("apply failed: read build result: %w", err)
	}
	fmt.Fprintf(out, "apply: built %s\n", built)

	// 3. probe the built system's own activation tool (the platform's probe).
	ws.recordStep("apply", false, stepProbe)
	if res, reason := p.asUser.platform.probe(ctx, ws, runDir, built); reason != "" {
		if err := ctx.Err(); err != nil {
			return cancelledBeforeRoot(err)
		}
		if res.Signal == "" && res.ExitCode != 0 {
			alog.failExit(failedStepProbe, res.ExitCode)
		} else {
			alog.fail(failedStepProbe, nil)
		}
		advice := ""
		if f := p.asUser.platform.probeAdvice; f != nil {
			advice = f(built)
		}
		return fmt.Errorf("apply failed: probe: the built system has no usable '%s' (%s)%s", p.asUser.platform.activateWhat, reason, advice)
	}

	activate, err := p.asUser.activateArgv(p.vars, built)
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
	alog.phase(phaseActivate)
	switch {
	case p.sudo.Lost():
		fmt.Fprintln(out, "apply: sudo authentication expired during the build; sudo will ask again now")
	case p.sudo.Cacheless():
		// The up-front warning already said sudo will ask again.
	default:
		fmt.Fprintln(out, "apply: running the root steps on the authenticated sudo session")
	}
	rootCtx := context.WithoutCancel(ctx)

	ws.recordStep("apply", false, stepSetProfile)
	sp := setProfileArgv(built, p.asUser.platform)
	if _, err := ws.runner.Run(rootCtx, sp[0], sp[1:], rootStepOptions(out)); err != nil {
		p.sudo.Stop()
		alog.fail(failedStepSetProfile, err)
		return fmt.Errorf("apply failed: set system profile: %w", err)
	}

	ws.recordStep("apply", false, stepActivate)
	res, err := ws.runner.Run(rootCtx, activate[0], activate[1:], rootStepOptions(out))
	// Release the credentials now, so they do not outlive the root steps
	// (nvd, the fsmonitor restart and markApplied follow).
	p.sudo.Stop()
	if err != nil {
		alog.fail(failedStepActivate, err)
		return fmt.Errorf("apply failed: activate: %w%s", err, p.asUser.detachedActivateNote(res.ExitCode))
	}
	alog.setExit(nil)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("apply cancelled, but set-profile and activate had already started and ran to completion (%s is the running system): %w", built, err)
	}
	return nil
}

// rootStepOptions are the RunOptions of a root step: not wrapped (they run no
// nix evaluation), output tee'd to the terminal and the apply log, cwd "/".
//
// No Env is ever passed: sudo's env_reset would drop it. The NixOS activation
// carries the variables it needs as `env -i` arguments in its argv instead
// (wrapNixOSActivate, ADR 0043), so the out path and every other root input stay
// out of the environment on every platform.
func rootStepOptions(out io.Writer) exec.RunOptions {
	return exec.RunOptions{Dir: "/", Stdout: out, Stderr: out}
}

func cancelledBeforeRoot(err error) error {
	return fmt.Errorf("apply cancelled before any root step started: %w", err)
}
