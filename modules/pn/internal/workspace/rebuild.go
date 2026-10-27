package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// The rebuild pipeline is a Template Method (bead pg2-sdbhh4.8): `pn workspace
// build` and `pn workspace apply` run ONE shared prelude (Workspace.rebuild) and
// differ only in the strategy (a rebuildVerb) that supplies the command template
// and everything after the prelude. Agents may not run apply, so build is the
// only way they can reproduce what apply would hit; routing both verbs through
// one prelude is what keeps that true as the prelude grows. A step added to the
// prelude therefore reaches both verbs by construction, and the parity contract
// test (rebuild_parity_test.go) fails if either verb stops running exactly the
// recorded prelude.

// Prelude step names, in the order Workspace.rebuild runs them. They are the
// vocabulary of the stepRecorder and of the parity contract test.
const (
	stepRequireTerminal    = "require_terminal"
	stepRepoSpan           = "repo_span"
	stepResolveOverrides   = "resolve_overrides"
	stepCheckFollows       = "check_follows"
	stepSubstituteTemplate = "substitute_template"
	stepDryRunGate         = "dry_run_gate"
	stepResolveTool        = "resolve_tool"
	stepTrustGate          = "trust_gate"
	stepAcquireLock        = "acquire_lock"
	stepCheckWsidUnique    = "check_wsid_unique"
	stepCheckNixDaemon     = "check_nix_daemon"
)

// Verb-specific step names (shared=false in the stepRecorder). They are recorded
// only so a test can pin what each verb does AFTER the prelude; they are never
// part of the parity contract.
const (
	stepReportApplyDecision = "report_apply_decision" // build: the read-only `apply would:` line
	stepSkipGate            = "skip_gate"             // apply: needsRebuild, may end the apply
	stepApplyLog            = "apply_log"
	stepCaptureHeads        = "capture_heads"
	stepRunCommand          = "run_command" // the verb's build/switch command
	stepPostSteps           = "post_steps"  // apply: nvd diff, fsmonitor restart
	stepMarkApplied         = "mark_applied"
)

// Steps of apply's build-as-user flow (pg2-sdbhh4.9, ADR 0038), recorded after
// stepRunCommand (which there is the user build). Verb-specific, never part of
// the parity contract.
const (
	stepReadResult = "read_result" // read and validate the build's ./result
	stepProbe      = "probe"       // <out>/sw/bin/darwin-rebuild --help
	stepSetProfile = "set_profile" // sudo ... nix-env -p <system profile> --set <out>
	stepActivate   = "activate"    // sudo <out>/sw/bin/darwin-rebuild activate
)

// stepRecorder observes the ordered steps a verb runs. shared is true for a step
// of the common prelude and false for a verb-specific step. It is nil in
// production and set only by tests (Workspace.stepRecorder).
type stepRecorder func(verb string, shared bool, step string)

// recordStep reports one step to the recorder, if any.
func (ws *Workspace) recordStep(verb string, shared bool, step string) {
	if ws.stepRecorder != nil {
		ws.stepRecorder(verb, shared, step)
	}
}

// hostname is the ONE hostname resolver both verbs substitute for {hostname}, so
// build and apply can never address different flake outputs. Tests stub it via
// Workspace.hostnameFn.
func (ws *Workspace) hostname() string {
	if ws.hostnameFn != nil {
		return ws.hostnameFn()
	}
	return shortHostname()
}

// EnableToolResolution turns on the rebuild prelude's tool resolution: when the
// verb's command (darwin-rebuild, nixos-rebuild, nix, or one of them after sudo)
// is not on PATH, the system-profile directories are appended to the child's
// PATH, and when it is found nowhere the verb fails before doing anything else
// (bead pg2-sdbhh4.12). Production (openWorkspaceRoot) calls it; it is off by
// default so tests with a fake runner need no real binary.
func (ws *Workspace) EnableToolResolution() {
	ws.resolveTool = func(name string) (map[string]string, error) {
		return exec.ResolveTool(name, exec.SystemProfileBinDirs)
	}
}

