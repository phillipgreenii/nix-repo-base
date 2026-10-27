package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

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

// runPlanned runs the verb's nix command with its output tapped for the build
// plan (plan.go), then records the plan in OTel (RecordPlan, once per run that
// reached the build step, 0 builds when nix printed no plan). The tap passes
// the child's bytes through unchanged and nothing here stops or signals the
// child. out is what the operator sees (for apply: the terminal tee'd into the
// apply log), so the optional warning reaches both.
func (ws *Workspace) runPlanned(ctx context.Context, out io.Writer, verb string, p *rebuildPlan) error {
	tap := newPlanTap(out, ws.config.WarnBuildsThreshold())
	_, err := ws.runner.Run(ctx, p.cmdArgs[0], p.fullArgs(), exec.RunOptions{
		Dir:     p.terminalRepoDir,
		Stdout:  tap,
		Stderr:  tap,
		WrapNix: true,
		Env:     p.toolEnv,
	})
	tap.finish()
	st := tap.stats()
	telemetry.RecordPlan(ctx, verb, telemetry.Plan{
		Builds:      st.Builds,
		Fetches:     st.Fetches,
		DownloadMiB: st.DownloadMiB,
		HasDownload: st.HasDownload,
	})
	return err
}

// rebuild is the Template Method: the shared prelude, then the verb's run.
//
// Prelude order (identical for both verbs): requireTerminal, pn.repo span,
// resolve overrides once, checkFollows, template substitution (shared hostname
// resolver), the dry-run exit, tool resolution, the trust gate, checkWsidUnique, checkNixDaemon.
func (ws *Workspace) rebuild(ctx context.Context, out io.Writer, v rebuildVerb, opts rebuildOptions) (err error) {
	verb := v.name()
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
	cmdArgs, err := substituteCommand(tmpl, templateVars{
		TerminalRepoDir:    terminalRepoDir,
		TerminalNixDir:     terminalNixDir,
		TerminalNixRelPath: nixRel,
		Hostname:           ws.hostname(),
		Builder:            builder,
	})
	if err != nil {
		return err
	}
	plan := &rebuildPlan{
		opts:              opts,
		terminal:          terminal,
		terminalRepoDir:   terminalRepoDir,
		terminalNixDir:    terminalNixDir,
		resolvedOverrides: resolvedOverrides,
		overrideArgs:      overrideInputArgs(resolvedOverrides),
		cmdArgs:           cmdArgs,
	}

	step(stepDryRunGate)
	if opts.ShowNixCommandsOnly {
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), plan.overrideArgs...), " "))
		return nil
	}

	// Resolve the command's tool BEFORE the trust gate and every other probe, so a
	// missing darwin-rebuild fails at once with nothing else done (bead
	// pg2-sdbhh4.12).
	step(stepResolveTool)
	toolEnv, err := ws.resolveCommandTool(ctx, cmdArgs)
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
