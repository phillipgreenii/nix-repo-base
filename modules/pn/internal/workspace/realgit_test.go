// internal/workspace/realgit_test.go
package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// TestMain makes the whole package's real-git tests HERMETIC: it redirects
// git's global and system config to /dev/null for the test process so BOTH the
// test harness (runGitT/initRealRepo, plus propagate_test.go's local git
// helpers) AND the production code under test — which spawns git via the real
// runner and inherits this process's environment (internal/exec.realRunner
// copies os.Environ()) — never read the developer's ~/.gitconfig or XDG
// global config.
//
// Why this matters (pg2-39rz2): on a developer machine with core.fsmonitor=true
// in global config, a fresh temp repo enables the built-in fsmonitor and
// `git add`/`commit`/`status` spawns `git fsmonitor--daemon`. A wedged/leaked
// daemon then blocks index refreshes on the IPC socket, hanging
// `go test ./internal/workspace/` to go's 600s panic timeout. The nix build
// sandbox is unaffected only because its HOME is clean. Neutralizing global +
// system config here removes that inheritance entirely (good hygiene beyond
// fsmonitor) and mirrors the smoke harness's scrubbed env (smoke/smoke_env.go).
//
// It also unsets every git-location env var BEFORE any test runs (pg2-kersl,
// mechanism proven in pg2-67h4y's design field). A git hook (pre-commit/prek,
// invoking `go test` for this very package as its run-unit-tests hook) exports
// GIT_DIR/GIT_INDEX_FILE for the commit in progress when the commit runs from a
// linked worktree, and this test binary inherits that. `-C <dir>`, cmd.Dir, and
// even an explicit path argument passed to runGitT/hermeticGitCmd do NOT
// override these — git's own repo discovery consults the environment FIRST —
// so a test that builds an isolated fixture under t.TempDir() and drives real
// git (init/config/commit/add/worktree/branch/remote/push/checkout/tag/reset)
// would otherwise silently operate on the AMBIENT repo (the canonical clone
// this worktree links to) instead of the fixture. gitConfigIsolationEnv above
// covers only the global/system CONFIG half of hermeticity; this covers the
// separate REPO-LOCATION half. Unsetting these here, once, for the whole
// process is sufficient: nothing in this package's tests or the code under
// test re-sets them.
//
// os.Setenv (rather than a per-command env) is used so exec'd children inherit
// the isolation via os.Environ(); it runs once, before any test, so it is safe
// with respect to test parallelism.
func TestMain(m *testing.M) {
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
		"GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
	} {
		_ = os.Unsetenv(k)
	}
	for k, v := range gitConfigIsolationEnv() {
		if err := os.Setenv(k, v); err != nil {
			panic("realgit_test TestMain: os.Setenv " + k + ": " + err.Error())
		}
	}
	// The env-var-based isolation above (os.Setenv, inherited via
	// internal/exec.realRunner's os.Environ() copy) does NOT reach git
	// invocations made through x/gitclient: gitclient.Client builds its
	// child environment from a fixed allowlist (PATH, HOME, SSH_AUTH_SOCK —
	// design §4.4 D5) and never carries over GIT_CONFIG_GLOBAL/
	// GIT_CONFIG_SYSTEM from the parent process. Left alone, every real-git
	// test that reaches openGitReader/openGitMutator/cloneGitRepo (bead
	// pg2-8bfb5's mutating-side migration routes real commits through this
	// seam) would inherit the developer's ACTUAL global git config —
	// exactly the pg2-39rz2 class of bug, just via a second, gitclient-
	// shaped door. Installing hermetic package-level defaults here, once,
	// closes that door the same way the env vars above close it for the raw
	// runner. Individual tests may still override these three vars further
	// via stubGitOpener/stubGitMutatorOpener/stubGitCloner; those
	// t.Cleanup-restore back to these hermetic defaults, not to gitclient's
	// bare constructors.
	openGitReader = func(ctx context.Context, dir string) (gitReader, error) {
		return gitclient.New(ctx, dir, hermeticGitOptions()...)
	}
	openGitMutator = func(ctx context.Context, dir string) (gitMutator, error) {
		return gitclient.New(ctx, dir, hermeticGitOptions()...)
	}
	cloneGitRepo = func(ctx context.Context, url, dir string, opts gitclient.CloneOptions) (gitMutator, *gitclient.Handle, error) {
		return gitclient.Clone(ctx, url, dir, opts, hermeticGitOptions()...)
	}
	os.Exit(m.Run())
}

// hermeticGitOptions is gitConfigIsolationEnv's equivalent for x/gitclient
// constructors — see TestMain's doc comment for why the two mechanisms
// (process env vars vs. gitclient's Option allowlist) must be applied
// separately.
func hermeticGitOptions() []gitclient.Option {
	var opts []gitclient.Option
	for k, v := range gitConfigIsolationEnv() {
		opts = append(opts, gitclient.WithEnv(k, v))
	}
	return opts
}

// gitConfigIsolationEnv returns the git env-var overrides that make a real-git
// invocation ignore the developer's global and system git config. Harness git
// helpers append these AFTER os.Environ() so they win over any ambient
// GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM (os/exec keeps the last value for a
// duplicated key). See TestMain for the rationale.
func gitConfigIsolationEnv() map[string]string {
	return map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null",
		"GIT_CONFIG_SYSTEM": "/dev/null",
	}
}

