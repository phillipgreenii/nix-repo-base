package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// hookableCommands is every pn-workspace command that may appear in an event
// name (`<pre|post>-<command>`).
var hookableCommands = map[string]struct{}{
	"clone": {}, "rebase": {}, "update": {}, "status": {}, "flake-check": {},
	"format": {}, "push": {}, "pre-commit-check": {}, "build": {}, "apply": {},
	"upgrade": {}, "lock": {}, "init": {}, "tree": {},
}

// splitEvent parses an event string "<pre|post>-<command>" into its phase and
// command. ok is false for an unknown phase or command.
func splitEvent(ev string) (HookPhase, string, bool) {
	if s, ok := strings.CutPrefix(ev, "pre-"); ok {
		if _, k := hookableCommands[s]; k {
			return HookPhasePre, s, true
		}
	}
	if s, ok := strings.CutPrefix(ev, "post-"); ok {
		if _, k := hookableCommands[s]; k {
			return HookPhasePost, s, true
		}
	}
	return 0, "", false
}

// repoIteratingCommands are the pn-workspace commands that process every repo in
// turn; only these (plus upgrade) fan a per-repo hook out to all repos.
var repoIteratingCommands = map[string]struct{}{
	"clone": {}, "rebase": {}, "update": {}, "status": {},
	"flake-check": {}, "format": {}, "push": {}, "pre-commit-check": {},
}

// validateAllHooks validates workspace- and repo-scoped event hooks at config
// load: every `when` event must be a known "<pre|post>-<command>"; each `run`
// entry may hold at most one {nix_run} token; and {nix_run} is valid only in
// per-repo hooks (a workspace hook has no repo to resolve it against).
func validateAllHooks(cfg *WorkspaceConfig) error {
	validate := func(h RepoHook, repoScoped bool) error {
		for _, ev := range h.When {
			if _, _, ok := splitEvent(ev); !ok {
				return fmt.Errorf("hook: unknown event %q (want <pre|post>-<command>)", ev)
			}
		}
		for _, entry := range h.Run {
			switch len(nixRunTokenRe.FindAllString(entry, -1)) {
			case 0:
				// no token
			case 1:
				if !repoScoped {
					return fmt.Errorf("hook %q: {nix_run …} is valid only in per-repo hooks", entry)
				}
			default:
				return fmt.Errorf("hook %q: v1 supports one {nix_run …} token per entry", entry)
			}
		}
		return nil
	}
	for _, h := range cfg.Hooks {
		if err := validate(h, false); err != nil {
			return err
		}
	}
	for _, r := range cfg.Repos {
		for _, h := range r.Hooks {
			if err := validate(h, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// eventName returns the "<phase>-<command>" event string.
func eventName(phase HookPhase, cmd string) string {
	if phase == HookPhasePre {
		return "pre-" + cmd
	}
	return "post-" + cmd
}

// processedReposFor returns the repos a command operates on — the set whose
// per-repo hooks should fire. Repo-iterating commands (and upgrade, whose update
// phase touches every repo) process all repos in topoAlpha order; build/apply
// process only the terminal; everything else processes none.
func (ws *Workspace) processedReposFor(ctx context.Context, cmd string) []string {
	if _, ok := repoIteratingCommands[cmd]; ok {
		return ws.topoAlpha(ctx)
	}
	switch cmd {
	case "upgrade":
		return ws.topoAlpha(ctx)
	case "build", "apply":
		if t, err := ws.config.TerminalRepo(); err == nil {
			return []string{t}
		}
	}
	return nil
}

// ProcessedReposFor is the exported wrapper the cli layer uses to compute the
// per-command repo set for runWithHooks.
func (ws *Workspace) ProcessedReposFor(ctx context.Context, cmd string) []string {
	return ws.processedReposFor(ctx, cmd)
}

// RunEventHooks fires the hooks for one (phase, command) event. Workspace
// [[hooks]] entries whose `when` contains the event run once at the workspace
// root; per-repo [[repos.<r>.hooks]] entries run in each processed repo
// (cwd=repo), expanding any {nix_run} token against that repo's flake +
// overrides. Pre-hooks abort on first failure; post-hooks warn and continue.
func (ws *Workspace) RunEventHooks(ctx context.Context, phase HookPhase, cmd string, processed []string, out io.Writer) error {
	ev := eventName(phase, cmd)
	// Workspace-scoped: once at root (no {nix_run}; enforced by validateAllHooks).
	for _, h := range ws.config.Hooks {
		if slices.Contains(h.When, ev) {
			if err := RunHooks(ctx, ws.runner, h.Run, ws.root, phase); err != nil {
				return err
			}
		}
	}
	// Repo-scoped: in each processed repo that declares a matching hook.
	for _, key := range processed {
		hooks := ws.config.Repos[key].Hooks
		if len(hooks) == 0 {
			continue
		}
		vars := ws.repoNixHookVars(ctx, key) // resolve once per repo
		dir := filepath.Join(ws.root, key)
		for _, h := range hooks {
			if !slices.Contains(h.When, ev) {
				continue
			}
			for _, raw := range h.Run {
				cmdStr, _, err := expandNixRunTokens(raw, vars)
				if err == nil {
					var resolved string
					if resolved, err = rewriteFirstToken(cmdStr, dir); err == nil {
						var res exec.Result
						res, err = ws.runner.Run(ctx, "sh", []string{"-c", resolved}, exec.RunOptions{Dir: dir, Stdout: out, Stderr: out})
						if err != nil && phase == HookPhasePost {
							_, _ = os.Stderr.Write(res.Stderr)
						}
					}
				}
				if err != nil {
					if phase == HookPhasePre {
						return fmt.Errorf("pre-hook %q in %s: %w", raw, key, err)
					}
					fmt.Fprintf(os.Stderr, "warning: post-hook %q in %s: %v\n", raw, key, err)
				}
			}
		}
	}
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
