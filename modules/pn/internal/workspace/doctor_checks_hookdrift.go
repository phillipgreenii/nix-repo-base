// internal/workspace/doctor_checks_hookdrift.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hookDriftCheckID is the doctor check id of the drift rows.
const hookDriftCheckID = "hook-drift"

// checkHookDrift is the runtime half of the hook drift guard (plan Task 22,
// design 7.1 step 6 and section 8; ADR 0032). Its source-tree half is the
// pg-hooks-drift-guard flake check; this check looks at the same conditions in
// the clones on this machine, in addition to the per-repo bundle-state check
// (checkPreCommitHookLive):
//
//   - a `.githooks` directory in a checkout (the removed commit-time shim's
//     committed hook directory): an error;
//   - a relative core.hooksPath in any config scope: an error, because the
//     only legal values are the absolute <common-dir>/hooks and the global
//     dispatcher's directory;
//   - a `.pre-commit-config.yaml` in a checkout that is not the old generated
//     symlink into /nix/store: a warning, because it was hand-made (a link or a
//     copy) and tooling MUST NOT link, copy or regenerate a hook config.
//
// The old generated symlink into /nix/store is deliberately NOT a finding: about
// 25 worktrees and every canonical clone still hold one until the follow-up that
// retires the ADR 0016 gitignore rule runs (ADR 0032, R2). Every workspace repo
// that is a git work tree is audited, whether or not it declares the installer,
// along with its linked worktrees.
func (ws *Workspace) checkHookDrift(_ context.Context, _ *doctorEnv) []Finding {
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		c, err := readHookGitContext(repoDir)
		if err != nil {
			continue
		}
		out = append(out, relativeHooksPathFindings(name, repoDir, c)...)
		for _, checkout := range hookCheckouts(repoDir) {
			out = append(out, githooksDirFindings(name, checkout)...)
			out = append(out, handMadeConfigFindings(name, checkout)...)
		}
	}
	return out
}

func hookDriftFinding(repo string, sev Severity, msg, manual string) Finding {
	return Finding{
		CheckID: hookDriftCheckID, Repo: repo, Severity: sev,
		Message: fmt.Sprintf("hook drift: repo %q: %s", repo, msg), Manual: manual,
	}
}

// hookCheckouts returns repoDir followed by its linked worktrees that still
// exist on disk, from one `git worktree list --porcelain`.
func hookCheckouts(repoDir string) []string {
	out := []string{repoDir}
	listing, err := runGitIn(repoDir, "", "worktree", "list", "--porcelain")
	if err != nil {
		return out
	}
	seen := map[string]bool{physicalDir(repoDir): true}
	for _, ln := range strings.Split(listing, "\n") {
		path, ok := strings.CutPrefix(ln, "worktree ")
		if !ok || path == "" {
			continue
		}
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			continue
		}
		if p := physicalDir(path); !seen[p] {
			seen[p] = true
			out = append(out, path)
		}
	}
	return out
}

// githooksDirFindings reports a `.githooks` directory in the checkout.
func githooksDirFindings(repo, checkout string) []Finding {
	dir := filepath.Join(checkout, ".githooks")
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return nil
	}
	return []Finding{hookDriftFinding(
		repo, SevError,
		fmt.Sprintf("%s exists; the commit-time shim's hook directory was removed (ADR 0032) and git must reach hooks through the per-clone bundle", dir),
		fmt.Sprintf("review it, then remove it:  rm -r %s", dir),
	)}
}

// relativeHooksPathFindings reports every relative core.hooksPath value visible
// from repoDir, in any scope (--get-all, so a shadowed value counts too).
func relativeHooksPathFindings(repo, repoDir string, c hookGitContext) []Finding {
	out, err := runGitIn(repoDir, "", "config", "--show-scope", "--show-origin", "--get-all", "core.hooksPath")
	if err != nil {
		return nil
	}
	var fs []Finding
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) != 3 || !relativeHooksPath(parts[2]) {
			continue
		}
		scope, value := parts[0], parts[2]
		origin := absoluteConfigOrigin(scope, parts[1], c)
		manual := fmt.Sprintf("operator:  remove core.hooksPath=%s from %s", value, origin)
		if scope == "local" {
			manual = fmt.Sprintf("operator:  git -C %s config --local --unset core.hooksPath", c.canonical())
		}
		fs = append(fs, hookDriftFinding(
			repo, SevError,
			fmt.Sprintf("core.hooksPath=%s (%s scope, %s) is a relative path; the only legal values are the absolute %s or the global dispatcher directory",
				value, scope, origin, filepath.Join(c.Common, "hooks")),
			manual,
		))
	}
	return fs
}

// relativeHooksPath reports whether a core.hooksPath value is relative: not
// empty, not absolute and not home-relative ("~/...", which git expands).
func relativeHooksPath(v string) bool {
	return v != "" && !filepath.IsAbs(v) && !strings.HasPrefix(v, "~/") && v != "~"
}

// handMadeConfigFindings reports a `.pre-commit-config.yaml` that is not the old
// generated symlink into /nix/store (ADR 0016): a regular file (a copy) or a
// symlink to anywhere else (a hand-made link).
func handMadeConfigFindings(repo, checkout string) []Finding {
	p := filepath.Join(checkout, ".pre-commit-config.yaml")
	fi, err := os.Lstat(p)
	if err != nil {
		return nil
	}
	what := "a regular file"
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil || strings.HasPrefix(target, "/nix/store/") {
			return nil // the old generated link (ADR 0016), kept until retired (R2)
		}
		what = "a symlink to " + target
	case fi.IsDir():
		what = "a directory"
	}
	return []Finding{hookDriftFinding(
		repo, SevWarning,
		fmt.Sprintf("%s is %s, not the generated link into /nix/store; tooling MUST NOT link, copy or regenerate a hook config (ADR 0032) and nothing reads it", p, what),
		fmt.Sprintf("remove it (hooks run from the bundle):  rm -r %s", p),
	)}
}
