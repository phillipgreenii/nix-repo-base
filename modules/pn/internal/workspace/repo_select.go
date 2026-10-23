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
