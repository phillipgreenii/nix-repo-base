package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// hexFilenameRe matches exactly 64 lowercase hex characters — the form
// produced by appliedStateFile (sha256 of the full repo path).
var hexFilenameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// needsRebuild reports whether apply must rebuild. Returns true if force is set,
// any repo's working tree is dirty, any repo's HEAD differs from the recorded
// applied hash, or any repo has no recorded hash (absent store triggers rebuild).
// A corrupt or unreadable store returns an error (fail-closed) rather than
// triggering a rebuild. Returns false (with a notice) only when every repo is
// clean and unchanged.
//
// The gate compares local HEAD against AppliedState.AppliedRef, and ADR 0025
// deliberately left that comparison alone: it added LockedRevs as a SEPARATE field
// instead of redefining AppliedRef. This is load-bearing. AppliedRef answers "has
// this checkout changed since the last apply", which is a question about the local
// working copy; if it had been redefined to a terminal-locked rev, this comparison
// would pit local HEAD against a rev that normally differs from it, the skip would
// never fire, and every apply would rebuild.
func (ws *Workspace) needsRebuild(ctx context.Context, repoDirs []repoDir, force bool, out io.Writer) (bool, error) {
	if force {
		return true, nil
	}
	for _, rd := range repoDirs {
		porcelain, err := ws.gitStatusPorcelain(ctx, rd.gitDir)
		if err != nil {
			return false, fmt.Errorf("git status in %s: %w", rd.gitDir, err)
		}
		if porcelain != "" {
			return true, nil
		}
		res, err := ws.runner.Run(ctx, "git", []string{"-C", rd.gitDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return false, fmt.Errorf("git rev-parse in %s: %w", rd.gitDir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		// Key the store by the canonical path so the rebuild-skip check reads
		// the same entry markApplied wrote and Info reads (shared key rule).
		st, ok, err := readAppliedState(rd.keyPath)
		if err != nil {
			return false, fmt.Errorf("read applied-state for %s: %w", rd.keyPath, err)
		}
		if !ok || head != st.AppliedRef {
			return true, nil
		}
	}
	fmt.Fprintln(out, "Skipping rebuild: all workspace repos clean and unchanged since last apply")
	return false, nil
}

// gitStatusProbeTimeout bounds apply's `git status` dirtiness probes. Disabling
// the filesystem monitor (see gitStatusPorcelain) already removes the wedged
// `git fsmonitor--daemon` hang that motivated bead pg2-0sa8p; this timeout is
// defense-in-depth against any OTHER stall (e.g. a slow or contended index),
// turning an unbounded hang into a clear, actionable error. It is deliberately
// generous: an fsmonitor-off `git status` on these repos completes well under a
// second, so this leaves large headroom and will not false-trip on a cold cache
// or a loaded machine.
const gitStatusProbeTimeout = 60 * time.Second

// gitStatusPorcelain runs `git status --porcelain` in dir for apply's dirtiness
// probes (needsRebuild, markApplied) and returns the trimmed output. It disables
// the filesystem monitor PER-INVOCATION (`-c core.fsmonitor=false`) so the probe
// never queries a `git fsmonitor--daemon` — a wedged daemon would otherwise block
// the status read forever on `.git/fsmonitor--daemon.ipc` and hang apply
// (bead pg2-0sa8p). Per-command config is used deliberately over the update-locks
// approach of writing core.fsmonitor=false into shared .git/config with an
// EXIT-trap restore: it mutates no shared state (safe under concurrent worktrees,
// per ADR 0009) and cannot leave fsmonitor disabled if apply dies mid-run. A
// bounded context guards against non-fsmonitor stalls (gitStatusProbeTimeout).
func (ws *Workspace) gitStatusPorcelain(ctx context.Context, dir string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, gitStatusProbeTimeout)
	defer cancel()
	res, err := ws.runner.Run(tctx, "git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.RunOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// markApplied records each repo's current HEAD (and dirty flag) into the
// authoritative applied-state store, TOGETHER WITH the terminal's locked revs as
// they stood for THIS apply. Written only after a successful apply.
//
// git reads HEAD/dirtiness from the applied checkout (gitDir), but the store is
// keyed by the canonical path (keyPath) — the same key Info reads — so an
// override-path apply is discoverable by `pn workspace info`. terminalNixDir is
// the directory holding the terminal's flake.nix/flake.lock for THIS apply, so an
// override-path apply reads the lock it actually ran against.
//
// AppliedRef stays exactly what it always was (this checkout's local HEAD): it is
// the evidence that an apply RAN. What it does NOT prove is that the applied
// system CONTAINS that commit — for a repo the terminal consumes as a flake input
// (a `github:` pin), the commit reaches the built system only once it is pushed and
// the terminal relocked, so a commit landed on local main resolved a `pn:applied`
// gate against code no build had ever seen (bead pg2-ft60a, which released the
// gated verification bead pg2-c40r4).
//
// The remedy is the SECOND, independent fact recorded here: LockedRevs, the rev the
// terminal's flake.lock pinned for each of its workspace flake inputs at this
// apply. A consumer requires BOTH — an apply happened (AppliedRef's range contains
// the patch) AND that apply's lock contained the commit. Recording the lock WITH
// the apply, rather than re-reading it at query time, is what closes the ordering
// hole: an apply at T1 followed by a relock at T2 > T1 must not read as though the
// T1 build contained code only the T2 lock names.
//
// The same map is written into EVERY repo's record: it describes the apply, not one
// repo, and each record is therefore self-contained evidence about the build that
// produced it. A repo with no entry (the terminal itself, or a repo no terminal
// input names) is a legitimate SKIP for the lock condition; an entry with an EMPTY
// rev is a FAIL-CLOSED marker and is announced on out, because a silent unprovable
// apply is the failure mode this bead is about.
//
// overridden is the THIRD fact, and the reason it is a PARAMETER rather than
// recomputed here: it must be the very override set Apply emitted as
// `--override-input` flags, captured from the same resolution (bead pg2-14yqh).
// Recomputing it after the build would resample dirExists and could disagree with
// what nix was actually told. A repo listed there was built from its LOCAL clone at
// eval-time HEAD, so its LockedRevs entry is NOT what the build carries and a
// consumer must not test against it; see AppliedState.OverriddenInputs. It is
// written into every record for the same reason LockedRevs is.
func (ws *Workspace) markApplied(ctx context.Context, repoDirs []repoDir, terminal, terminalNixDir string, overridden map[string]string, out io.Writer) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tl, lockErr := ws.terminalLockedRevs(terminal, terminalNixDir)
	if lockErr != nil {
		fmt.Fprintf(out, "pn: warn: applied-state: cannot read terminal %q flake.lock in %s: %v\n",
			terminal, terminalNixDir, lockErr)
	}
	for _, rd := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", rd.gitDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git rev-parse in %s: %w", rd.gitDir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		porcelain, err := ws.gitStatusPorcelain(ctx, rd.gitDir)
		if err != nil {
			return fmt.Errorf("git status in %s: %w", rd.gitDir, err)
		}
		dirty := porcelain != ""
		_, wasOverridden := overridden[rd.name]
		// The unresolvable-rev warning is conditioned on NOT having overridden the
		// repo, because the consequence it announces only exists for a lock-built
		// input. For an overridden repo the lock rev is not what the build carries at
		// all, so its gate does NOT stay blocked and warning here would be a false
		// alarm on every apply — which is what trains an operator to ignore the
		// warning (agent-support ADR 0046's "a blocked gate says why" reasoning).
		if rev, isInput := tl.Revs[rd.name]; isInput && rev == "" && !wasOverridden {
			fmt.Fprintf(out, "pn: warn: applied-state: %s: terminal %q declares it as flake input %q but its "+
				"flake.lock pins no rev for it; locked_revs[%s] is left empty, so a pn:applied gate on %s "+
				"stays blocked (fail closed)\n", rd.name, terminal, tl.Aliases[rd.name], rd.name, rd.name)
		}
		if err := writeAppliedState(rd.keyPath, AppliedState{
			Schema:           appliedStateSchema,
			AppliedRef:       head,
			LockedRevs:       tl.Revs,
			OverriddenInputs: overridden,
			Dirty:            dirty,
			AppliedAt:        now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// terminalLock is what the TERMINAL's flake.lock said, at one apply, about the
// workspace repos the terminal consumes as flake inputs. Aliases and Revs share
// one key set: the repos that ARE terminal flake inputs.
type terminalLock struct {
	// Aliases maps workspace repo key -> the flake input name the terminal uses
	// for it. Kept alongside Revs so a fail-closed warning can name the input the
	// operator has to look at.
	Aliases map[string]string
	// Revs maps workspace repo key -> the rev the terminal's flake.lock pinned,
	// or "" when no rev could be resolved for that alias. See AppliedState.LockedRevs
	// for how the three states (absent / non-empty / empty) must be read.
	Revs map[string]string
}

// terminalLockedRevs resolves, for each workspace repo the TERMINAL declares as a
// flake input, the rev the terminal's flake.lock pins for it.
//
// The repo -> lock-node mapping composes two mechanisms that already exist rather
// than pattern-matching flake.lock node keys or their locked.repo/locked.owner
// fields:
//
//   - ws.lock.Edges already maps (consumer, alias) -> target repo, and those edges
//     were derived by matching canonicalURL(flake input URL) against
//     canonicalURL(the repo's configured remote). The canonical form is
//     host/owner/repo, so the OWNER is inherently part of the match and two
//     same-named repos under different owners cannot be crossed (this workspace has
//     exactly that shape). buildEdges already rejects a genuinely ambiguous config
//     at lock time, so no ambiguity survives to here.
//   - alias -> rev then goes through readAliasRevs, which walks
//     root.inputs[alias] -> node key -> nodes[key].locked.rev exactly as nix
//     resolves it (the same path checkFollows and tree.go use). Node KEYS are
//     unusable as identities: they neither match the workspace repo key
//     (`phillipgreenii-nix-base` is the node for repo `phillipg-nix-repo-base`) nor
//     stay stable (nix appends `_2`/`_3` to disambiguate), so they are never
//     matched against.
//
// It reads ws.lock — the SAME edge set apply derived its --override-input flags
// from (see overrideInputArgsForLock) — so the recorded state describes the build
// that just ran. It deliberately does NOT fall back to effectiveLock's nix-eval
// derivation: markApplied runs AFTER a successful apply, where a fresh nix fan-out
// could fail and turn a good apply into an error.
//
// Every repo with an edge gets a key, INCLUDING when its rev cannot be resolved —
// the key set is the claim "the terminal consumes these repos as flake inputs", and
// dropping an unresolvable one would downgrade a fail-closed state into an
// indistinguishable "not an input" skip. An unreadable terminal flake.lock
// therefore yields every input keyed to "" plus the error; an edgeless workspace
// lock yields an EMPTY key set, which correctly says "the terminal has no workspace
// flake inputs" — the same edge set apply passes as overrides, so an edgeless lock
// is also an apply that overrode nothing.
func (ws *Workspace) terminalLockedRevs(terminal, terminalNixDir string) (terminalLock, error) {
	tl := terminalLock{Aliases: map[string]string{}, Revs: map[string]string{}}
	if ws == nil || ws.lock == nil {
		return tl, nil
	}
	for _, e := range ws.lock.Edges {
		if e.Consumer == terminal {
			tl.Aliases[e.Target] = e.Alias
			tl.Revs[e.Target] = ""
		}
	}
	if len(tl.Aliases) == 0 {
		return tl, nil
	}
	names := make([]string, 0, len(tl.Aliases))
	for _, alias := range tl.Aliases {
		names = append(names, alias)
	}
	sort.Strings(names) // deterministic; readAliasRevs is order-insensitive
	byAlias, err := readAliasRevs(filepath.Join(terminalNixDir, "flake.lock"), names)
	if err != nil {
		return tl, err
	}
	for repo, alias := range tl.Aliases {
		if rev := byAlias[alias]; rev != "" {
			tl.Revs[repo] = rev
		}
	}
	return tl, nil
}

// checkNixDaemon probes daemon responsiveness with a 10s-bounded `nix eval`. On
// failure it returns an actionable error (the interactive restart prompt from
// the bash version is intentionally omitted).
func (ws *Workspace) checkNixDaemon(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ws.runner.Run(tctx, "nix", []string{"eval", "--expr", "true"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("nix daemon health check failed: %w\n  Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon", err)
	}
	return nil
}