// resolveCommandTool resolves the bare command name of cmdArgs (the name after
// sudo, when sudo leads) and returns the Env overlay for running it. A missing
// tool is recorded as a pn.exec span (telemetry.RecordToolNotFound) and
// returned as an *exec.ToolNotFoundError.
func (ws *Workspace) resolveCommandTool(ctx context.Context, cmdArgs []string) (map[string]string, error) {
	if ws.resolveTool == nil || len(cmdArgs) == 0 {
		return nil, nil
	}
	tool := cmdArgs[0]
	if tool == "sudo" {
		if len(cmdArgs) < 2 || strings.HasPrefix(cmdArgs[1], "-") {
			return nil, nil
		}
		tool = cmdArgs[1]
	}
	env, err := ws.resolveTool(tool)
	if err != nil {
		telemetry.RecordToolNotFound(ctx, tool, exec.ToolNotFoundExitCode, err)
		return nil, err
	}
	return env, nil
}

// rebuildOptions is the option set common to both verbs. BuildOptions and
// ApplyOptions are projected onto it; Cmd is the per-invocation override of the
// verb's command template.
type rebuildOptions struct {
	Terminal            string
	Cmd                 string
	OverridePaths       map[string]string
	ShowNixCommandsOnly bool
	Force               bool // apply only: bypass the skip gate
	Builder             string
	// Wait is how long to wait for a contended single-flight lock (rebuildlock.go):
	// 0 fails fast, WaitForever waits indefinitely.
	Wait time.Duration
}

// rebuildPlan is everything the shared prelude resolves, handed to the verb's run.
type rebuildPlan struct {
	opts            rebuildOptions
	terminal        string
	terminalRepoDir string
	terminalNixDir  string
	// resolvedOverrides is the ONE override resolution; overrideArgs is its
	// `--override-input` projection. Resolving once is deliberate (bead
	// pg2-14yqh): a second resolution could sample a different filesystem.
	resolvedOverrides []resolvedOverride
	overrideArgs      []string
	// cmdArgs is the substituted command argv (before overrideArgs).
	cmdArgs []string
	// toolEnv is the RunOptions.Env overlay for the command's Run (the system
	// profile appended to PATH when PATH lacked the tool), or nil.
	toolEnv map[string]string
	// vars are the template variables the command was expanded with, kept so a
	// verb can expand a second template (apply's activate_command).
	vars templateVars
	// asUser is non-nil when apply runs the build-as-user flow (apply_asuser.go):
	// cmdArgs is then the user build, not apply_command.
	asUser *asUserPlan
	// begin is when the verb started (the run log's prelude phase starts here).
	begin time.Time
	// alog is the verb's retained run log once it is open (nil before, or when it
	// could not be created); runPlannedIn records the build plan into it.
	alog *applyLog
}

// fullArgs is the command's arguments after the program name, with the override
// flags appended.
func (p *rebuildPlan) fullArgs() []string {
	return append(append([]string{}, p.cmdArgs[1:]...), p.overrideArgs...)
}

// rebuildVerb is the strategy a verb plugs into the Template Method.
type rebuildVerb interface {
	// name is the verb ("build" or "apply"), used by the step recorder.
	name() string
	// commandTemplate returns the verb's command template, honoring the
	// per-invocation override in opts.Cmd.
	commandTemplate(ws *Workspace, opts rebuildOptions) (string, error)
	// run executes everything after the shared prelude.
	run(ctx context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) error
}

// planExtender is an optional capability of a verb: it may adjust the plan the
// shared prelude resolved, right after template substitution and before the
// dry-run gate (apply uses it to select the build-as-user flow; build does not
// implement it).
type planExtender interface {
	extendPlan(ctx context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) error
}

// dryRunExtender is an optional capability of a verb: extra lines printed after
// the command in a --show-nix-commands dry run.
type dryRunExtender interface {
	dryRunLines(p *rebuildPlan) []string
}

