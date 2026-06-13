package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ApplyOptions configures Apply.
type ApplyOptions struct {
	Terminal            string            // overrides workspace.terminal for this invocation
	ApplyCmd            string            // overrides apply_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
	Force               bool // always rebuild (bypass the skip gate)
}

// Apply formats and activates the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It checks daemon health, skips the rebuild
// when nothing changed, diffs the system profile via nvd when available, and
// records the applied state.
func (ws *Workspace) Apply(ctx context.Context, out io.Writer, opts ApplyOptions) error {
	terminal, err := ws.effectiveTerminal(opts.Terminal)
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

	tmpl := opts.ApplyCmd
	if tmpl == "" {
		tmpl, err = ws.config.ApplyCommandTemplate()
		if err != nil {
			return err
		}
	}
	cmdArgs := substituteCommand(tmpl, terminalDir, shortHostname())
	if len(cmdArgs) == 0 {
		return fmt.Errorf("apply_command is empty")
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintf(out, "cd %s && nix fmt\n", terminalDir)
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	if err := ws.checkNixDaemon(ctx); err != nil {
		return err
	}

	fmt.Fprintf(out, "  --== %s: formatting flake ==--  \n", terminal)
	if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("nix fmt in %s: %w", terminalDir, err)
	}

	fmt.Fprintf(out, "  --== %s: applying flake ==--  \n", terminal)
	allDirs := ws.allRepoDirs(opts.OverridePaths)
	rebuild, err := ws.needsRebuild(ctx, allDirs, opts.Force, out)
	if err != nil {
		return err
	}
	if !rebuild {
		return nil
	}

	oldProfile := readSystemProfile()
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	newProfile := readSystemProfile()
	if oldProfile != newProfile && newProfile != "" && commandExists("nvd") {
		fmt.Fprintln(out, "  --== Package changes ==--  ")
		_, _ = ws.runner.Run(ctx, "nvd", []string{"diff", oldProfile, newProfile}, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out})
	}

	return ws.markApplied(ctx, allDirs)
}

// allRepoDirs returns the clone dir for every declared repo that exists on
// disk, honoring overrides. Missing clones are skipped so the rebuild gate and
// mark-applied don't fail on a repo that hasn't been cloned yet.
func (ws *Workspace) allRepoDirs(overrides map[string]string) []string {
	var dirs []string
	for _, key := range orderedRepoNames(ws.config.Repos) {
		dir := filepath.Join(ws.root, key)
		if ov, ok := overrides[key]; ok {
			dir = ov
		}
		if !dirExists(dir) {
			continue
		}
		dirs = append(dirs, dir)
	}
	return dirs
}

const systemProfileLink = "/nix/var/nix/profiles/system"

// readSystemProfile resolves the current system profile to an absolute store
// path, or "" if it cannot be read.
func readSystemProfile() string {
	target, err := os.Readlink(systemProfileLink)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	return filepath.Join(filepath.Dir(systemProfileLink), target)
}

func commandExists(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}
