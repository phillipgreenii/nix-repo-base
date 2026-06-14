package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// CloneOptions configures workspace clone behavior.
type CloneOptions struct {
	// Terminal is an optional terminal override (accepted for uniformity;
	// has no behavioral effect on the clone command itself).
	Terminal string
}

// Clone reads pn-workspace.toml and clones every listed repo that is not
// already present on disk. Repos that already exist (identified by the
// presence of a .git directory) are skipped. Idempotent: running Clone
// twice in a row produces no git calls on the second run.
//
// Clone progress is streamed to out. Errors from git are returned immediately,
// naming the failing repo.
//
// Honor per-repo config fields: url, branch, and remotes (multi-remote).
func (w *Workspace) Clone(ctx context.Context, out io.Writer, opts CloneOptions) error {
	names := make([]string, 0, len(w.config.Repos))
	for n := range w.config.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		r := w.config.Repos[name]
		repoDir := filepath.Join(w.root, name)

		// Skip already-cloned repos (idempotent).
		if isGitRepo(repoDir) {
			continue
		}

		// Determine clone URL and branch.
		cloneURL, branch, err := cloneURLAndBranch(r)
		if err != nil {
			return fmt.Errorf("clone %s: %w", name, err)
		}

		fmt.Fprintf(out, "  --== clone %s ==--  \n", name)
		if _, err := w.runner.Run(ctx, "git",
			[]string{"clone", "--branch", branch, cloneURL, repoDir},
			exec.RunOptions{Dir: w.root, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("clone %s: %w", name, err)
		}

		// Add extra remotes declared in [[repos.X.remotes]].
		for _, rm := range r.Remotes {
			if rm.Name == "origin" {
				// Origin was set by git clone; skip adding it again.
				continue
			}
			if _, err := w.runner.Run(ctx, "git",
				[]string{"-C", repoDir, "remote", "add", rm.Name, rm.URL},
				exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				return fmt.Errorf("clone %s: add remote %s: %w", name, rm.Name, err)
			}
		}
	}
	return nil
}

// cloneURLAndBranch returns the clone URL and branch for a RepoConfig.
// For the single-url form, it converts github: shorthand to HTTPS.
// For the multi-remote form, origin's URL is used if present, otherwise
// the first remote. Returns an error when neither url nor remotes are set.
func cloneURLAndBranch(r RepoConfig) (url, branch string, err error) {
	branch = r.Branch
	if branch == "" {
		branch = "main"
	}

	if r.URL != "" {
		return flakeURLToHTTPS(r.URL), branch, nil
	}

	// Multi-remote form: prefer origin, fall back to first.
	for _, rm := range r.Remotes {
		if rm.Name == "origin" {
			return rm.URL, branch, nil
		}
	}
	if len(r.Remotes) > 0 {
		return r.Remotes[0].URL, branch, nil
	}

	return "", "", fmt.Errorf("repo config has no url and no remotes")
}