// runPlanned runs the verb's nix command with its output tapped for the build
// plan (plan.go), then records the plan in OTel (RecordPlan, once per run that
// reached the build step, 0 builds when nix printed no plan). The tap passes
// the child's bytes through unchanged and nothing here stops or signals the
// child. A cancel does reach the child, and (for a user-run command) the whole
// process group it leads, see ownGroup below. out is what the operator sees (for apply: the terminal tee'd into the
// apply log), so the optional warning reaches both.
func (ws *Workspace) runPlanned(ctx context.Context, out io.Writer, verb string, p *rebuildPlan) error {
	return ws.runPlannedIn(ctx, out, verb, p, p.terminalRepoDir, true)
}

// runPlannedIn is runPlanned with an explicit working directory (apply's
// build-as-user flow runs the build in its private per-run directory) and an
// option to skip RecordPlan (the legacy fallback run after a user build, which
// already recorded the plan once).
func (ws *Workspace) runPlannedIn(ctx context.Context, out io.Writer, verb string, p *rebuildPlan, dir string, recordPlan bool) error {
	tap := newPlanTap(out, ws.config.WarnBuildsThreshold())
	// ownGroup: a user-run command (build, and apply's build-as-user build; NOT
	// apply's `sudo darwin-rebuild switch` legacy/fallback command, and never the
	// two root steps of the build-as-user flow, pg2-sdbhh4.9) runs as its own
	// process group so a pn-initiated stop reaches every descendant, e.g.
	// darwin-rebuild's `nix build | jq` (pg2-sdbhh4.20). A sudo child stays in
	// pn's group: sudo reads the password from the terminal, which a background
	// group cannot (SIGTTIN), and a non-root pn cannot signal root children anyway.
	ownGroup := p.cmdArgs[0] != "sudo"
	_, err := ws.runner.Run(ctx, p.cmdArgs[0], p.fullArgs(), exec.RunOptions{
		Dir:             dir,
		Stdout:          tap,
		Stderr:          tap,
		WrapNix:         true,
		Env:             p.toolEnv,
		OwnProcessGroup: ownGroup,
	})
	tap.finish()
	if recordPlan {
		st := tap.stats()
		p.alog.recordPlan(st)
		telemetry.RecordPlan(ctx, verb, telemetry.Plan{
			Builds:      st.Builds,
			Fetches:     st.Fetches,
			DownloadMiB: st.DownloadMiB,
			HasDownload: st.HasDownload,
		})
	}
	return err
}

