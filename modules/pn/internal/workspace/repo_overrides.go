package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotWorkspaceRepo is returned (wrapped) when the repo argument names no
// configured workspace repo, so a caller can tell "not a member" from a failure.
var ErrNotWorkspaceRepo = errors.New("not a workspace repo")

// RepoOverrides returns the `--override-input <alias> git+file://<abs sibling>`
// arguments pn would inject for ONE workspace repo, as one argument per element,
// plus the repo key they were computed for. It is the entry point behind
// `pn workspace overrides`: a caller that runs its own nix command against a repo
// (pre-land-eval, an ad-hoc `nix eval`) consumes the SAME override set that
// `build`, `apply` and `flake-check` use, instead of re-deriving it.
//
// repoArg names the repo either by its workspace key or by a path (see
// resolveRepoKey). The set is computed against the effective lock (the disk lock
// when it covers the configured repos, otherwise one derived in memory), exactly
// like the hook fan-out, so it is non-empty even before `pn workspace lock` ran.
// The repo is excluded from its own set. A sibling whose clone is missing yields
// no override, as for flake-check. Nothing is written: no flake.lock, no
// pn-workspace.lock.json, no applied state.
func (ws *Workspace) RepoOverrides(ctx context.Context, repoArg string) (key string, args []string, err error) {
	key, err = ws.resolveRepoKey(ctx, repoArg)
	if err != nil {
		return "", nil, err
	}
	lk, _, err := ws.effectiveLock(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("compute the workspace lock: %w", err)
	}
	return key, ws.overrideInputArgsForLock(lk, key, overrideOpts{ExcludeRepo: key}), nil
}

// resolveRepoKey maps repoArg to a configured repo key. In order:
//
//  1. repoArg is itself a repo key.
//  2. repoArg is a path equal to, or below, <root>/<key>. This covers the repo
//     itself, a directory inside it, a linked worktree under it (for example
//     <root>/<key>/.worktrees/<branch>) and a workforest set member, whose
//     <root> is the set root.
//  3. repoArg is a path whose git common directory belongs to <root>/<key> or
//     <canonical root>/<key>: a linked worktree kept outside the repo directory.
//
// Anything else is an error naming the configured keys.
func (ws *Workspace) resolveRepoKey(ctx context.Context, repoArg string) (string, error) {
	if repoArg == "" {
		return "", fmt.Errorf("repo is required: a workspace repo key or a path inside one: %w", ErrNotWorkspaceRepo)
	}
	if _, ok := ws.config.Repos[repoArg]; ok {
		return repoArg, nil
	}
	p := repoArg
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	names := orderedRepoNames(ws.config.Repos)
	resolved := func(dir string) string {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return real
		}
		return dir
	}
	for _, name := range names {
		if pathWithin(p, resolved(filepath.Join(ws.root, name))) {
			return name, nil
		}
	}
	if dirExists(p) {
		if common := ws.gitCommonDir(ctx, p); common != "" && filepath.Base(common) == ".git" {
			owner := filepath.Dir(common)
			for _, name := range names {
				if owner == resolved(filepath.Join(ws.root, name)) {
					return name, nil
				}
				if canon := ws.canonicalRoot(); canon != "" && owner == resolved(filepath.Join(canon, name)) {
					return name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%q: %w of %s (configured: %s)",
		repoArg, ErrNotWorkspaceRepo, ws.root, strings.Join(names, ", "))
}

// pathWithin reports whether p equals dir or lies below it.
func pathWithin(p, dir string) bool {
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(os.PathSeparator))+string(os.PathSeparator))
}
