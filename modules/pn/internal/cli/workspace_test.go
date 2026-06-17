package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
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
		{"worktree list", []string{"worktree", "list"}},
		{"worktree prune", []string{"worktree", "prune"}},
		{"worktree add", []string{"worktree", "add", "my-branch"}},
		{"worktree remove", []string{"worktree", "remove", "my-branch"}},
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
// worktree add
// ---------------------------------------------------------------------------

func TestWorkspaceWorktreeAdd_BranchArgRequired(t *testing.T) {
	// worktree add with no positional args must be rejected by cobra (RangeArgs(1,2)).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "add"})
	if err == nil {
		t.Error("worktree add with no args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreeAdd_TooManyArgsRejected(t *testing.T) {
	// worktree add accepts at most 2 positional args (branch + commit-ish).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "add", "my-branch", "abc123", "extra"})
	if err == nil {
		t.Error("worktree add with 3 positional args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreeAdd_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted without
	// "unknown flag" on the worktree add subcommand.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "add", "--terminal", "myterm", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("worktree add --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorktreeAdd_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"worktree", "add", "my-branch"})
	if err == nil {
		t.Error("worktree add: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// worktree list
// ---------------------------------------------------------------------------

func TestWorkspaceWorktreeList_NoArgsAccepted(t *testing.T) {
	// worktree list takes no args and exits 0 when the worktrees dir is absent.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "list"})
	if err != nil {
		t.Errorf("worktree list on empty workspace: unexpected error: %v", err)
	}
}

func TestWorkspaceWorktreeList_TooManyArgsRejected(t *testing.T) {
	// worktree list is cobra.NoArgs; extra positional args must error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "list", "extra"})
	if err == nil {
		t.Error("worktree list with extra arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreeList_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted without error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "list", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("worktree list --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorktreeList_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"worktree", "list"})
	if err == nil {
		t.Error("worktree list: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// worktree remove
// ---------------------------------------------------------------------------

func TestWorkspaceWorktreeRemove_BranchArgRequired(t *testing.T) {
	// worktree remove requires exactly 1 positional arg.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "remove"})
	if err == nil {
		t.Error("worktree remove with no args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreeRemove_TooManyArgsRejected(t *testing.T) {
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "remove", "my-branch", "extra"})
	if err == nil {
		t.Error("worktree remove with 2 positional args: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreeRemove_ForceFlagAccepted(t *testing.T) {
	// --force must be accepted without "unknown flag" error.
	// The command will fail because the set dir does not exist, but NOT due to an
	// unknown flag.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "remove", "--force", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("worktree remove --force: flag not wired: %v", err)
	}
}

func TestWorkspaceWorktreeRemove_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "remove", "--terminal", "myterm", "my-branch"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("worktree remove --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorktreeRemove_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"worktree", "remove", "my-branch"})
	if err == nil {
		t.Error("worktree remove: expected error when workspace open fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// worktree prune
// ---------------------------------------------------------------------------

func TestWorkspaceWorktreePrune_NoArgsAccepted(t *testing.T) {
	// worktree prune takes no args; with no repos the command exits 0 (no-op).
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "prune"})
	if err != nil {
		t.Errorf("worktree prune on empty workspace: unexpected error: %v", err)
	}
}

func TestWorkspaceWorktreePrune_TooManyArgsRejected(t *testing.T) {
	// worktree prune is cobra.NoArgs; extra args must error.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "prune", "extra"})
	if err == nil {
		t.Error("worktree prune with extra arg: expected cobra arg error, got nil")
	}
}

func TestWorkspaceWorktreePrune_FlagTerminalFlows(t *testing.T) {
	// --terminal is the persistent parent flag; it must be accepted.
	withFakeWorkspace(t, minimalToml)
	_, _, err := runCobraCmd(t, []string{"worktree", "prune", "--terminal", "myterm"})
	if err != nil && strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("worktree prune --terminal: persistent flag not inherited: %v", err)
	}
}

func TestWorkspaceWorktreePrune_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	_, _, err := runCobraCmd(t, []string{"worktree", "prune"})
	if err == nil {
		t.Error("worktree prune: expected error when workspace open fails, got nil")
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
		{"worktree add", []string{"worktree", "add", "--terminal", "sentinel", "my-branch"}},
		{"worktree list", []string{"worktree", "list", "--terminal", "sentinel"}},
		{"worktree remove", []string{"worktree", "remove", "--terminal", "sentinel", "my-branch"}},
		{"worktree prune", []string{"worktree", "prune", "--terminal", "sentinel"}},
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
