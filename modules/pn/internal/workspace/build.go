package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// BuildOptions configures Build.
type BuildOptions struct {
	Terminal            string            // overrides workspace.terminal for this invocation
	BuildCmd            string            // overrides build_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
}

// Build builds the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It does not activate.
// Formatting is a separate step: run `pn workspace format` before building.
func (ws *Workspace) Build(ctx context.Context, out io.Writer, opts BuildOptions) error {
	terminal, err := ws.requireTerminal(ctx, opts.Terminal)
	if err != nil {
		return err
	}
	terminalDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalDir = td
	}

	overrides := ws.overrideInputArgsFor(terminal, overrideOpts{OverridePaths: opts.OverridePaths})

	if err := checkFollows(terminalDir, ws.workspaceInputNamesFromEdges(terminal)); err != nil {
		return err
	}

	tmpl := ws.config.BuildCommandTemplate()
	if opts.BuildCmd != "" {
		tmpl = opts.BuildCmd
	}
	cmdArgs := substituteCommand(tmpl, terminalDir, shortHostname())
	if len(cmdArgs) == 0 {
		return fmt.Errorf("build_command is empty")
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	fmt.Fprintf(out, "  --== %s: building flake ==--  \n", terminal)
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
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
