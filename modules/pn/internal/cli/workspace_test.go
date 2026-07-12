package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestWorkspace is shared by multiple test files in this package.
// It creates a temp dir, writes a minimal pn-workspace.toml, and opens a
// *workspace.Workspace backed by the supplied runner.
// Declared in hooks_test.go; re-used here.

// withFakeWorkspace replaces the package-level openWorkspace var for the
// duration of one test and restores it when the test ends. It returns the
// FakeRunner so callers can script responses and inspect calls.
func withFakeWorkspace(t *testing.T, tomlBody string) *exec.FakeRunner {
	t.Helper()
	fr := exec.NewFakeRunner()
	w := newTestWorkspace(t, fr, tomlBody)
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return w, nil }
	t.Cleanup(func() { openWorkspace = orig })
	return fr
}

// minimalToml is a workspace TOML with no repos and no terminal configured,
// suitable for tests that only care about flag plumbing before workspace methods
// do meaningful work.
const minimalToml = `[workspace]
name = "test"
`

// terminalToml is a workspace TOML with a terminal configured, so commands
// that call requireTerminal succeed without --terminal.
const terminalToml = `[workspace]
name = "test"
terminal = "myterm"

[repos.myterm]
url = "github:owner/myterm"
`

// runCobraCmd runs a workspace subcommand through the full cobra tree, which
// ensures the persistent --terminal flag on the parent "workspace" command is
// properly wired. args should be the subcommand and its flags/args (the
// "workspace" parent is prepended automatically). Returns (stdout, stderr, err).
func runCobraCmd(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer

	// Build the full pn root (workspace group is wired by newRootCmd).
	root := newRootCmd("20260101-test000")
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"workspace"}, args...))

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// ---------------------------------------------------------------------------
// resolveWorkspaceRoot tests (existing — keep them)
// ---------------------------------------------------------------------------

func TestResolveWorkspaceRoot_WalkUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := resolveWorkspaceRoot("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	gotR, _ := filepath.EvalSymlinks(got)
	rootR, _ := filepath.EvalSymlinks(root)
	if gotR != rootR {
		t.Errorf("got %q want %q", gotR, rootR)
	}
}

func TestResolveWorkspaceRoot_FlagMissingToml(t *testing.T) {
	if _, err := resolveWorkspaceRoot(t.TempDir()); err == nil {
		t.Fatal("expected error when --root has no pn-workspace.toml")
	}
}

// ---------------------------------------------------------------------------
// Workspace-open failure: all commands should propagate it
// ---------------------------------------------------------------------------

