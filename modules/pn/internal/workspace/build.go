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
	BuildCmd            string            // overrides build_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
}

// Build formats and builds the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It does not activate.
func (ws *Workspace) Build(ctx context.Context, out io.Writer, opts BuildOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	terminalDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalDir = td
	}

	overrides := ws.overrideInputArgs(overrideOpts{ExcludeTerminal: true, OverridePaths: opts.OverridePaths})

	if err := checkFollows(terminalDir, ws.workspaceInputNames(terminal)); err != nil {
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
		fmt.Fprintf(out, "cd %s && nix fmt\n", terminalDir)
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	fmt.Fprintf(out, "  --== %s: formatting flake ==--  \n", terminal)
	if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("nix fmt in %s: %w", terminalDir, err)
	}

	fmt.Fprintf(out, "  --== %s: building flake ==--  \n", terminal)
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	fmt.Fprintln(out, "Build successful. To apply, run: pn workspace apply")
	return nil
}

// workspaceInputNames returns the resolved input names of all non-terminal
// repos (used for check_follows).
func (ws *Workspace) workspaceInputNames(terminal string) []string {
	var names []string
	for _, key := range orderedRepoNames(ws.config.Repos) {
		if key == terminal {
			continue
		}
		names = append(names, ws.config.InputNameFor(key))
	}
	return names
}
