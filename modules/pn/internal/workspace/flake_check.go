package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// FlakeCheckOptions configures FlakeCheck.
type FlakeCheckOptions struct {
	Terminal string // overrides workspace.terminal for this invocation
}

// FlakeCheck runs `nix flake check` in every workspace repo, injecting
// --override-input flags that pin the repo's local workspace siblings to their
// on-disk clones — so each repo is checked against your local changes, not its
// locked inputs. The repo under test is excluded from its own override set
// (it's the flake being evaluated, so it cannot override itself).
//
// Per-repo failures are collected; the overall call returns non-nil if any
// failed. Matches the bash "full sweep" behavior — does not short-circuit on
// first failure. Each check's output is streamed live to out. Repos are
// processed in topological order (dependencies before consumers).
// FlakeCheck is a terminal-optional command: if no terminal is configured it
// emits a warning and continues.
func (ws *Workspace) FlakeCheck(ctx context.Context, out io.Writer, opts FlakeCheckOptions) error {
	if ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(out, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	var failed []string
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		// Per-consumer override: inject workspace deps of this repo (excluding itself).
		overrides := ws.overrideInputArgsFor(name, overrideOpts{ExcludeRepo: name})
		args := append([]string{"flake", "check"}, overrides...)
		fmt.Fprintf(out, "  --== flake-check %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "nix", args, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out}); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("flake check failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
