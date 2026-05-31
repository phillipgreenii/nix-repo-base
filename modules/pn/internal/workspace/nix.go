package workspace

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// NixCommand runs `nix <args>` from the workspace root, injecting one
// --override-input flag for each repo in the workspace lock so the local
// clone is used in place of the upstream input.
//
// Override flags are emitted in alphabetical order by input name for
// deterministic command construction.
//
// TODO(tc-perh.5): port the full pn-ws-nix subcommand-allow/deny list and
// --non-override-subcommand-action behavior; currently every subcommand
// receives overrides unconditionally.
func (ws *Workspace) NixCommand(ctx context.Context, args []string) error {
	names := make([]string, 0, len(ws.lock.Repos))
	for n := range ws.lock.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	overrides := make([]string, 0, 3*len(names))
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		overrides = append(overrides, "--override-input", name, "path:"+repoDir)
	}
	full := append([]string{}, args...)
	full = append(full, overrides...)
	_, err := ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root})
	return err
}
