// internal/workspace/doctor_checks_terminal.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkTerminal audits the terminal's resolvability, the follows-correctness of
// its workspace inputs, and flake-path drift in the lock.
func (ws *Workspace) checkTerminal(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding

	// terminal-resolvable: surface deriveLock validation errors (missing_terminal,
	// terminal_not_sink, missing_flake_path).
	if _, verrs, err := deriveLock(ctx, ws, env.terminal); err == nil {
		for _, ve := range verrs {
			fs = append(fs, Finding{
				CheckID: "terminal-resolvable", Severity: SevError,
				Message: fmt.Sprintf("%s: %s", ve.Code, ve.Message),
				Manual:  terminalWarningMessage,
			})
		}
	}

	// follows-correct: only when the terminal is present on disk.
	if env.terminal != "" {
		termDir := filepath.Join(ws.root, env.terminal)
		if isGitRepo(termDir) {
			names := ws.workspaceInputNamesFromEdges(env.terminal)
			if err := checkFollows(termDir, names); err != nil {
				fs = append(fs, Finding{
					CheckID: "follows-correct", Repo: env.terminal, Severity: SevError,
					Message: err.Error(),
					Manual:  "edit the terminal flake.nix to add the inputs.<a>.inputs.<b>.follows lines shown above, then re-lock",
				})
			}
		}
	}

	// flake-path-resolves: lock's recorded FlakePath must match on-disk resolution.
	if env.lock != nil {
		for name := range ws.config.Repos {
			dir := filepath.Join(ws.root, name)
			if !isGitRepo(dir) {
				continue
			}
			recorded := env.lock.Repos[name].FlakePath
			actual := ws.resolveFlakePath(name)
			if recorded != "" && actual != "" && recorded != actual {
				rec := recorded
				fs = append(fs, Finding{
					CheckID: "flake-path-resolves", Repo: name, Severity: SevError,
					Message: fmt.Sprintf("repo %q lock flake_path %q != on-disk %q (wrong flake would be evaluated)", name, rec, actual),
					Fixable: true,
					fix:     func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
					Manual:  "pn workspace lock",
				})
			}
		}
	}
	return fs
}
