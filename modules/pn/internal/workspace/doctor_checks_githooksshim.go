// internal/workspace/doctor_checks_githooksshim.go
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// shimHooksDirName is the committed directory holding the commit-time hook
// shims (ADR 0029, bead pg2-z19ad). Its presence in a checkout is how doctor
// and the install gate recognise a repo that opted into the experiment; a repo
// with the option OFF has no such directory and keeps the legacy behaviour.
const shimHooksDirName = ".githooks"

// gitOutput runs `git <args>` in dir and returns trimmed stdout, or "" on any
// error. Used for read-only config/path resolution only.
func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// localHooksPath returns the repo's LOCAL core.hooksPath ("" when unset).
func localHooksPath(repoDir string) string {
	return gitOutput(repoDir, "config", "--local", "--get", "core.hooksPath")
}

// resolvedHooksDir returns the absolute directory git actually runs hooks from
// (honouring core.hooksPath, relative or absolute, and linked worktrees), or
// the legacy <repo>/.git/hooks when git cannot say. This is the directory the
// pre-commit-hook-live audit MUST inspect: with the shim enabled the legacy
// .git/hooks shim is bypassed and may legitimately go stale.
func resolvedHooksDir(repoDir string) string {
	if d := gitOutput(repoDir, "rev-parse", "--path-format=absolute", "--git-path", "hooks"); d != "" {
		return d
	}
	return filepath.Join(repoDir, ".git", "hooks")
}

// shimDeclared reports whether the checkout commits a .githooks directory.
func shimDeclared(repoDir string) bool {
	info, err := os.Stat(filepath.Join(repoDir, shimHooksDirName))
	return err == nil && info.IsDir()
}

// shimHooksPathWired reports whether the install step has nothing left to do
// for the shim wiring: either the repo does not use the shim, or its local
// core.hooksPath is already the relative ".githooks".
func shimHooksPathWired(repoDir string) bool {
	if !shimDeclared(repoDir) {
		return true
	}
	return localHooksPath(repoDir) == shimHooksDirName
}

// configuredStages returns the sorted union of the `stages` of every hook in the
// generated .pre-commit-config.yaml (git-hooks.nix emits JSON), falling back to
// default_stages and finally to {"pre-commit"} when the config is unreadable.
// It mirrors the stage set checks.pre-commit-githooks-wired pins at build time.
func configuredStages(repoDir string) []string {
	set := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(repoDir, preCommitConfigName))
	if err == nil {
		// Strip the leading "# ..." comment lines git-hooks.nix prepends.
		var body []string
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "#") {
				continue
			}
			body = append(body, ln)
		}
		var cfg struct {
			DefaultStages []string `json:"default_stages"`
			Repos         []struct {
				Hooks []struct {
					Stages []string `json:"stages"`
				} `json:"hooks"`
			} `json:"repos"`
		}
		if json.Unmarshal([]byte(strings.Join(body, "\n")), &cfg) == nil {
			for _, r := range cfg.Repos {
				for _, h := range r.Hooks {
					for _, s := range h.Stages {
						set[s] = true
					}
				}
			}
			if len(set) == 0 {
				for _, s := range cfg.DefaultStages {
					set[s] = true
				}
			}
		}
	}
	if len(set) == 0 {
		set["pre-commit"] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// checkGitHooksShimWired is the commit-time-visible guard for the silent-ungate
// hazard of the shim experiment (ADR 0029): the install step sets a RELATIVE
// core.hooksPath=.githooks, so a branch or worktree whose tree lacks .githooks/
// is silently ungated. checks.pre-commit-githooks-wired only runs at
// `nix flake check` / land time, and a post-checkout guard cannot exist (it
// would live in .githooks). This is therefore the only commit-adjacent guard.
//
// Applies only to a repo whose LOCAL core.hooksPath is a RELATIVE path (the
// shim wiring); repos on the legacy absolute path are covered by
// pre-commit-hook-live and are unaffected. For such a repo, the resolved hooks
// dir MUST contain an executable shim for EVERY stage the generated config uses.
// Severity is an error that fails the exit code: a missing shim is a gate that
// silently does not run.
func (ws *Workspace) checkGitHooksShimWired(_ context.Context, _ *doctorEnv) []Finding {
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		hp := localHooksPath(repoDir)
		if hp == "" || filepath.IsAbs(hp) {
			continue
		}
		dir := resolvedHooksDir(repoDir)
		var bad []string
		for _, stage := range configuredStages(repoDir) {
			info, err := os.Stat(filepath.Join(dir, stage))
			switch {
			case err != nil:
				bad = append(bad, stage+" (missing)")
			case info.IsDir() || info.Mode()&0o111 == 0:
				bad = append(bad, stage+" (not executable)")
			}
		}
		if len(bad) == 0 {
			continue
		}
		out = append(out, Finding{
			CheckID: "git-hooks-shim-wired", Repo: name, Severity: SevError,
			Message: fmt.Sprintf(
				"repo %q has core.hooksPath=%s resolving to %s, which lacks an executable shim for: %s; those stages are SILENTLY UNGATED on commit (ADR 0029)",
				name, hp, dir, strings.Join(bad, ", "),
			),
			Manual: fmt.Sprintf("restore the committed shims (git checkout -- %s) or unwire: (cd %s && git config --local core.hooksPath \"$(git rev-parse --git-common-dir)/hooks\")", shimHooksDirName, repoDir),
		})
	}
	return out
}