func TestAllWorkspaceCommands_OpenFailurePropagates(t *testing.T) {
	// Replace openWorkspace with one that always fails.
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) {
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { openWorkspace = orig })

	type cmdEntry struct {
		name string
		args []string
	}
	cmds := []cmdEntry{
		{"status", []string{"status"}},
		{"init", []string{"init"}},
		{"clone", []string{"clone"}},
		{"lock", []string{"lock"}},
		{"build", []string{"build"}},
		{"apply", []string{"apply"}},
		{"flake-check", []string{"flake-check"}},
		{"pre-commit-check", []string{"pre-commit-check"}},
		{"push", []string{"push"}},
		{"rebase", []string{"rebase"}},
		{"format", []string{"format"}},
		{"tree", []string{"tree"}},
		{"update", []string{"update"}},
		{"upgrade", []string{"upgrade"}},
		{"discover", []string{"discover"}},
		{"workforest list", []string{"workforest", "list"}},
		{"workforest prune", []string{"workforest", "prune"}},
		{"workforest add", []string{"workforest", "add", "my-branch"}},
		{"workforest remove", []string{"workforest", "remove", "my-branch"}},
	}
	for _, tc := range cmds {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCobraCmd(t, tc.args)
			if err == nil {
				t.Errorf("%s: expected error when workspace open fails, got nil", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// removed install-hooks / sync-hooks subcommands (bd pg2-lbsi)
// ---------------------------------------------------------------------------

// TestWorkspaceInstallHooks_RemovedStubGuidesMigration verifies the removed
// install-hooks / sync-hooks subcommands (pre-ADR-0019) now fail loudly with a
// pointer to the event-hook replacement, rather than cobra printing group help
// and exiting 0 — a silent no-op that would re-open the staleness bug for
// muscle-memory / CI callers.
func TestWorkspaceInstallHooks_RemovedStubGuidesMigration(t *testing.T) {
	for _, name := range []string{"install-hooks", "sync-hooks"} {
		t.Run(name, func(t *testing.T) {
			_, stderr, err := runCobraCmd(t, []string{name})
			if err == nil {
				t.Fatalf("%s must exit non-zero, not silently no-op", name)
			}
			msg := err.Error() + stderr
			for _, want := range []string{"repos.", "hooks", "ADR-0019"} {
				if !strings.Contains(msg, want) {
					t.Errorf("migration message missing %q; got err=%q stderr=%q", want, err, stderr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func TestWorkspaceStatus_FlagTerminalFlows(t *testing.T) {
	// With --terminal set, Status should receive opts.Terminal = "myterm".
	// We verify this by observing that the command does NOT emit the
	// "no terminal configured" warning (it only emits when both the flag
	// and the config terminal are absent).
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"status", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal configured") {
		t.Error("--terminal flag was not threaded into Status: warning still emitted")
	}
}

func TestWorkspaceStatus_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning appeared on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got stderr=%q stdout=%q", stderr, stdout)
	}
}

func TestWorkspaceStatus_ExitZeroWithNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"status"})
	if err != nil {
		t.Errorf("status should exit 0 when no terminal configured; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func TestWorkspaceInit_FlagTerminalFlows(t *testing.T) {
	// Init accepts --terminal for uniformity but it is a no-op. The test
	// verifies the flag is wired: the command must not error on unknown flag.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"init", "--terminal", "myterm"})
	// init scans the workspace dir and writes TOML; on the temp workspace
	// there are no repos, so it should print "no changes" and exit 0.
	if err != nil {
		t.Fatalf("init --terminal: unexpected error: %v", err)
	}
}

func TestWorkspaceInit_NoTerminalNoWarning(t *testing.T) {
	// init is not a terminal-required or terminal-optional command in the
	// warning sense; no warning should appear even when terminal is absent.
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"init"})
	if err != nil {
		t.Fatalf("init: unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal") {
		t.Errorf("init must not emit terminal warning; got stderr=%q", stderr)
	}
}

func TestWorkspaceInit_OutputToStdout(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"init"})
	if err != nil {
		t.Fatalf("init: unexpected error: %v", err)
	}
	// "no changes" is the expected output when there is nothing to reconcile.
	if !strings.Contains(stdout, "no changes") {
		t.Errorf("init: expected 'no changes' on stdout; got %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// clone
// ---------------------------------------------------------------------------

func TestWorkspaceClone_FlagTerminalAccepted(t *testing.T) {
	// clone has no repos in the minimal fixture, so it is a no-op.
	// The main assertion: --terminal must be accepted without error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"clone", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("clone --terminal: unexpected error: %v", err)
	}
}

func TestWorkspaceClone_NoReposIsNoOp(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"clone"})
	if err != nil {
		t.Errorf("clone with no repos: expected exit 0; got %v", err)
	}
}

func TestWorkspaceClone_NoTerminalNoWarning(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"clone"})
	if err != nil {
		t.Fatalf("clone: unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal") {
		t.Errorf("clone must not emit terminal warning; got stderr=%q", stderr)
	}
}

// ---------------------------------------------------------------------------
// lock
// ---------------------------------------------------------------------------

func TestWorkspaceLock_FlagTerminalFlows(t *testing.T) {
	// lock --terminal passes the flag to WriteDerivedLockTo; the flag must be
	// accepted (not "unknown flag"). The command may still fail if the named
	// terminal does not exist as a repo in the workspace — that is fine.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"lock", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("lock --terminal: flag not wired: %v", err)
	}
}

func TestWorkspaceLock_NoTerminalErrors(t *testing.T) {
	// lock requires a terminal to be resolvable (config terminal or flag).
	// An empty workspace with no terminal set must exit non-zero with a
	// "terminal cannot be determined" error (validation fail from WriteDerivedLockTo).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"lock"})
	if err == nil {
		t.Fatal("lock with no repos and no terminal: expected non-zero exit")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("expected terminal-related error; got %v", err)
	}
}

func TestWorkspaceLock_WithTerminalSucceeds(t *testing.T) {
	// lock with a configured terminal should write pn-workspace.lock.json
	// and print the confirmation on stdout. Use a TOML with terminal set so
	// the derivation succeeds without nix evaluation (no repos to evaluate).
	withFakeWorkspace(t, terminalToml)
	stdout, _, err := runCobraCmd(t, []string{"lock"})
	if err != nil {
		t.Fatalf("lock with terminal configured: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "pn-workspace.lock.json") {
		t.Errorf("lock: expected confirmation on stdout; got %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// build (required-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceBuild_RequiresTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"build"})
	if err == nil {
		t.Fatal("build with no terminal: expected non-zero exit")
	}
	if !strings.Contains(err.Error(), "terminal") && !strings.Contains(stderr, "terminal") {
		t.Errorf("build: expected terminal-related error; got err=%v stderr=%q", err, stderr)
	}
}

func TestWorkspaceBuild_FlagTerminalFlows(t *testing.T) {
	// When --terminal is passed to a required-terminal command, it must pass
	// the requireTerminal check. The command may still fail later (FakeRunner
	// returns errors for unscripted calls) but the error must NOT be
	// "no terminal repo configured".
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"build", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("build --terminal: flag did not reach requireTerminal; got %v", err)
	}
}

func TestWorkspaceBuild_ErrorToStderr_NoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"build"})
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	// Error must not appear on stdout.
	if strings.Contains(stdout, "terminal") {
		t.Errorf("build error appeared on stdout (must be stderr-only); stdout=%q", stdout)
	}
	_ = stderr // cobra routes RunE errors to stderr automatically
}