// rebuild is the Template Method: the shared prelude, then the verb's run.
//
// Prelude order (identical for both verbs): requireTerminal, pn.repo span,
// resolve overrides once, checkFollows, template substitution (shared hostname
// resolver), the dry-run exit, tool resolution, the trust gate, the single-flight
// lock (rebuildlock.go), checkWsidUnique, checkNixDaemon.
func (ws *Workspace) rebuild(ctx context.Context, out io.Writer, v rebuildVerb, opts rebuildOptions) (err error) {
	verb := v.name()
	begin := applyLogNow()
	step := func(s string) { ws.recordStep(verb, true, s) }

	step(stepRequireTerminal)
	terminal, err := ws.requireTerminal(ctx, opts.Terminal)
	if err != nil {
		return err
	}

	// pn.repo span for the terminal repo this verb builds/applies; ended with the
	// verb's final error.
	step(stepRepoSpan)
	ctx, endRepo := telemetry.StartRepo(ctx, terminal)
	defer func() { endRepo(err) }()
	terminalRepoDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalRepoDir = td
	}
	nixRel := filepath.Dir(ws.resolveFlakePath(terminal))
	terminalNixDir := filepath.Join(terminalRepoDir, nixRel)

	// ONE resolution, TWO projections: the flags nix is given and (apply) the
	// record of what this apply overrode (markApplied's `overridden`).
	step(stepResolveOverrides)
	resolvedOverrides := ws.resolveOverridesFor(terminal, overrideOpts{OverridePaths: opts.OverridePaths})

	step(stepCheckFollows)
	if err := checkFollows(terminalNixDir, ws.workspaceInputNamesFromEdges(terminal)); err != nil {
		return err
	}

	step(stepSubstituteTemplate)
	tmpl, err := v.commandTemplate(ws, opts)
	if err != nil {
		return err
	}
	builder := opts.Builder
	if builder == "" {
		builder = defaultBuilder()
	}
	vars := templateVars{
		TerminalRepoDir:    terminalRepoDir,
		TerminalNixDir:     terminalNixDir,
		TerminalNixRelPath: nixRel,
		Hostname:           ws.hostname(),
		Builder:            builder,
	}
	cmdArgs, err := substituteCommand(tmpl, vars)
	if err != nil {
		return err
	}
	plan := &rebuildPlan{
		vars:              vars,
		opts:              opts,
		terminal:          terminal,
		terminalRepoDir:   terminalRepoDir,
		terminalNixDir:    terminalNixDir,
		resolvedOverrides: resolvedOverrides,
		overrideArgs:      overrideInputArgs(resolvedOverrides),
		cmdArgs:           cmdArgs,
		begin:             begin,
	}

	// A verb may adjust the plan (apply: select the build-as-user flow, which
	// swaps cmdArgs for the user build). Everything after this point (dry run,
	// tool resolution) therefore sees the command that will actually run first.
	if ex, ok := v.(planExtender); ok {
		if err := ex.extendPlan(ctx, ws, out, plan); err != nil {
			return err
		}
	}

	telemetry.RecordRebuildContext(ctx, telemetry.RebuildContext{
		Terminal:      terminal,
		Hostname:      ws.hostname(),
		Builder:       builder,
		OverrideCount: len(resolvedOverrides),
		Force:         opts.Force,
	})

	step(stepDryRunGate)
	if opts.ShowNixCommandsOnly {
		fmt.Fprintln(out, strings.Join(append(append([]string{}, plan.cmdArgs...), plan.overrideArgs...), " "))
		if ex, ok := v.(dryRunExtender); ok {
			for _, l := range ex.dryRunLines(plan) {
				fmt.Fprintln(out, l)
			}
		}
		return nil
	}

	// Resolve the command's tool BEFORE the trust gate and every other probe, so a
	// missing darwin-rebuild fails at once with nothing else done (bead
	// pg2-sdbhh4.12).
	step(stepResolveTool)
	toolEnv, err := ws.resolveCommandTool(ctx, plan.cmdArgs)
	if err != nil {
		return err
	}
	plan.toolEnv = toolEnv

	// Trust gate (bead pg2-x2q6o): abort before ANY execution (including the nix
	// daemon check and the verb's probes) when the workspace root is untrusted, so
	// the config-sourced command is never run from an untrusted checkout.
	step(stepTrustGate)
	if err := ws.ensureExecTrusted(); err != nil {
		return err
	}

	// Single-flight lock (bead pg2-sdbhh4.11): after the trust gate, before any
	// probe or build, so a contended run fails (or waits) before doing work. It is
	// held until this function returns, i.e. through the verb's whole run and never
	// through the post-hooks, which the CLI layer runs after Build/Apply returns.
	step(stepAcquireLock)
	releaseLock, err := ws.acquireRebuildLock(ctx, out, verb, terminal, opts.Wait)
	if err != nil {
		return err
	}
	defer releaseLock()

	step(stepCheckWsidUnique)
	if id := ws.config.Workspace.Id; id != "" {
		if err := checkWsidUnique(id, ws.root); err != nil {
			return err
		}
	}

	step(stepCheckNixDaemon)
	if err := ws.checkNixDaemon(ctx); err != nil {
		return err
	}

	return v.run(ctx, ws, out, plan)
}
