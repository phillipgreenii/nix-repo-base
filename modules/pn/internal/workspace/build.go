package workspace

import (
	"context"
	"fmt"
	"io"

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

func (buildVerb) run(ctx context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) error {
	fmt.Fprintf(out, "  --== %s: building flake ==--  \n", p.terminal)
	ws.recordStep("build", false, stepReportApplyDecision)
	ws.reportApplyDecision(ctx, out, p.opts.OverridePaths)
	ws.recordStep("build", false, stepRunCommand)
	if err := ws.runPlanned(ctx, out, "build", p); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	fmt.Fprintln(out, "Build successful. To apply, run: pn workspace apply")
	return nil
}

// reportApplyDecision evaluates apply's skip gate (needsRebuild) READ-ONLY and
// prints what `pn workspace apply` would do right now:
//
//	apply would: skip (unchanged)
//	apply would: rebuild (<forced|dirty|head_changed|no_record>)
//
// It only runs `git status` / `git rev-parse` and reads the applied-state store;
// it never writes applied state and never changes build's own behavior. A gate
// failure (an unreadable store, a failing git) is reported as `apply would: fail
// (...)` — that is what apply would do (it fails closed) — and does not fail the
// build. force is never set: an operator's `apply --force` is not knowable here.
func (ws *Workspace) reportApplyDecision(ctx context.Context, out io.Writer, overridePaths map[string]string) {
	reason, err := ws.needsRebuild(ctx, ws.allRepoDirs(overridePaths), false)
	fmt.Fprintln(out, applyDecisionLine(reason, err))
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
