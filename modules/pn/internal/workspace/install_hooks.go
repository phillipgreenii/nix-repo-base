package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// installHooksRunArgs returns the argv for running a single install-hooks flake
// output in a repo: `nix run .#<output>`. Shared by InstallHooksInDir and its
// tests so the two cannot drift.
func installHooksRunArgs(output string) []string {
	return []string{"run", ".#" + output}
}

// InstallHooksInDir runs each opt-in flake output for one repo directory,
// (re)installing that repo's git pre-commit hooks. For each output (in list
// order) it runs `nix run .#<output>` in dir, streaming output live. Per-output
// failures are collected so every output is attempted; the returned error names
// the outputs that failed. When outputs is empty it does nothing and returns
// nil (no banner). This helper is reused per git worktree by other commands.
func (ws *Workspace) InstallHooksInDir(ctx context.Context, out, errOut io.Writer, label, dir string, outputs []string) error {
	if len(outputs) == 0 {
		return nil
	}
	fmt.Fprintf(out, "  --== install-hooks %s ==--  \n", label)
	var failed []string
	for _, output := range outputs {
		if _, err := ws.runner.Run(ctx, "nix", installHooksRunArgs(output), exec.RunOptions{Dir: dir, Stdout: out, Stderr: out}); err != nil {
			failed = append(failed, output)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("install-hooks in %s failed for output(s): %s", label, strings.Join(failed, ", "))
	}
	return nil
}

// InstallHooks (re)installs each workspace repo's git pre-commit hooks by
// running that repo's opt-in flake outputs (its per-repo `install-hooks` config
// key) via `nix run .#<name>`. Repos that did not opt in (absent or empty list)
// are skipped. Repos are processed in topological order (dependencies before
// consumers). Per-repo failures are collected; the overall call returns non-nil
// if any repo failed and does not short-circuit on the first failure. Each run's
// output is streamed live to out. This resyncs pre-commit configs that go stale
// when a repo's treefmt config changes (bd pg2-5yq5).
func (ws *Workspace) InstallHooks(ctx context.Context, out io.Writer, errOut io.Writer) error {
	names := ws.topoAlpha(ctx)
	var failed []string
	for _, name := range names {
		outputs := ws.config.Repos[name].InstallHooks
		if len(outputs) == 0 {
			continue
		}
		dir := filepath.Join(ws.root, name)
		if err := ws.InstallHooksInDir(ctx, out, errOut, name, dir, outputs); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("install-hooks failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
