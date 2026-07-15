package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
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
func (ws *Workspace) Build(ctx context.Context, out io.Writer, opts BuildOptions) error {
	terminal, err := ws.requireTerminal(ctx, opts.Terminal)
	if err != nil {
		return err
	}
	terminalRepoDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalRepoDir = td
	}
	nixRel := filepath.Dir(ws.resolveFlakePath(terminal))
	terminalNixDir := filepath.Join(terminalRepoDir, nixRel)

	overrides := ws.overrideInputArgsFor(terminal, overrideOpts{OverridePaths: opts.OverridePaths})

	if err := checkFollows(terminalNixDir, ws.workspaceInputNamesFromEdges(terminal)); err != nil {
		return err
	}

	tmpl := ws.config.BuildCommandTemplate()
	if opts.BuildCmd != "" {
		tmpl = opts.BuildCmd
	}
	builder := opts.Builder
	if builder == "" {
		builder = defaultBuilder()
	}
	cmdArgs, err := substituteCommand(tmpl, templateVars{
		TerminalRepoDir:    terminalRepoDir,
		TerminalNixDir:     terminalNixDir,
		TerminalNixRelPath: nixRel,
		Hostname:           shortHostname(),
		Builder:            builder,
	})
	if err != nil {
		return err
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	// Trust gate (bead pg2-x2q6o): abort before executing the config-sourced
	// build_command when the workspace root is untrusted.
	if err := ws.ensureExecTrusted(); err != nil {
		return err
	}

	fmt.Fprintf(out, "  --== %s: building flake ==--  \n", terminal)
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalRepoDir, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	fmt.Fprintln(out, "Build successful. To apply, run: pn workspace apply")
	return nil
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
