package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
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
	// Wait is how long to wait for a contended single-flight lock; 0 fails fast,
	// WaitForever waits indefinitely (see rebuildlock.go).
	Wait time.Duration
}

// Apply activates the terminal flake, injecting --override-input for
// every non-terminal workspace repo. It checks daemon health, skips the rebuild
// when nothing changed, diffs the system profile via nvd when available, and
// records the applied state. Formatting is a separate step: run
// `pn workspace format` before applying.
//
// Apply runs the same shared prelude as Build (Workspace.rebuild); only the
// strategy after the prelude is apply-specific (applyVerb.run).
func (ws *Workspace) Apply(ctx context.Context, out io.Writer, opts ApplyOptions) error {
	return ws.rebuild(ctx, out, applyVerb{}, rebuildOptions{
		Terminal:            opts.Terminal,
		Cmd:                 opts.ApplyCmd,
		OverridePaths:       opts.OverridePaths,
		ShowNixCommandsOnly: opts.ShowNixCommandsOnly,
		Force:               opts.Force,
		Builder:             opts.Builder,
		Wait:                opts.Wait,
	})
}

// applyVerb is the apply strategy of the rebuild Template Method.
type applyVerb struct{}

func (applyVerb) name() string { return "apply" }

func (applyVerb) commandTemplate(ws *Workspace, opts rebuildOptions) (string, error) {
	if opts.Cmd != "" {
		return opts.Cmd, nil
	}
	tmpl, err := ws.config.ApplyCommandTemplate()
	if err != nil && ws.config.ActivateCommandTemplate() != "" {
		// The build-as-user flow (activate_command) needs no apply_command unless
		// it has to fall back; extendPlan reports the missing fallback if it does.
		return ws.config.BuildCommandTemplate(), nil
	}
	return tmpl, err
}

func (applyVerb) run(ctx context.Context, ws *Workspace, out io.Writer, p *rebuildPlan) (err error) {
	terminal, terminalRepoDir, terminalNixDir := p.terminal, p.terminalRepoDir, p.terminalNixDir
	fmt.Fprintf(out, "  --== %s: applying flake ==--  \n", terminal)
	allDirs := ws.allRepoDirs(p.opts.OverridePaths)
	ws.recordStep("apply", false, stepSkipGate)
	reason, err := ws.needsRebuild(ctx, allDirs, p.opts.Force)
	if err != nil {
		return err
	}
	telemetry.RecordApplyDecision(ctx, reason.rebuild(), string(reason))
	if reason == reasonUnchanged {
		fmt.Fprintln(out, "Skipping rebuild: all workspace repos clean and unchanged since last apply")
		return nil
	}

	// From here on a real rebuild is happening: retain everything the operator
	// sees in the apply log (bead pg2-xdvbj; see applylog.go). `out` is rebound to
	// tee into it, so the terminal output is unchanged. A failure to open the log
	// is reported and otherwise ignored: it must never fail or alter the apply.
	ws.recordStep("apply", false, stepApplyLog)
	var extraHeader []string
	if p.asUser != nil {
		extraHeader = p.asUser.describeSteps(p)
	}
	alog, logErr := openRunLog(runLogOpts{
		Verb:     "apply",
		Terminal: terminal,
		Cmdline:  append(append([]string{}, p.cmdArgs...), p.overrideArgs...),
		Extra:    extraHeader,
		Begin:    p.begin,
		TraceID:  telemetry.TraceIDFromContext(ctx),
		Reason:   string(reason),
	})
	if logErr != nil {
		fmt.Fprintf(out, "warning: could not retain the apply log: %v\n", logErr)
	}
	p.alog = alog
	out = alog.tee(out)
	defer func() { alog.finish(err) }()

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
	ws.recordStep("apply", false, stepCaptureHeads)
	preBuildHeads, err := ws.captureHeads(ctx, allDirs)
	if err != nil {
		alog.recordRefs(nil)
		return err
	}
	alog.recordRefs(preBuildHeads)
	ws.recordStep("apply", false, stepRunCommand)
	alog.phase(phaseBuild)
	if p.asUser != nil {
		// Build-as-user flow (activate_command set, preflight passed): apply_asuser.go.
		if err := ws.applyAsUser(ctx, out, alog, p); err != nil {
			return err
		}
	} else {
		if runErr := ws.runPlanned(ctx, out, "apply", p); runErr != nil {
			alog.setExit(runErr)
			return fmt.Errorf("apply failed: %w", runErr)
		}
		alog.setExit(nil)
	}
	ws.recordStep("apply", false, stepPostSteps)
	alog.phase(phasePostSteps)
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

	// markApplied is reached ONLY here, after a real rebuild — the unchanged early
	// return above skips it. So a record (its locked_revs and its overridden_inputs)
	// is written exactly when a build actually happened, never on a skipped apply.
	ws.recordStep("apply", false, stepMarkApplied)
	return ws.markApplied(ctx, allDirs, terminal, terminalNixDir, overriddenInputs(p.resolvedOverrides), preBuildHeads, out)
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
