package workspace

import (
	"context"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// NixCommand runs `nix <args>` from the workspace root, injecting one
// --override-input flag for each repo in the workspace lock so the local
// clone is used in place of the upstream input.
//
// Override flags are emitted in alphabetical order by input name for
// deterministic command construction.
//
// TODO: port the full pn-ws-nix subcommand-allow/deny list and
// --non-override-subcommand-action behavior; currently every subcommand
// receives overrides unconditionally.
func (ws *Workspace) NixCommand(ctx context.Context, args []string) error {
	overrides := computeOverrideArgs(ws)
	full := append([]string{}, args...)
	full = append(full, overrides...)
	_, err := ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root})
	return err
}