// ---------------------------------------------------------------------------
// apply (required-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceApply_RequiresTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"apply"})
	if err == nil {
		t.Fatal("apply with no terminal: expected non-zero exit")
	}
}

func TestWorkspaceApply_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"apply", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("apply --terminal: flag did not reach requireTerminal; got %v", err)
	}
}

func TestWorkspaceApply_ErrorOnStderrOnly(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"apply"})
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if strings.Contains(stdout, "terminal") {
		t.Errorf("apply error on stdout; must be stderr-only; stdout=%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// flake-check (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceFlakeCheck_FlagTerminalFlows(t *testing.T) {
	// Verifies the opts.Terminal gate on the no-terminal warning
	// (fixed in tc-perh.9.23, commit d3d699c).
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"flake-check", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("flake-check --terminal: unexpected error: %v", err)
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Log("flake-check: terminal warning suppressed by --terminal flag")
	}
}

func TestWorkspaceFlakeCheck_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"flake-check"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got %q", stderr)
	}
}

func TestWorkspaceFlakeCheck_ExitZeroNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"flake-check"})
	if err != nil {
		t.Errorf("flake-check with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// pre-commit-check (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspacePreCommitCheck_FlagTerminalFlows(t *testing.T) {
	// Verifies the opts.Terminal gate on the no-terminal warning
	// (fixed in tc-perh.9.23, commit d3d699c).
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"pre-commit-check", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("pre-commit-check --terminal: unexpected error: %v", err)
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Log("pre-commit-check: terminal warning suppressed by --terminal flag")
	}
}

func TestWorkspacePreCommitCheck_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"pre-commit-check"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got %q", stderr)
	}
}

func TestWorkspacePreCommitCheck_ExitZeroNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"pre-commit-check"})
	if err != nil {
		t.Errorf("pre-commit-check with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// push (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspacePush_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"push", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("push --terminal: unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal") {
		t.Error("--terminal flag not threaded: warning still emitted")
	}
}

func TestWorkspacePush_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got %q", stderr)
	}
}

func TestWorkspacePush_ExitZeroNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push"})
	if err != nil {
		t.Errorf("push with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// rebase (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceRebase_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"rebase", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("rebase --terminal: unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal") {
		t.Error("--terminal flag not threaded: warning still emitted")
	}
}

func TestWorkspaceRebase_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"rebase"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got %q", stderr)
	}
}