// runGitT runs git in dir and returns trimmed stdout, failing the test on error.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	// Neutralize the developer's global/system git config on EVERY harness git
	// call. Appended after os.Environ() so it wins over any ambient
	// GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM (os/exec keeps the last value for a
	// duplicated key). See TestMain / gitConfigIsolationEnv for the rationale.
	for k, v := range gitConfigIsolationEnv() {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRealRepo creates a real git repo at dir with an initial commit on main.
func initRealRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "init", "-q", "-b", "main")
	// Set a repo-local identity so commits made by the PRODUCTION code under
	// test (which spawns its own git without runGitT's per-command env identity)
	// succeed even when the environment has no global git identity — e.g. the
	// nix build sandbox, whose auto-detected `_nixbld1@host.(none)` git rejects.
	// Mirrors propEnv in propagate_test.go.
	runGitT(t, dir, "config", "user.email", "t@t")
	runGitT(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", ".")
	runGitT(t, dir, "commit", "-q", "-m", "init")
}

// addCommit writes file=content, commits it, and returns the new HEAD sha.
func addCommit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", ".")
	runGitT(t, dir, "commit", "-q", "-m", msg)
	return headRev(t, dir)
}

func headRev(t *testing.T, dir string) string {
	t.Helper()
	return runGitT(t, dir, "rev-parse", "HEAD")
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	return runGitT(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// setupLocalBareRemote creates a bare repo beside dir, adds it as origin,
// and pushes the current branch. Returns the bare repo path.
func setupLocalBareRemote(t *testing.T, dir string) string {
	t.Helper()
	bare := dir + ".git"
	// -b main so the bare repo's HEAD points at main regardless of the ambient
	// init.defaultBranch (unset in the nix sandbox → "master"). Otherwise a
	// clone of this remote lands on an unborn "master", commits made in that
	// clone create "master", and `push origin main` fails with
	// "src refspec main does not match any".
	runGitT(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	runGitT(t, dir, "remote", "add", "origin", bare)
	runGitT(t, dir, "push", "-q", "origin", currentBranch(t, dir))
	return bare
}

// setupLocalBareRemoteNamed creates a bare repo beside dir, adds it as a remote
// under the given name (not "origin"), and pushes the current branch. Returns
// the bare repo path. Used to prove the doctor honors the resolved push remote
// rather than a hardcoded "origin".
func setupLocalBareRemoteNamed(t *testing.T, dir, remote string) string {
	t.Helper()
	bare := dir + "." + remote + ".git"
	// -b main: see setupLocalBareRemote for why the bare HEAD must be pinned.
	runGitT(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	runGitT(t, dir, "remote", "add", remote, bare)
	runGitT(t, dir, "push", "-q", remote, currentBranch(t, dir))
	// Track the remote branch so `git rev-parse @{u}` / aheadBehind work.
	runGitT(t, dir, "branch", "--set-upstream-to", remote+"/"+currentBranch(t, dir))
	return bare
}

// dirtyTrackedFile modifies an already-tracked file without committing.
func dirtyTrackedFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRealGitHelpers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initRealRepo(t, dir)
	if b := currentBranch(t, dir); b != "main" {
		t.Fatalf("branch: want main, got %s", b)
	}
	h1 := headRev(t, dir)
	h2 := addCommit(t, dir, "a.txt", "x", "add a")
	if h1 == h2 || len(h2) != 40 {
		t.Fatalf("addCommit did not advance HEAD: %s -> %s", h1, h2)
	}
	bare := setupLocalBareRemote(t, dir)
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare remote not created: %v", err)
	}
}

// TestHarnessNeutralizesGlobalFsmonitor is the pg2-39rz2 regression guard: it
// proves the real-git harness never inherits the developer's global git config.
// It simulates a global config that turns core.fsmonitor on (the setting that,
// on the affected machine, made temp repos spawn `git fsmonitor--daemon` and
// hang the suite) and asserts a harness git invocation in a fresh repo still
// reports core.fsmonitor unset.
//
// It probes via a config READ (never `git status`), so the assertion itself
// cannot spawn an fsmonitor daemon: `git init` performs no index refresh, and
// `git config` reads config without touching fsmonitor.
func TestHarnessNeutralizesGlobalFsmonitor(t *testing.T) {
	// Simulate a developer global git config that enables fsmonitor.
	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalCfg, []byte("[core]\n\tfsmonitor = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point git's global-config selector at it for this test only. A
	// non-hermetic harness would inherit this via os.Environ(); the hermetic
	// harness overrides it with GIT_CONFIG_GLOBAL=/dev/null.
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)

	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "init", "-q", "-b", "main")

	// --default false => the read exits 0 with "false" when the key is unset;
	// a bare `config --get` exits 1 on a missing key, which runGitT would treat
	// as a fatal error.
	if got := runGitT(t, dir, "config", "--default", "false", "--get", "core.fsmonitor"); got == "true" {
		t.Fatalf("harness inherited developer core.fsmonitor=%q; the real-git harness must neutralize global git config (GIT_CONFIG_GLOBAL=/dev/null)", got)
	}
}
