// internal/workspace/realgit_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestMain makes the PRODUCTION code under test hermetic. The harness's own
// git (initRealRepo, runGitT, the bare-remote helpers) now runs through
// x/gittest/x/gitfixture, which is hermetic by construction (fixture HOME,
// ceiling directory, no system config, GIT_* location variables never
// inherited). What it cannot cover is the code under test: production code
// spawns git via internal/exec.realRunner, which copies os.Environ(), so this
// process's own environment must still be neutral.
//
// It redirects git's global and system config to /dev/null (pg2-39rz2: on a
// developer machine with core.fsmonitor=true in global config, a fresh temp
// repo spawns `git fsmonitor--daemon`, and a wedged daemon hung `go test` to
// go's 600s panic timeout; the nix build sandbox is unaffected only because its
// HOME is clean). It also unsets every git-location env var BEFORE any test
// runs (pg2-kersl, mechanism proven in pg2-67h4y's design field): a git hook
// invoking `go test` for this package exports GIT_DIR/GIT_INDEX_FILE for the
// commit in progress, and `-C <dir>`/cmd.Dir do NOT override them, so production
// git run against a fixture would otherwise operate on the AMBIENT repo.
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
// invocation ignore the developer's global and system git config. TestMain
// applies it to the process environment (for production git run through
// internal/exec) and hermeticGitOptions to x/gitclient constructors. See
// TestMain for the rationale.
func gitConfigIsolationEnv() map[string]string {
	return map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null",
		"GIT_CONFIG_SYSTEM": "/dev/null",
	}
}

// fixtureRepos maps every repository and fixture root created by this
// harness (symlink-resolved) to the x/gitfixture Repo that owns it, so runGitT
// can find the hermetic client for a directory a test hands it.
var (
	fixtureMu    sync.Mutex
	fixtureRepos = map[string]*gitfixture.Repo{}
	scratchRepos = map[*testing.T]*gitfixture.Repo{}
)