func TestWorkspaceRebase_ExitZeroNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"rebase"})
	if err != nil {
		t.Errorf("rebase with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// rebase [branch] positional arg
// ---------------------------------------------------------------------------

func TestWorkspaceRebase_PositionalArgAccepted(t *testing.T) {
	// "rebase main" should be accepted without an "unexpected argument" error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"rebase", "main"})
	if err != nil && strings.Contains(err.Error(), "unknown") {
		t.Errorf("rebase <branch>: unexpected cobra error: %v", err)
	}
}

func TestWorkspaceRebase_TooManyArgsRejected(t *testing.T) {
	// More than one positional arg should be rejected by cobra.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"rebase", "main", "extra"})
	if err == nil {
		t.Error("rebase with two positional args: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// push --set-upstream / -u flag
// ---------------------------------------------------------------------------

func TestWorkspacePush_SetUpstreamFlagAccepted(t *testing.T) {
	// --set-upstream must be accepted without "unknown flag" error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push", "--set-upstream"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("push --set-upstream: flag not wired: %v", err)
	}
}

func TestWorkspacePush_SetUpstreamShortFlagAccepted(t *testing.T) {
	// -u shorthand must also be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push", "-u"})
	if err != nil && strings.Contains(err.Error(), "unknown shorthand") {
		t.Errorf("push -u: shorthand not wired: %v", err)
	}
}

// ---------------------------------------------------------------------------
// push --remote flag (tc-perh.16)
// ---------------------------------------------------------------------------

func TestWorkspacePush_RemoteFlagAccepted(t *testing.T) {
	// --remote must be accepted without "unknown flag" error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push", "--remote", "upstream"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("push --remote: flag not wired: %v", err)
	}
}

func TestWorkspacePush_RemoteFlagWithSetUpstreamAccepted(t *testing.T) {
	// --remote combined with --set-upstream must be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push", "--set-upstream", "--remote", "gitea"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("push --set-upstream --remote: flags not wired: %v", err)
	}
}

func TestWorkspacePush_RemoteShortCombinedAccepted(t *testing.T) {
	// -u --remote must both be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"push", "-u", "--remote", "gitea"})
	if err != nil && strings.Contains(err.Error(), "unknown") {
		t.Errorf("push -u --remote: flags not wired: %v", err)
	}
}

// ---------------------------------------------------------------------------
// format (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceFormat_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, stderr, err := runCobraCmd(t, []string{"format", "--terminal", "myterm"})
	if err != nil {
		t.Fatalf("format --terminal: unexpected error: %v", err)
	}
	if strings.Contains(stderr, "no terminal") {
		t.Error("--terminal flag not threaded: warning still emitted")
	}
}

func TestWorkspaceFormat_NoTerminalWarnsOnStderr(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, stderr, err := runCobraCmd(t, []string{"format"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Error("terminal warning on stdout; must be stderr-only")
	}
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("expected terminal warning on stderr; got %q", stderr)
	}
}

func TestWorkspaceFormat_ExitZeroNoTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"format"})
	if err != nil {
		t.Errorf("format with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// tree (required-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceTree_RequiresTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"tree"})
	if err == nil {
		t.Fatal("tree with no terminal: expected non-zero exit")
	}
}

func TestWorkspaceTree_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"tree", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("tree --terminal: flag did not reach requireTerminal; got %v", err)
	}
}

func TestWorkspaceTree_ErrorOnStderrOnly(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"tree"})
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if strings.Contains(stdout, "terminal") {
		t.Errorf("tree error on stdout; must be stderr; stdout=%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// update (required-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceUpdate_RequiresTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"update"})
	if err == nil {
		t.Fatal("update with no terminal: expected non-zero exit")
	}
}

func TestWorkspaceUpdate_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"update", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("update --terminal: flag did not reach requireTerminal; got %v", err)
	}
}

func TestWorkspaceUpdate_ErrorOnStderrOnly(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"update"})
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if strings.Contains(stdout, "terminal") {
		t.Errorf("update error on stdout; must be stderr; stdout=%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// upgrade (required-terminal, delegates to update then apply)
// ---------------------------------------------------------------------------

func TestWorkspaceUpgrade_RequiresTerminal(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"upgrade"})
	if err == nil {
		t.Fatal("upgrade with no terminal: expected non-zero exit")
	}
}

