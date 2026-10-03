package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
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
	validate := func(h EventHook, repoScoped bool) error {
		for _, ev := range h.When {
			if _, _, ok := splitEvent(ev); !ok {
				return fmt.Errorf("hook: unknown event %q (want <pre|post>-<command>)", ev)
			}
		}
		for _, entry := range h.Run {
			wellFormed := nixRunTokenRe.FindAllString(entry, -1)
			// A "{nix_run" opener that did not form a well-formed token is a
			// malformed near-miss (no attr, illegal char); reject it rather than
			// let the literal reach the shell.
			if len(nixRunOpenerRe.FindAllString(entry, -1)) != len(wellFormed) {
				return fmt.Errorf("hook %q: malformed {nix_run …} token (want {nix_run <attr>} with attr matching [A-Za-z0-9._-]+)", entry)
			}
			switch len(wellFormed) {
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

// installPreCommitHooksAttr is the {nix_run} attr name for the flake output
// that (re)builds a repo's per-clone hook bundle and installs the git hook
// stubs (ADR 0032). It is the sole {nix_run} hook declared on every workspace
// repo (pn-workspace.toml's post-clone/-rebase/-update/-upgrade entries), and
// realizing it is the dominant recurring cost the idempotency gate below
// targets — each invocation is a `nix run --override-input …`, and
// --override-input defeats nix's flake-eval cache, forcing a full re-evaluation
// of the whole flake graph even when the install itself has nothing to do. See
// the amendment to ADR-0019 (bead pg2-19rcj).
const installPreCommitHooksAttr = "install-pre-commit-hooks"

// installHookUpToDate is the "already installed" gate for
// {nix_run install-pre-commit-hooks} (bead pg2-19rcj, rekeyed by pg2-pla9d.10,
// spec 4.4): true when running the installer would be a pure no-op, so the hook
// can be skipped before a `nix run --override-input` re-evaluates the whole
// flake graph.
//
//   - A hook bundle in state present (pointer valid, bundle live, stamp matching,
//     and every recorded override's HEAD and dirty state equal to now) skips.
//     In a linked worktree the bundle must be the worktree's OWN private one:
//     the shared bundle being present does not give a set its private bundle.
//   - stale, broken and relocated run the installer, and so does a bundle whose
//     GC root dangles.
//   - With NO bundle pointer at all (state missing, or unreachable by a
//     core.hooksPath), and for a directory that is not a git work tree, the
//     installer runs: there is nothing to skip. A clone that still holds only
//     an old .pre-commit-config.yaml symlink reads as missing (R4).
func installHookUpToDate(dir string) bool {
	state, info, err := ReadHookBundleState(dir)
	if err != nil || info.Gen == "" {
		return false
	}
	return state == HookBundlePresent && (!info.Linked || info.Private)
}

// privateInstallArgs renders the installer arguments for a linked worktree:
// `--private` plus one `--override <alias>=<path>` per `--override-input
// <alias> git+file://<path>` pin in overrideArgs (the triples
// overrideInputArgs emits), so the installer records the same pins nix got.
func privateInstallArgs(overrideArgs []string) []string {
	args := []string{"--private"}
	for i := 0; i+2 < len(overrideArgs); i++ {
		if overrideArgs[i] != "--override-input" {
			continue
		}
		args = append(args, "--override",
			overrideArgs[i+1]+"="+strings.TrimPrefix(overrideArgs[i+2], "git+file://"))
		i += 2
	}
	return args
}

// eventName returns the "<phase>-<command>" event string.
func eventName(phase HookPhase, cmd string) string {
	if phase == HookPhasePre {
		return "pre-" + cmd
	}
	return "post-" + cmd
}

// ProcessedReposFor returns the repos a command operates on — the set whose
// per-repo hooks should fire. Repo-iterating commands (and upgrade, whose update
// phase touches every repo) process all repos in topoAlpha order; build/apply
// process only the terminal; everything else processes none.
func (ws *Workspace) ProcessedReposFor(ctx context.Context, cmd string) []string {
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

// RunEventHooks fires the hooks for one (phase, command) event. Workspace
// [[hooks]] entries whose `when` contains the event run once at the workspace
// root; per-repo [[repos.<r>.hooks]] entries run in each processed repo
// (cwd=repo), expanding any {nix_run} token against that repo's flake +
// overrides. Pre-hooks abort on first failure; post-hooks warn and continue.
// A per-repo {nix_run install-pre-commit-hooks} entry is additionally skipped
// (for every event, every caller — workforest add/add-repo included) when
// that repo's hook bundle is present and current; see installHookUpToDate. In a linked
// worktree the installer is invoked with `--private` and the set's pins.
func (ws *Workspace) RunEventHooks(ctx context.Context, phase HookPhase, cmd string, processed []string, out, errOut io.Writer) error {
	ev := eventName(phase, cmd)

	// Trust gate (bead pg2-oymai): hooks execute `sh -c` from a pn-workspace.toml
	// discovered by walking up from cwd, so an untrusted checkout must not run
	// them. Engage the gate ONLY when a hook actually fires for this
	// (phase, command) — hook-free / non-matching commands incur no friction.
	// --root / PN_WORKSPACE_ROOT do not bypass it (an untrusted dir can also plant
	// an env var). Pre-phase: abort (nothing executed). Post-phase: warn + skip.
	willFire := false
	for _, h := range ws.config.Hooks {
		if slices.Contains(h.When, ev) {
			willFire = true
			break
		}
	}
	if !willFire {
		for _, key := range processed {
			for _, h := range ws.config.Repos[key].Hooks {
				if slices.Contains(h.When, ev) {
					willFire = true
					break
				}
			}
			if willFire {
				break
			}
		}
	}
	if willFire {
		if err := trust.EnsureAllowed(ws.root); err != nil {
			if phase == HookPhasePre {
				return err
			}
			fmt.Fprintf(errOut, "warning: skipping post-%s hooks: %v\n", cmd, err)
			return nil
		}
	}

	// Workspace-scoped: once at root (no {nix_run}; enforced by validateAllHooks).
	for _, h := range ws.config.Hooks {
		if slices.Contains(h.When, ev) {
			if err := RunHooks(ctx, ws.runner, h.Run, ws.root, phase); err != nil {
				return err
			}
		}
	}
	// Repo-scoped: in each processed repo that declares a matching hook. The
	// effective lock backing {nix_run} override injection is derived at most once
	// per call, and only when a matched hook actually carries a {nix_run} token —
	// avoiding O(N^2) nix evals (deriveLock per repo) and spurious
	// "effective lock unavailable" warnings on token-free hooks (bd pg2-4g2h).
	var (
		lk        *Lock
		lockReady bool
	)
	varsCache := map[string]nixHookVars{}
	varsFor := func(key string) nixHookVars {
		if !lockReady {
			var err error
			lk, _, err = ws.effectiveLock(ctx)
			lockReady = true
			if err != nil {
				fmt.Fprintf(errOut, "warning: hook overrides: effective lock unavailable (%v); gates may build against locked inputs\n", err)
			}
		}
		if v, ok := varsCache[key]; ok {
			return v
		}
		v := ws.nixHookVarsForLock(key, lk)
		varsCache[key] = v
		return v
	}
	for _, key := range processed {
		hooks := ws.config.Repos[key].Hooks
		if len(hooks) == 0 {
			continue
		}
		dir := filepath.Join(ws.root, key)
		for _, h := range hooks {
			if !slices.Contains(h.When, ev) {
				continue
			}
			for _, raw := range h.Run {
				// Idempotency gate (bead pg2-19rcj, rekeyed by pg2-pla9d.10):
				// install-pre-commit-hooks only (re)builds the per-clone hook
				// bundle and installs the git hook stubs, so when the bundle is
				// present and current there is nothing for it to do. Skip BEFORE
				// expanding vars/effectiveLock so the dominant cost — nix
				// re-evaluating the whole flake graph because --override-input
				// defeats its eval cache — is never paid on a re-materialization/
				// persistent worktree whose hooks are already current. A fresh
				// worktree (no private bundle) or a genuinely-changed hook set
				// (stale/broken/relocated) falls through and installs normally.
				// See installHookUpToDate.
				m := nixRunTokenRe.FindStringSubmatch(raw)
				isInstall := m != nil && m[1] == installPreCommitHooksAttr
				if isInstall && installHookUpToDate(dir) {
					continue
				}
				var vars nixHookVars
				if nixRunTokenRe.MatchString(raw) {
					vars = varsFor(key)
					// A linked worktree (a set member) never touches the shared
					// bundle: it builds its own private one, recording the set's
					// pins so a later producer change shows up as stale.
					if isInstall && isLinkedWorktree(dir) {
						vars.InstallArgs = privateInstallArgs(vars.OverrideArgs)
					}
				}
				cmdStr, _, err := expandNixRunTokens(raw, vars)
				if err == nil {
					var resolved string
					if resolved, err = rewriteFirstToken(cmdStr, dir); err == nil {
						// Subprocess stdout→out, stderr→errOut (separate writers);
						// no manual res.Stderr re-print (that double-printed).
						_, err = ws.runner.Run(ctx, "sh", []string{"-c", resolved}, exec.RunOptions{Dir: dir, Stdout: out, Stderr: errOut, PropagateTrace: true})
					}
				}
				if err != nil {
					if phase == HookPhasePre {
						return fmt.Errorf("pre-hook %q in %s: %w", raw, key, err)
					}
					fmt.Fprintf(errOut, "warning: post-hook %q in %s: %v\n", raw, key, err)
				}
			}
		}
	}
	return nil
}

// nixHookVarsForLock builds the per-repo {nix_run} expansion values from an
// ALREADY-DERIVED lock. Pure (no I/O, no warnings): RunEventHooks derives the
// effective lock once and warns once on failure, then calls this per repo.
func (ws *Workspace) nixHookVarsForLock(key string, lk *Lock) nixHookVars {
	return nixHookVars{
		NixExe:       "nix",
		OverrideArgs: ws.overrideInputArgsForLock(lk, key, overrideOpts{}),
		FlakeDir:     filepath.Join(ws.root, key, filepath.Dir(ws.resolveFlakePath(key))),
	}
}
