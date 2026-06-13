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
// locked inputs. The terminal (the build target) and the repo under test (the
// flake being evaluated) are excluded from its own override set, matching how
// the bash ran each check via pn-ws-nix.
//
// Per-repo failures are collected; the overall call returns non-nil if any
// failed. Matches the bash "full sweep" behavior — does not short-circuit on
// first failure. Each check's output is streamed live to out.
func (ws *Workspace) FlakeCheck(ctx context.Context, out io.Writer, opts FlakeCheckOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	var failed []string
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		overrides := ws.overrideInputArgs(overrideOpts{ExcludeTerminal: true, ExcludeRepo: name})
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
