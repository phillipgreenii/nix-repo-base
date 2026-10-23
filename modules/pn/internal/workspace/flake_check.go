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

	// NoBuild selects the land check: evaluate the derivation paths of checks,
	// packages and devShells instead of running `nix flake check` (see
	// flake_check_nobuild.go). The fields below apply only with NoBuild.
	NoBuild bool
	// Repo limits the check to one repo, named by key or by a path to the repo or
	// one of its worktrees. Empty means every repo, in topological order.
	Repo string
	// Rev evaluates that commit of Repo (a pure ref) instead of its working tree.
	Rev string
	// Caller labels the experiment log line: CallerManual (default) or CallerFF1c.
	Caller string
	// NoIFD passes `--option allow-import-from-derivation false` to nix.
	NoIFD bool
}

// FlakeCheck runs `nix flake check` in every workspace repo, injecting
// --override-input flags that pin the repo's local workspace siblings to their
// on-disk clones — so each repo is checked against your local changes, not its
// locked inputs. The repo under test is excluded from its own override set
// (it's the flake being evaluated, so it cannot override itself).
//
// Per-repo failures are collected; the overall call returns non-nil if any
// failed. Matches the bash "full sweep" behavior — does not short-circuit on
// first failure. Each check's output is streamed live to out. Warning output
// goes to errOut (stderr). Repos are processed in topological order
// (dependencies before consumers; foundation repos first, see ADR-0033).
// FlakeCheck is a terminal-optional command: if no terminal is configured it
// emits a warning to errOut and continues.
func (ws *Workspace) FlakeCheck(ctx context.Context, out io.Writer, errOut io.Writer, opts FlakeCheckOptions) error {
	if opts.NoBuild {
		return ws.flakeCheckNoBuild(ctx, out, errOut, opts)
	}
	if opts.Repo != "" || opts.Rev != "" || opts.NoIFD || opts.Caller != "" {
		return &UsageError{Msg: "--repo, --rev, --caller and --no-ifd apply only with --no-build"}
	}
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	var failed []string
	first := true
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		// Per-consumer override: inject workspace deps of this repo (excluding itself).
		overrides := ws.overrideInputArgsFor(name, overrideOpts{ExcludeRepo: name})
		args := append([]string{"flake", "check"}, overrides...)
		// Blank line between repo blocks (not before the first).
		if !first {
			fmt.Fprintln(out)
		}
		first = false
		fmt.Fprintf(out, "  --== flake-check %s ==--  \n", name)
		if err := inRepoSpan(ctx, name, func(ctx context.Context) error {
			_, err := ws.runner.Run(ctx, "nix", args, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out, WrapNix: true})
			return err
		}); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("flake check failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
