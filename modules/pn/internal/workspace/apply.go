package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ApplyOptions configures Apply.
type ApplyOptions struct {
	Terminal            string            // overrides workspace.terminal for this invocation
	ApplyCmd            string            // overrides apply_command template
	OverridePaths       map[string]string // repo key -> abs path
	ShowNixCommandsOnly bool
	Force               bool // always rebuild (bypass the skip gate)
	// Builder overrides the OS-detected {builder} value (activation tool).
	// Empty falls through to defaultBuilder().
	Builder string
}

// Apply activates the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It checks daemon health, skips the rebuild
// when nothing changed, diffs the system profile via nvd when available, and
// records the applied state. Formatting is a separate step: run
// `pn workspace format` before applying.
func (ws *Workspace) Apply(ctx context.Context, out io.Writer, opts ApplyOptions) error {
	terminal, err := ws.requireTerminal(ctx, opts.Terminal)
	if err != nil {
		return err
	}
	terminalRepoDir := filepath.Join(ws.root, terminal)
	if td, ok := opts.OverridePaths[terminal]; ok {
		terminalRepoDir = td
	}
	nixRel := filepath.Dir(ws.resolveFlakePath(terminal))
	terminalNixDir := filepath.Join(terminalRepoDir, nixRel)

	// ONE resolution, TWO projections: the flags nix is given and the record of what
	// this apply overrode (markApplied's `overridden`). Resolving once is deliberate
	// — the set depends on which clones exist on disk, so a second resolution could
	// sample a different filesystem and record an override nix was never told about,
	// or miss one it was (bead pg2-14yqh).
	resolvedOverrides := ws.resolveOverridesFor(terminal, overrideOpts{OverridePaths: opts.OverridePaths})
	overrides := overrideInputArgs(resolvedOverrides)

	if err := checkFollows(terminalNixDir, ws.workspaceInputNamesFromEdges(terminal)); err != nil {
		return err
	}

	tmpl := opts.ApplyCmd
	if tmpl == "" {
		tmpl, err = ws.config.ApplyCommandTemplate()
		if err != nil {
			return err
		}
	}
	builder := opts.Builder
	if builder == "" {
		builder = defaultBuilder()
	}
	cmdArgs, err := substituteCommand(tmpl, templateVars{
		TerminalRepoDir:    terminalRepoDir,
		TerminalNixDir:     terminalNixDir,
		TerminalNixRelPath: nixRel,
		Hostname:           shortHostname(),
		Builder:            builder,
	})
	if err != nil {
		return err
	}

	if opts.ShowNixCommandsOnly {
		fmt.Fprintln(out, strings.Join(append(append([]string{}, cmdArgs...), overrides...), " "))
		return nil
	}

	// Trust gate (bead pg2-x2q6o): abort before ANY execution (including the nix
	// daemon check and rebuild probe) when the workspace root is untrusted, so
	// the config-sourced apply_command is never run from an untrusted checkout.
	if err := ws.ensureExecTrusted(); err != nil {
		return err
	}

	if id := ws.config.Workspace.Id; id != "" {
		if err := checkWsidUnique(id, ws.root); err != nil {
			return err
		}
	}

	if err := ws.checkNixDaemon(ctx); err != nil {
		return err
	}

	fmt.Fprintf(out, "  --== %s: applying flake ==--  \n", terminal)
	allDirs := ws.allRepoDirs(opts.OverridePaths)
	rebuild, err := ws.needsRebuild(ctx, allDirs, opts.Force, out)
	if err != nil {
		return err
	}
	if !rebuild {
		return nil
	}

	oldProfile := readSystemProfile()
	// Capture the git binary identity before the rebuild so we can tell,
	// afterwards, whether this apply swapped in a new git binary.
	oldGitID := ws.gitBinaryID(ctx)
	// Capture each repo's HEAD as late as possible, immediately before the
	// build invocation below — this is the concrete meaning of "eval-time
	// HEAD": nix resolves each --override-input from the repo's local clone at
	// whatever HEAD that clone has the instant the build reads it, so this is
	// the last moment the recorded value can still match what was actually
	// evaluated. markApplied (after the build) must record THIS map rather
	// than re-reading HEAD itself, or a commit landing during the build
	// window — which can be minutes wide — would be recorded as applied even
	// though the activated system was evaluated before that commit existed
	// (bead pg2-0782j).
	preBuildHeads, err := ws.captureHeads(ctx, allDirs)
	if err != nil {
		return err
	}
	full := append(append([]string{}, cmdArgs[1:]...), overrides...)
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{
		Dir:    terminalRepoDir,
		Stdout: out,
		Stderr: out,
	}); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	newProfile := readSystemProfile()
	if oldProfile != newProfile && newProfile != "" && commandExists("nvd") {
		fmt.Fprintln(out, "  --== Package changes ==--  ")
		_, _ = ws.runner.Run(ctx, "nvd", []string{"diff", oldProfile, newProfile}, exec.RunOptions{Dir: terminalRepoDir, Stdout: out, Stderr: out})
	}

	// If this apply installed a new git, the running `git fsmonitor--daemon`
	// is still executing the OLD git binary and will not auto-restart. Kill it
	// so the next git command spawns a fresh daemon from the new binary.
	if newGitID := ws.gitBinaryID(ctx); oldGitID != "" && newGitID != "" && newGitID != oldGitID {
		ws.restartFsmonitorDaemon(ctx, out)
	}

	// markApplied is reached ONLY here, after a real rebuild — the !rebuild early
	// return above skips it. So a record (its locked_revs and its overridden_inputs)
	// is written exactly when a build actually happened, never on a skipped apply.
	return ws.markApplied(ctx, allDirs, terminal, terminalNixDir, overriddenInputs(resolvedOverrides), preBuildHeads, out)
}

