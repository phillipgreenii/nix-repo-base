// internal/workspace/doctor_checks_hooks.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// checkHookExpressions emits advisory (SevWarning) findings about per-repo event
// hooks (bd pg2-uswb / pg2-id4a):
//   - hook-never-fires: a hook whose every `when` event names a command that
//     never processes the repo, so the hook can never run.
//   - hook-nix-run-output: a {nix_run <attr>} token whose <attr> is not a flake
//     output the repo exposes (the hook would fail at runtime). Probed via
//     `nix eval` and swallowed-as-absent per the edges.go convention; skipped
//     under --offline. Never a hard failure — runtime failure is the backstop.
//
// build_command/apply_command placeholder validity and {nix_run} well-formedness
// are already enforced at config load (ParseConfig hard-errors), so they cannot
// reach here; this check covers only what load-time validation cannot: output
// existence (needs nix eval) and firability (needs the command→repo mapping).
func (ws *Workspace) checkHookExpressions(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		for _, h := range ws.config.Repos[name].Hooks {
			if hookNeverFires(h, name, env.terminal) {
				fs = append(fs, Finding{
					CheckID: "hook-never-fires", Repo: name, Severity: SevWarning,
					Message: fmt.Sprintf(
						"per-repo hook events %v never process repo %q (repo-iterating commands + upgrade process all repos; build/apply only the terminal); the hook can never fire",
						h.When, name,
					),
				})
			}
			if env.offline {
				continue // the nix-eval output probe is a live call; skip it offline
			}
			for _, raw := range h.Run {
				for _, attr := range nixRunAttrsIn(raw) {
					if !ws.flakeHasOutput(ctx, name, attr) {
						fs = append(fs, Finding{
							CheckID: "hook-nix-run-output", Repo: name, Severity: SevWarning,
							Message: fmt.Sprintf(
								"{nix_run %s} in repo %q: flake output %q not found (hook would fail at runtime)",
								attr, name, attr,
							),
						})
					}
				}
			}
		}
	}
	return fs
}

// hookNeverFires reports whether none of h's events can ever process repo. It is
// conservative: an event whose firability is unknown (build/apply when the
// terminal is unset) counts as "could fire", so it is never flagged.
func hookNeverFires(h EventHook, repo, terminal string) bool {
	if len(h.When) == 0 {
		return false
	}
	for _, ev := range h.When {
		_, cmd, ok := splitEvent(ev)
		if !ok {
			return false // unknown event (rejected at load); don't second-guess
		}
		processes, known := commandProcessesRepo(cmd, repo, terminal)
		if processes || !known {
			return false // this event could fire the hook
		}
	}
	return true
}

// commandProcessesRepo reports whether pn-workspace command cmd processes repo
// (so a per-repo hook on cmd fires). known is false only for build/apply when
// the terminal is unset — the caller then treats firability as unknown.
func commandProcessesRepo(cmd, repo, terminal string) (processes, known bool) {
	if _, ok := repoIteratingCommands[cmd]; ok {
		return true, true
	}
	switch cmd {
	case "upgrade":
		return true, true
	case "build", "apply":
		if terminal == "" {
			return false, false
		}
		return repo == terminal, true
	default:
		// lock, init, tree — process no repos.
		return false, true
	}
}

// nixRunAttrsIn returns the attr of every well-formed {nix_run <attr>} token in raw.
func nixRunAttrsIn(raw string) []string {
	var attrs []string
	for _, m := range nixRunTokenRe.FindAllStringSubmatch(raw, -1) {
		attrs = append(attrs, m[1])
	}
	return attrs
}

// flakeHasOutput reports whether repo's flake exposes output attr, via
// `nix eval <flakeDir>#<attr> --apply '_: true'`. A non-nil error is swallowed as
// "absent" (edges.go convention): the attr is missing, or the flake did not
// evaluate — either way the hook cannot be confirmed to work.
func (ws *Workspace) flakeHasOutput(ctx context.Context, repo, attr string) bool {
	flakeDir := filepath.Join(ws.root, repo, filepath.Dir(ws.resolveFlakePath(repo)))
	_, err := ws.runner.Run(ctx, "nix", []string{"eval", flakeDir + "#" + attr, "--apply", "_: true"}, exec.RunOptions{})
	return err == nil
}
