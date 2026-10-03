// internal/workspace/doctor_checks_precommithook.go
package workspace

import (
	"context"
	"path/filepath"
)

// checkPreCommitHookLive guards against a repo silently losing its
// git-triggered pre-commit gate (bd tc-0wzp/tc-771m): 5 of this workspace's 8
// repos once lost their local .git/hooks/pre-commit to a prek-version
// regression, and commits succeeded without the hook ever firing — "silently
// skipping validation rather than failing loudly." It was found by accident
// while working an unrelated bead, not by any audit (bd tc-wdwnl).
//
// Per-clone hook bundle (pg2-pla9d, ADR 0032, design sections 4.1, 7.4): for a
// repo whose pn-workspace.toml entry runs install-pre-commit-hooks (DECLARED),
// the audit is bundleHookFindings (errors, not Skipped: a missing pointer,
// missing or foreign stubs, a dangling bundle and unreachable hooks each mean
// the gate silently does not run). A clone that holds only an old
// .pre-commit-config.yaml symlink has no bundle and is reported as such (R4:
// there is no legacy state). A repo that does not declare the installer, and a
// directory that is not a git work tree, produce no finding.
func (ws *Workspace) checkPreCommitHookLive(_ context.Context, _ *doctorEnv) []Finding {
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		if !ws.installHookDeclared(name) {
			continue
		}
		if fs, ok := ws.bundleHookFindings(name, repoDir); ok {
			out = append(out, fs...)
		}
	}
	return out
}