// captureHeads resolves each repo's local HEAD, keyed by [repos.<key>] name, at
// the moment it is called. Apply calls this IMMEDIATELY BEFORE the build
// invocation so the recorded value is eval-time HEAD (see the call site's
// comment); markApplied then consumes this map instead of re-reading HEAD
// itself after the build (bead pg2-0782j). A `git rev-parse HEAD` failure is
// returned as an error, matching how the pre-fix in-loop call in markApplied
// handled it — not silently skipped.
func (ws *Workspace) captureHeads(ctx context.Context, repoDirs []repoDir) (map[string]string, error) {
	heads := make(map[string]string, len(repoDirs))
	for _, rd := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", rd.gitDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return nil, fmt.Errorf("git rev-parse in %s: %w", rd.gitDir, err)
		}
		heads[rd.name] = strings.TrimSpace(string(res.Stdout))
	}
	return heads, nil
}

// gitBinaryID returns a string identifying the installed git *binary*, via the
// trimmed output of `git --exec-path` (which is rooted in git's Nix store path),
// or "" if git is not available. Used to detect whether an apply swapped in a
// new git binary. `git --version` is deliberately NOT used: a same-version Nix
// rebuild swaps the store path while leaving the version string unchanged, so a
// version probe would miss it and the fsmonitor daemon would keep executing the
// stale binary.
func (ws *Workspace) gitBinaryID(ctx context.Context) string {
	res, err := ws.runner.Run(ctx, "git", []string{"--exec-path"}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// restartFsmonitorDaemon terminates any running `git fsmonitor--daemon` so the
// next git command spawns a fresh one from the just-installed git binary. It is
// best-effort: pkill exits non-zero when no daemon is running, which is not an
// error here.
func (ws *Workspace) restartFsmonitorDaemon(ctx context.Context, out io.Writer) {
	fmt.Fprintln(out, "  --== git updated: restarting fsmonitor daemon ==--  ")
	_, _ = ws.runner.Run(ctx, "pkill", []string{"-f", "git fsmonitor--daemon"}, exec.RunOptions{})
}

// stopFsmonitorDaemon best-effort stops the per-worktree `git fsmonitor--daemon`
// for the worktree at dir. Unlike restartFsmonitorDaemon (a blunt pkill of ALL
// daemons after a git-binary swap), this is targeted at one worktree. The daemon
// is keyed by worktree path and is NOT torn down by `git worktree remove`, so
// without this stop each removed worktree orphans a lingering daemon. Call it
// immediately before removing the worktree at dir. Best-effort:
// `fsmonitor--daemon stop` exits non-zero when no daemon is running (or fsmonitor
// is off), which is not an error here.
func (ws *Workspace) stopFsmonitorDaemon(ctx context.Context, dir string) {
	_, _ = ws.runner.Run(ctx, "git", []string{"-C", dir, "fsmonitor--daemon", "stop"}, exec.RunOptions{})
}

// repoDir pairs a repo's canonical applied-state store key (keyPath) with the
// checkout git actually operates on (gitDir). They differ only under an
// override-path apply (coordinated-worktree flow): git reads HEAD/dirtiness
// from gitDir (the applied checkout), but the store is keyed by keyPath (the
// canonical <root>/<name>) so `pn workspace info`, which knows only the
// canonical path, finds the record. Absent overrides the two are identical.
type repoDir struct {
	// name is the [repos.<key>] workspace repo key. It is the identity markApplied
	// maps through the workspace lock's edge set to find the terminal's flake input
	// alias for this repo — the edge set is keyed on the repo key, never on a path,
	// so name is unaffected by an override.
	name    string
	keyPath string // canonical <root>/<name>; the applied-state store key
	gitDir  string // resolved checkout (override path or canonical); git runs here
}

// appliedStateKeyPath is the single, shared rule for deriving a repo's
// applied-state store key: the canonical <root>/<name> path. Both the apply
// path (allRepoDirs → markApplied/needsRebuild) and Info key the store through
// this rule, so an override-path apply and a later `pn workspace info` resolve
// to the same store entry.
func (ws *Workspace) appliedStateKeyPath(name string) string {
	return filepath.Join(ws.root, name)
}

// allRepoDirs returns, for every declared repo that exists on disk, the
// canonical store-key path paired with the checkout git operates on (honoring
// overrides). Missing clones are skipped so the rebuild gate and mark-applied
// don't fail on a repo that hasn't been cloned yet.
func (ws *Workspace) allRepoDirs(overrides map[string]string) []repoDir {
	var dirs []repoDir
	// Alpha (not topoAlpha): callers use the result as a set of paths for
	// existence checks (rebuild gate, mark-applied) — order is not semantic.
	for _, key := range orderedRepoNames(ws.config.Repos) {
		keyPath := ws.appliedStateKeyPath(key)
		gitDir := keyPath
		if ov, ok := overrides[key]; ok {
			gitDir = ov
		}
		if !dirExists(gitDir) {
			continue
		}
		dirs = append(dirs, repoDir{name: key, keyPath: keyPath, gitDir: gitDir})
	}
	return dirs
}

const systemProfileLink = "/nix/var/nix/profiles/system"

// readSystemProfile resolves the current system profile to an absolute store
// path, or "" if it cannot be read.
func readSystemProfile() string {
	target, err := os.Readlink(systemProfileLink)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	return filepath.Join(filepath.Dir(systemProfileLink), target)
}

func commandExists(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}