// canonPath resolves symlinks in p even when its tail does not exist yet (a
// worktree or clone target), so a path compares equal to the symlink-resolved
// paths x/gitfixture records (darwin: /var -> /private/var).
func canonPath(p string) string {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func registerFixture(t *testing.T, r *gitfixture.Repo) {
	t.Helper()
	keys := []string{r.Dir, r.Root()}
	fixtureMu.Lock()
	for _, k := range keys {
		fixtureRepos[k] = r
	}
	fixtureMu.Unlock()
	t.Cleanup(func() {
		fixtureMu.Lock()
		for _, k := range keys {
			if fixtureRepos[k] == r {
				delete(fixtureRepos, k)
			}
		}
		fixtureMu.Unlock()
	})
}

// fixtureFor returns the fixture repo whose hermetic client should run git for
// dir: the repo or fixture root dir lives in (or is), else a per-test scratch
// fixture from x/gittest for a directory outside every known root.
func fixtureFor(t *testing.T, dir string) *gitfixture.Repo {
	t.Helper()
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	for cur := canonPath(dir); ; cur = filepath.Dir(cur) {
		if r, ok := fixtureRepos[cur]; ok {
			return r
		}
		if filepath.Dir(cur) == cur {
			break
		}
	}
	if r, ok := scratchRepos[t]; ok {
		return r
	}
	r := gittest.New(t, gitfixture.RepoOptions{Name: "scratch"})
	scratchRepos[t] = r
	t.Cleanup(func() {
		fixtureMu.Lock()
		delete(scratchRepos, t)
		fixtureMu.Unlock()
	})
	return r
}

// runGitT runs git in dir through the hermetic x/gitfixture client that owns
// dir and returns trimmed stdout, failing the test on error. The command runs
// with `-C dir`, so dir need not be a repo the fixture created (worktrees,
// clones). A fixed author identity is supplied per call so commits made in
// clones and worktrees never depend on ambient config.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-C", dir}, args...)
	out, err := fixtureFor(t, dir).Client.Run(t.Context(), full...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// initRealRepo creates a real git repo at dir with an initial commit on main
// (README.md). The repo is an x/gitfixture repository named filepath.Base(dir)
// under the fixture root filepath.Dir(dir), so several repos created beside one
// another (dep/consumer, alpha/beta) share one hermetic fixture root. It uses
// gitfixture.NewRepo (the core gittest.New wraps) rather than gittest.New
// because callers dictate dir, while gittest.New always picks its own t.TempDir.
func initRealRepo(t *testing.T, dir string) {
	t.Helper()
	// Callers may have pre-created dir, even populated (openHookWS drops a
	// flake.nix into each repo dir). gitfixture refuses an explicitly named repo
	// that already exists, so park the existing entries aside, create the repo,
	// and restore them so the initial commit still contains them.
	held := holdExistingEntries(t, dir)
	r, err := gitfixture.NewRepo(t.Context(), filepath.Dir(dir), gitfixture.RepoOptions{
		Suite: t.Name(),
		Name:  filepath.Base(dir),
	})
	if err != nil {
		t.Fatalf("initRealRepo(%s): %v", dir, err)
	}
	registerFixture(t, r)
	// gitfixture points core.hooksPath at its empty hooks dir (an extra guard
	// against an inherited global hooksPath). The fixture HOME already hides
	// global config and a fresh repo's own .git/hooks is empty, so drop the
	// redirect: pn's hook-bundle code reads core.hooksPath and the hook tests
	// install hooks under .git/hooks.
	if _, err := r.Client.Run(t.Context(), "config", "--unset", "core.hooksPath"); err != nil {
		t.Fatalf("initRealRepo(%s): unsetting core.hooksPath: %v", dir, err)
	}
	for _, name := range held.names {
		if err := os.Rename(filepath.Join(held.dir, name), filepath.Join(r.Dir, name)); err != nil {
			t.Fatalf("initRealRepo(%s): restoring %s: %v", dir, name, err)
		}
	}
	if _, err := r.Commit(t.Context(), "init", map[string]string{"README.md": "init\n"}); err != nil {
		t.Fatalf("initRealRepo(%s): %v", dir, err)
	}
	if _, err := r.Client.Run(t.Context(), "add", "."); err != nil {
		t.Fatalf("initRealRepo(%s): staging pre-existing files: %v", dir, err)
	}
	if len(held.names) > 0 {
		if _, err := r.Client.Run(t.Context(), "commit", "-q", "-m", "init (pre-existing files)"); err != nil {
			t.Fatalf("initRealRepo(%s): committing pre-existing files: %v", dir, err)
		}
	}
}

// heldEntries is a directory's top-level entries moved out of the way.
type heldEntries struct {
	dir   string
	names []string
}

// holdExistingEntries moves every top-level entry of dir (if it exists) into a
// fresh temp dir and removes dir, so gitfixture can create a repo at the path.
func holdExistingEntries(t *testing.T, dir string) heldEntries {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return heldEntries{} // dir absent: nothing to hold
	}
	h := heldEntries{dir: t.TempDir()}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(h.dir, e.Name())); err != nil {
			t.Fatalf("initRealRepo(%s): parking %s: %v", dir, e.Name(), err)
		}
		h.names = append(h.names, e.Name())
	}
	if err := os.Remove(dir); err != nil {
		t.Fatalf("initRealRepo(%s): removing emptied dir: %v", dir, err)
	}
	return h
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

// addBareSibling creates a bare x/gitfixture repo named name beside dir (under
// the same fixture root), registers it as remote `remote` of dir, and returns
// its path. The bare HEAD is pinned to main by gitfixture regardless of the
// ambient init.defaultBranch.
func addBareSibling(t *testing.T, dir, name, remote string) string {
	t.Helper()
	bare, err := fixtureFor(t, dir).NewSibling(t.Context(), name, gitfixture.RepoOptions{Bare: true})
	if err != nil {
		t.Fatalf("bare sibling %s of %s: %v", name, dir, err)
	}
	runGitT(t, dir, "remote", "add", remote, bare.Dir)
	return bare.Dir
}

// setupLocalBareRemote creates a bare repo beside dir, adds it as origin,
// and pushes the current branch. Returns the bare repo path.
func setupLocalBareRemote(t *testing.T, dir string) string {
	t.Helper()
	bare := addBareSibling(t, dir, filepath.Base(dir)+".git", "origin")
	runGitT(t, dir, "push", "-q", "origin", currentBranch(t, dir))
	return bare
}

// setupLocalBareRemoteNamed creates a bare repo beside dir, adds it as a remote
// under the given name (not "origin"), and pushes the current branch. Returns
// the bare repo path. Used to prove the doctor honors the resolved push remote
// rather than a hardcoded "origin".
func setupLocalBareRemoteNamed(t *testing.T, dir, remote string) string {
	t.Helper()
	bare := addBareSibling(t, dir, filepath.Base(dir)+"."+remote+".git", remote)
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
