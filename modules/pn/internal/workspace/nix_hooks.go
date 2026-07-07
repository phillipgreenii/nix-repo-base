package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// validateAllHooks validates workspace- and repo-scoped event hooks at config
// load. The real checks (known events, {nix_run} placement + single-token) are
// added in a later step; the stub keeps ParseConfig callable meanwhile.
func validateAllHooks(cfg *WorkspaceConfig) error {
	return nil
}

// repoNixHookVars builds the per-repo values for expanding a {nix_run} token,
// resolving --override-input flags from the EFFECTIVE lock (derived when the
// disk lock is absent/stale) so the gate builds against local workspace
// siblings rather than locked inputs. If the effective lock is unavailable the
// overrides collapse to empty — which would silently build against locked
// inputs — so this warns rather than proceeding quietly (bd pg2-5yq5).
func (ws *Workspace) repoNixHookVars(ctx context.Context, key string) nixHookVars {
	lk, _, err := ws.effectiveLock(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: hook overrides for %s: effective lock unavailable (%v); gate may build against locked inputs\n", key, err)
	}
	return nixHookVars{
		NixExe:       "nix",
		OverrideArgs: ws.overrideInputArgsForLock(lk, key, overrideOpts{}),
		FlakeDir:     filepath.Join(ws.root, key, filepath.Dir(ws.resolveFlakePath(key))),
	}
}

// repoNixRunString returns the `sh -c`-ready command that runs flake output
// attr in repo key with the workspace's override overlays injected. The caller
// MUST set cwd to key's directory: install-pre-commit-hooks installs into $PWD,
// so cwd is load-bearing (the absolute flakeref only selects config content).
func (ws *Workspace) repoNixRunString(ctx context.Context, key, attr string) string {
	s, _, _ := expandNixRunTokens("{nix_run "+attr+"}", ws.repoNixHookVars(ctx, key))
	return s
}
