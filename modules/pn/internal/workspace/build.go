package workspace

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
)

// ensureExecTrusted gates execution of config-sourced command templates
// (build_command / apply_command) behind the same TOFU trust check as event
// hooks (ADR-0019). These commands are arbitrary argv taken from the
// pn-workspace.toml discovered by walking up from the cwd and executed by
// Build/Apply, so an untrusted checkout must not run them — closing the residual
// the hook trust gate left open (bead pg2-x2q6o). --root / PN_WORKSPACE_ROOT do
// NOT bypass it: an untrusted dir can also plant an env var. Callers invoke this
// as a pre-exec abort (mirroring RunEventHooks' pre-phase behavior); the
// ShowNixCommandsOnly dry-run, which only prints and never executes, is exempt.
func (ws *Workspace) ensureExecTrusted() error {
	return trust.EnsureAllowed(ws.root)
}

// BuildOptions configures Build.
type BuildOptions struct {
	Terminal            string            // overrides workspace.terminal for this invocation
	BuildCmd            string            // overrides build_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
	// Builder overrides the OS-detected {builder} value (activation tool).
	// Empty falls through to defaultBuilder().
	Builder string
	// Wait is how long to wait for a contended single-flight lock; 0 fails fast,
	// WaitForever waits indefinitely (see rebuildlock.go).
	Wait time.Duration
}

// Build builds the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It does not activate.
// Formatting is a separate step: run `pn workspace format` before building.
//
// Build runs the same shared prelude as Apply (Workspace.rebuild), so it
// reproduces the failures apply would hit before activation. Before building it
// also evaluates apply's skip gate READ-ONLY and prints one `apply would: ...`
// line; build itself NEVER skips and NEVER writes applied state.
func (ws *Workspace) Build(ctx context.Context, out io.Writer, opts BuildOptions) error {
	return ws.rebuild(ctx, out, buildVerb{}, rebuildOptions{
		Terminal:            opts.Terminal,
		Cmd:                 opts.BuildCmd,
		OverridePaths:       opts.OverridePaths,
		ShowNixCommandsOnly: opts.ShowNixCommandsOnly,
		Builder:             opts.Builder,
		Wait:                opts.Wait,
	})
}

// buildVerb is the build strategy of the rebuild Template Method.
type buildVerb struct{}

func (buildVerb) name() string { return "build" }

func (buildVerb) commandTemplate(ws *Workspace, opts rebuildOptions) (string, error) {
	if opts.Cmd != "" {
		return opts.Cmd, nil
	}
	return ws.config.BuildCommandTemplate(), nil
}

func (buildVerb) run(ctx context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) (err error) {
	fmt.Fprintf(out, "  --== %s: building flake ==--  \n", p.terminal)
	ws.recordStep("build", false, stepReportApplyDecision)
	reason, gateErr := ws.evalApplyDecision(ctx, p.opts.OverridePaths)

	// From here on the build output is retained in the build log, like apply's
	// (bead pg2-sdbhh4.14; see applylog.go). `out` is rebound to tee into it, so the
	// terminal output is unchanged, and a failure to open the log is reported and
	// otherwise ignored: it must never fail or alter the build. The `apply would:`
	// line below is the first thing retained.
	headerReason := string(reason)
	if gateErr != nil {
		headerReason = ""
	}
	blog, logErr := openRunLog(runLogOpts{
		Verb:     "build",
		Terminal: p.terminal,
		Cmdline:  append(append([]string{}, p.cmdArgs...), p.overrideArgs...),
		Begin:    p.begin,
		TraceID:  telemetry.TraceIDFromContext(ctx),
		Reason:   headerReason,
	})
	if logErr != nil {
		fmt.Fprintf(out, "warning: could not retain the build log: %v\n", logErr)
	}
	p.alog = blog
	out = blog.tee(out)
	defer func() { blog.finish(err) }()
	blog.recordRefs(nil)

	fmt.Fprintln(out, applyDecisionLine(reason, gateErr))
	ws.recordStep("build", false, stepRunCommand)
	blog.phase(phaseBuild)
	if runErr := ws.runPlanned(ctx, out, "build", p); runErr != nil {
		blog.setExit(runErr)
		return fmt.Errorf("build failed: %w", runErr)
	}
	blog.setExit(nil)
	fmt.Fprintln(out, "Build successful. To apply, run: pn workspace apply")
	return nil
}

// evalApplyDecision evaluates apply's skip gate (needsRebuild) READ-ONLY and
// records the verdict in OTel; the caller prints what `pn workspace apply` would
// do right now (applyDecisionLine):
//
//	apply would: skip (unchanged)
//	apply would: rebuild (<forced|dirty|head_changed|no_record>)
//
// It only runs `git status` / `git rev-parse` and reads the applied-state store;
// it never writes applied state and never changes build's own behavior. A gate
// failure (an unreadable store, a failing git) is reported as `apply would: fail
// (...)` — that is what apply would do (it fails closed) — and does not fail the
// build. force is never set: an operator's `apply --force` is not knowable here.
func (ws *Workspace) evalApplyDecision(ctx context.Context, overridePaths map[string]string) (rebuildReason, error) {
	reason, err := ws.needsRebuild(ctx, ws.allRepoDirs(overridePaths), false)
	if err == nil {
		telemetry.RecordApplyDecision(ctx, reason.rebuild(), string(reason))
	}
	return reason, err
}

// applyDecisionLine renders the gate's verdict as the `apply would: ...` line.
func applyDecisionLine(reason rebuildReason, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("apply would: fail (%v)", err)
	case reason == reasonUnchanged:
		return "apply would: skip (unchanged)"
	default:
		return fmt.Sprintf("apply would: rebuild (%s)", reason)
	}
}

// effectiveTerminal returns the terminal repo key: flagTerminal if non-empty,
// otherwise the config's workspace.terminal. Used by non-required commands
// that accept a --terminal flag.
func (ws *Workspace) effectiveTerminal(flagTerminal string) (string, error) {
	if flagTerminal != "" {
		return flagTerminal, nil
	}
	return ws.config.TerminalRepo()
}