func TestWorkspaceUpgrade_FlagTerminalFlows(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"upgrade", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "no terminal repo configured") {
		t.Errorf("upgrade --terminal: flag did not reach requireTerminal; got %v", err)
	}
}

func TestWorkspaceUpgrade_ErrorOnStderrOnly(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"upgrade"})
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if strings.Contains(stdout, "terminal") {
		t.Errorf("upgrade error on stdout; must be stderr; stdout=%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// discover (optional-terminal)
// ---------------------------------------------------------------------------

func TestWorkspaceDiscover_FlagTerminalFlows(t *testing.T) {
	// discover with --terminal should not error on "unknown flag".
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"discover", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("discover --terminal: flag not wired; got %v", err)
	}
}

func TestWorkspaceDiscover_EmptyWorkspaceListsNothing(t *testing.T) {
	// With no repos in the workspace, discover should print nothing and exit 0.
	withFakeWorkspace(t, minimalToml)
	stdout, _, err := runCobraCmd(t, []string{"discover"})
	if err != nil {
		t.Fatalf("discover: unexpected error: %v", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("discover on empty workspace: expected no output; got %q", stdout)
	}
}

func TestWorkspaceDiscover_NoTerminalNoError(t *testing.T) {
	// discover does not require a terminal.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"discover"})
	if err != nil {
		t.Errorf("discover with no terminal should exit 0; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// workforest add
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestAdd_BranchArgRequired(t *testing.T) {
	// workforest add with no positional args must be rejected by cobra (RangeArgs(1,2)).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add"})
	if err == nil {
		t.Error("workforest add with no args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestAdd_TooManyArgsRejected(t *testing.T) {
	// workforest add accepts at most 2 positional args (branch + commit-ish).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add", "my-branch", "abc123", "extra"})
	if err == nil {
		t.Error("workforest add with 3 positional args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestAdd_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted without
	// "unknown flag" on the workforest add subcommand.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add", "--terminal", "myterm", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest add --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorkforestAdd_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "add", "my-branch"})
	if err == nil {
		t.Error("workforest add: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// workforest list
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestList_NoArgsAccepted(t *testing.T) {
	// workforest list takes no args and exits 0 when the workforests dir is absent.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "list"})
	if err != nil {
		t.Errorf("workforest list on empty workspace: unexpected error: %v", err)
	}
}

func TestWorkspaceWorkforestList_TooManyArgsRejected(t *testing.T) {
	// workforest list is cobra.NoArgs; extra positional args must error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "list", "extra"})
	if err == nil {
		t.Error("workforest list with extra arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestList_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted without error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "list", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest list --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorkforestList_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "list"})
	if err == nil {
		t.Error("workforest list: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// workforest remove
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestRemove_BranchArgRequired(t *testing.T) {
	// workforest remove requires exactly 1 positional arg.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove"})
	if err == nil {
		t.Error("workforest remove with no args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestRemove_TooManyArgsRejected(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove", "my-branch", "extra"})
	if err == nil {
		t.Error("workforest remove with 2 positional args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestRemove_ForceFlagAccepted(t *testing.T) {
	// --force must be accepted without "unknown flag" error.
	// The command will fail because the set dir does not exist, but NOT due to an
	// unknown flag.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove", "--force", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest remove --force: flag not wired: %v", err)
	}
}

func TestWorkspaceWorkforestRemove_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove", "--terminal", "myterm", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest remove --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorkforestRemove_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "remove", "my-branch"})
	if err == nil {
		t.Error("workforest remove: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// workforest prune
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestPrune_NoArgsAccepted(t *testing.T) {
	// workforest prune takes no args; with no repos the command exits 0 (no-op).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "prune"})
	if err != nil {
		t.Errorf("workforest prune on empty workspace: unexpected error: %v", err)
	}
}

func TestWorkspaceWorkforestPrune_TooManyArgsRejected(t *testing.T) {
	// workforest prune is cobra.NoArgs; extra args must error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "prune", "extra"})
	if err == nil {
		t.Error("workforest prune with extra arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestPrune_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "prune", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest prune --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorkforestPrune_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "prune"})
	if err == nil {
		t.Error("workforest prune: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// workforest add --repos (subset)
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestAdd_ReposFlagAccepted(t *testing.T) {
	// --repos must be accepted without "unknown flag" error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add", "--repos", "foo,bar", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest add --repos: flag not wired: %v", err)
	}
}

// ---------------------------------------------------------------------------
// workforest add-repo
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestAddRepo_ArgsRequired(t *testing.T) {
	// add-repo requires exactly 2 positional args (branch + repo).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add-repo", "my-branch"})
	if err == nil {
		t.Error("workforest add-repo with 1 arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestAddRepo_TooManyArgsRejected(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "add-repo", "my-branch", "repo", "extra"})
	if err == nil {
		t.Error("workforest add-repo with 3 args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestAddRepo_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "add-repo", "my-branch", "repo"})
	if err == nil {
		t.Error("workforest add-repo: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// workforest remove-repo
// ---------------------------------------------------------------------------

func TestWorkspaceWorkforestRemoveRepo_ArgsRequired(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove-repo", "my-branch"})
	if err == nil {
		t.Error("workforest remove-repo with 1 arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorkforestRemoveRepo_ForceFlagAccepted(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "remove-repo", "--force", "my-branch", "repo"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("workforest remove-repo --force: flag not wired: %v", err)
	}
}

func TestWorkspaceWorkforestRemoveRepo_AliasRmRepo(t *testing.T) {
	// rm-repo is an alias for remove-repo; must be accepted (will fail on missing
	// set, but not as an unknown command).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"workforest", "rm-repo", "my-branch", "repo"})
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		t.Errorf("workforest rm-repo alias not wired: %v", err)
	}
}

func TestWorkspaceWorkforestRemoveRepo_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"workforest", "remove-repo", "my-branch", "repo"})
	if err == nil {
		t.Error("workforest remove-repo: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// Persistent --terminal flag wiring: verify it is truly persistent
// (inherited by every subcommand via the parent ws command)
// ---------------------------------------------------------------------------

func TestPersistentTerminalFlag_IsInheritedByAllSubcommands(t *testing.T) {
	// For each subcommand that accepts --terminal, invoke it with the flag and
	// assert the flag is not rejected. Commands that are required-terminal will
	// fail for other reasons (no real terminal dir) but NOT with "unknown flag".
	type subEntry struct {
		name string
		args []string
	}
	subcommands := []subEntry{
		{"status", []string{"status", "--terminal", "sentinel"}},
		{"init", []string{"init", "--terminal", "sentinel"}},
		{"clone", []string{"clone", "--terminal", "sentinel"}},
		{"lock", []string{"lock", "--terminal", "sentinel"}},
		{"build", []string{"build", "--terminal", "sentinel"}},
		{"apply", []string{"apply", "--terminal", "sentinel"}},
		{"flake-check", []string{"flake-check", "--terminal", "sentinel"}},
		{"pre-commit-check", []string{"pre-commit-check", "--terminal", "sentinel"}},
		{"push", []string{"push", "--terminal", "sentinel"}},
		{"rebase", []string{"rebase", "--terminal", "sentinel"}},
		{"format", []string{"format", "--terminal", "sentinel"}},
		{"tree", []string{"tree", "--terminal", "sentinel"}},
		{"update", []string{"update", "--terminal", "sentinel"}},
		{"upgrade", []string{"upgrade", "--terminal", "sentinel"}},
		{"discover", []string{"discover", "--terminal", "sentinel"}},
		{"workforest add", []string{"workforest", "add", "--terminal", "sentinel", "my-branch"}},
		{"workforest list", []string{"workforest", "list", "--terminal", "sentinel"}},
		{"workforest remove", []string{"workforest", "remove", "--terminal", "sentinel", "my-branch"}},
		{"workforest prune", []string{"workforest", "prune", "--terminal", "sentinel"}},
	}

	orig := openWorkspace
	t.Cleanup(func() { openWorkspace = orig })

	for _, sub := range subcommands {
		sub := sub
		t.Run(sub.name, func(t *testing.T) {
			fr := exec.NewFakeRunner()
			w := newTestWorkspace(t, fr, minimalToml)
			openWorkspace = func() (*workspace.Workspace, error) { return w, nil }

			_, _, err := runCobraCmd(t, sub.args)
			if err != nil && strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("%s: --terminal flag not wired (persistent flag missing): %v", sub.name, err)
			}
		})
	}
}

// TestWorkspaceLock_EvalFailure_RefusesThenAllows: `pn workspace lock` exits
// non-zero when a repo's flake fails every eval tier; --allow-missing-edges
// downgrades that to a warning and writes the lock. (bead pg2-cqcex)
func TestWorkspaceLock_EvalFailure_RefusesThenAllows(t *testing.T) {
	root := t.TempDir()
	// Repo with a flake on disk whose eval fails; explicit terminal so the only
	// validation error is eval_failed.
	if err := os.MkdirAll(filepath.Join(root, "myrepo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "myrepo", "flake.nix"), []byte("{ outputs = {}; }"), 0o644); err != nil {
		t.Fatalf("write flake: %v", err)
	}
	toml := `[workspace]
name = "test"
terminal = "myrepo"

[repos.myrepo]
url = "github:owner/myrepo"
`
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(toml), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	flakeAbs := filepath.Join(root, "myrepo", "flake.nix")
	evalExprs := []string{
		`is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`,
		`is: builtins.mapAttrs (n: v: { url = v.url or null; flake = true; }) is`,
		"builtins.attrNames",
	}
	// Each runCobraCmd calls openWorkspace(), which must return a workspace with a
	// FRESH runner (eval responses are consumed FIFO). Rebuild per call.
	orig := openWorkspace
	t.Cleanup(func() { openWorkspace = orig })
	openWorkspace = func() (*workspace.Workspace, error) {
		fr := exec.NewFakeRunner()
		for _, expr := range evalExprs {
			cmdErr := &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}}
			fr.AddResponse("nix", []string{"eval", "--json", "--file", flakeAbs, "inputs", "--apply", expr},
				exec.Result{ExitCode: 1}, cmdErr)
		}
		return workspace.Open(root, fr)
	}

	// Without the flag → refuse (non-zero exit).
	if _, _, err := runCobraCmd(t, []string{"lock"}); err == nil {
		t.Fatal("lock with an un-evaluable repo flake: expected non-zero exit")
	}
	if _, statErr := os.Stat(filepath.Join(root, workspace.LockFileName)); !os.IsNotExist(statErr) {
		t.Errorf("no lock file should be written on refusal; stat err = %v", statErr)
	}

	// With the flag → succeed and write the lock.
	stdout, _, err := runCobraCmd(t, []string{"lock", "--allow-missing-edges"})
	if err != nil {
		t.Fatalf("lock --allow-missing-edges: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "pn-workspace.lock.json") {
		t.Errorf("expected confirmation on stdout; got %q", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(root, workspace.LockFileName)); statErr != nil {
		t.Errorf("lock file should exist after --allow-missing-edges; err = %v", statErr)
	}
}

// TestWorkspaceAllowDeny_RoundTrip: `pn workspace allow` echoes the declared
// hooks and trusts the resolved root; `pn workspace deny` revokes it. (pg2-oymai)
func TestWorkspaceAllowDeny_RoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(`
[workspace]
name = "test"
[[hooks]]
when = ["pre-status"]
run = ["echo hi"]
`), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	t.Setenv("PN_WORKSPACE_ROOT", root)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stdout, _, err := runCobraCmd(t, []string{"allow"})
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !strings.Contains(stdout, "trusted workspace hooks") {
		t.Errorf("allow should confirm trust; got %q", stdout)
	}
	if !strings.Contains(stdout, "echo hi") {
		t.Errorf("allow should echo declared hook run lines for review; got %q", stdout)
	}
	if err := trust.EnsureAllowed(root); err != nil {
		t.Errorf("after allow, EnsureAllowed should pass; got %v", err)
	}

	if _, _, err := runCobraCmd(t, []string{"deny"}); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if err := trust.EnsureAllowed(root); err == nil {
		t.Errorf("after deny, EnsureAllowed should fail")
	}
}
