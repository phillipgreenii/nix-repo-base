// internal/workspace/hooks_paths.go
package workspace

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Git hook-path helpers shared by the doctor hook checks. They live apart from
// doctor_checks_githooksshim.go so that file can be deleted with the shim
// experiment (design: per-clone hook bundle, section 7.4) without taking them
// along.

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
