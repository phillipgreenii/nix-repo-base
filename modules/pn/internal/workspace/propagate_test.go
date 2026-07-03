package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func osWrite(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }
func osMkdirAll(dir string) error        { return os.MkdirAll(dir, 0o755) }

// --- pure-unit coverage -----------------------------------------------------

func TestReadAliasRevs(t *testing.T) {
	dir := t.TempDir()
	lock := `{
      "version": 7,
      "root": "root",
      "nodes": {
        "root":      {"inputs": {"sib": "sib", "dedup": "nixpkgs_2", "followed": ["app", "nixpkgs"]}},
        "sib":       {"locked": {"rev": "1111111111111111111111111111111111111111"}},
        "nixpkgs_2": {"locked": {"rev": "2222222222222222222222222222222222222222"}}
      }
    }`
	lockPath := filepath.Join(dir, "flake.lock")
	if err := osWrite(lockPath, lock); err != nil {
		t.Fatal(err)
	}

	got, err := readAliasRevs(lockPath, []string{"sib", "dedup", "followed", "absent"})
	if err != nil {
		t.Fatalf("readAliasRevs: %v", err)
	}
	want := map[string]string{
		"sib":   "1111111111111111111111111111111111111111", // direct node
		"dedup": "2222222222222222222222222222222222222222", // root.inputs key != node key
	}
	// "followed" (a follows array) and "absent" must be skipped, not errored.
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("alias %s: got %q want %q", k, got[k], v)
		}
	}

	// Missing flake.lock → empty map, no error.
	empty, err := readAliasRevs(filepath.Join(dir, "nope.lock"), []string{"sib"})
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing lock: got %v err %v", empty, err)
	}
}

func TestBumpCommitMessage(t *testing.T) {
	before := map[string]string{"a": "a111111deadbeef", "b": "b111111deadbeef"}
	after := map[string]string{"a": "a222222deadbeef", "b": "b222222deadbeef"}

	if got := bumpCommitMessage([]string{"a"}, before, after); got != "chore(deps): bump a a111111 -> a222222" {
		t.Errorf("single-alias message = %q", got)
	}
	multi := bumpCommitMessage([]string{"a", "b"}, before, after)
	if !strings.HasPrefix(multi, "chore(deps): bump workspace inputs\n") ||
		!strings.Contains(multi, "\na a111111 -> a222222") ||
		!strings.Contains(multi, "\nb b111111 -> b222222") {
		t.Errorf("multi-alias message = %q", multi)
	}
}

// --- integration: real git, nix intercepted to mutate flake.lock -----------

// fsNixRunner runs git for real (so staging/commit/clean-tree behave
// authentically) and intercepts `nix flake update` to apply a filesystem
// mutation (FakeRunner cannot do side effects). It records nix args.
type fsNixRunner struct {
	real    exec.Runner
	mutate  func() // applied on `nix flake update`
	nixErr  error
	nixArgs [][]string
}

func (r *fsNixRunner) Run(ctx context.Context, name string, args []string, opts exec.RunOptions) (exec.Result, error) {
	if name == "nix" {
		r.nixArgs = append(r.nixArgs, append([]string(nil), args...))
		if r.nixErr != nil {
			return exec.Result{ExitCode: 1}, r.nixErr
		}
		if r.mutate != nil {
			r.mutate()
		}
		return exec.Result{}, nil
	}
	return r.real.Run(ctx, name, args, opts)
}

// propEnv sets up a real git repo laid out the way PRODUCTION is: flakeFileRel
// is the path to the flake.nix FILE (exactly what resolveFlakePath returns —
// "flake.nix", "nix/flake.nix"), so flake.nix is written as a regular file with
// flake.lock as its sibling. Tests therefore pass propagateWorkspaceEdges the
// same file-form value its production callers do (update.go / update_worktree.go
// pass ws.resolveFlakePath(name)). The earlier version of this helper treated
// its arg as the flake DIRECTORY — that fixture-vs-caller divergence is exactly
// what let pg2-vpy4 (ENOTDIR on <dir>/flake.nix/flake.lock) ship green.
// Returns the repo dir and a writer that overwrites flake.lock.
func propEnv(t *testing.T, flakeFileRel, initialLock string) (dir string, writeLock func(string)) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	flakeFile := filepath.Join(dir, flakeFileRel)
	flakeDir := filepath.Dir(flakeFile)
	if err := osMkdirAll(flakeDir); err != nil {
		t.Fatal(err)
	}
	if err := osWrite(flakeFile, "{ }\n"); err != nil { // flake.nix is a FILE, not a dir
		t.Fatal(err)
	}
	lockPath := filepath.Join(flakeDir, "flake.lock")
	if err := osWrite(lockPath, initialLock); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "init")
	return dir, func(s string) {
		if err := osWrite(lockPath, s); err != nil {
			t.Fatal(err)
		}
	}
}

func lockWith(sibRev string, lastModified int) string {
	return fmt.Sprintf(`{
  "version": 7, "root": "root",
  "nodes": {
    "root": {"inputs": {"sib": "sib"}},
    "sib": {"locked": {"rev": %q, "lastModified": %d}}
  }
}`, sibRev, lastModified)
}

// TestPropagate_BumpsAndCommitsOnRevChange is the root-flake happy path AND the
// pg2-vpy4 regression: flakeRel is the file-form "flake.nix" (what
// resolveFlakePath returns), so the function must derive "." as the flake dir and
// read/commit flake.lock from the repo root — not stat <dir>/flake.nix/flake.lock
// (ENOTDIR).
func TestPropagate_BumpsAndCommitsOnRevChange(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("2222222222222222222222222222222222222222", 2))
	}}
	ws := &Workspace{runner: r}

	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if !relocked {
		t.Errorf("relocked = false, want true (a rev moved and was committed)")
	}
	// C1: --refresh must be present.
	if len(r.nixArgs) != 1 || !containsStr(r.nixArgs[0], "--refresh") {
		t.Errorf("nix args = %v; want one call with --refresh", r.nixArgs)
	}
	// Exactly one new commit, with the canonical single-alias subject.
	if n := commitCount(t, dir); n != 2 {
		t.Errorf("commit count = %d, want 2", n)
	}
	if subj := headSubject(t, dir); subj != "chore(deps): bump sib 1111111 -> 2222222" {
		t.Errorf("subject = %q", subj)
	}
	// The committed path is the root flake.lock, not "flake.nix/flake.lock".
	if files := commitFiles(t, dir); len(files) != 1 || files[0] != "flake.lock" {
		t.Errorf("committed files = %v, want [flake.lock]", files)
	}
	assertCleanTree(t, dir)
}

// TestPropagate_NoCommitOnLastModifiedChurn is the C2 regression test: a
// flake.lock that changed only its lastModified (no rev move) must produce NO
// commit AND leave a clean tree (so the subsequent rebase + update-locks
// clean-tree gate are not tripped).
func TestPropagate_NoCommitOnLastModifiedChurn(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("1111111111111111111111111111111111111111", 99)) // same rev, new lastModified
	}}
	ws := &Workspace{runner: r}

	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if relocked {
		t.Errorf("relocked = true, want false (only lastModified churn, no bump)")
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1 (no bump commit)", n)
	}
	assertCleanTree(t, dir)
}

func TestPropagate_NoOpWhenUnchanged(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {}} // nix writes nothing
	ws := &Workspace{runner: r}
	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if relocked {
		t.Errorf("relocked = true, want false (nix wrote nothing)")
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1", n)
	}
	assertCleanTree(t, dir)
}

// TestPropagate_FlakeInSubdir is the homelab-style case: the flake.nix FILE lives
// in a subdir, so resolveFlakePath returns "nix/flake.nix" and the function must
// derive "nix" as the flake dir and read/commit "nix/flake.lock".
func TestPropagate_FlakeInSubdir(t *testing.T) {
	dir, writeLock := propEnv(t, "nix/flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("3333333333333333333333333333333333333333", 2))
	}}
	ws := &Workspace{runner: r}
	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "homelab", dir, "nix/flake.nix", []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if !relocked {
		t.Errorf("relocked = false, want true (subdir flake rev moved and committed)")
	}
	if subj := headSubject(t, dir); !strings.HasPrefix(subj, "chore(deps): bump sib 1111111 -> 3333333") {
		t.Errorf("subject = %q", subj)
	}
	// The committed path must be nix/flake.lock.
	if files := commitFiles(t, dir); len(files) != 1 || files[0] != "nix/flake.lock" {
		t.Errorf("committed files = %v, want [nix/flake.lock]", files)
	}
	assertCleanTree(t, dir)
}

// TestPropagate_FlakeRelViaResolveFlakePath drives the flakeRel value THROUGH
// resolveFlakePath (the production source) rather than hard-coding it, so the
// test contract can never again silently diverge from the caller's: the repo is
// laid out under ws.root/<repoKey> with a real flake.nix file, resolveFlakePath
// discovers it on disk and returns the FILE path "flake.nix", and that exact
// value is fed to propagateWorkspaceEdges.
func TestPropagate_FlakeRelViaResolveFlakePath(t *testing.T) {
	root := t.TempDir()
	const repoKey = "foo"
	dir := filepath.Join(root, repoKey)
	if err := osMkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	if err := osWrite(filepath.Join(dir, "flake.nix"), "{ }\n"); err != nil { // FILE
		t.Fatal(err)
	}
	if err := osWrite(filepath.Join(dir, "flake.lock"), lockWith("1111111111111111111111111111111111111111", 1)); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "init")

	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		if err := osWrite(filepath.Join(dir, "flake.lock"), lockWith("2222222222222222222222222222222222222222", 2)); err != nil {
			t.Fatal(err)
		}
	}}
	ws := &Workspace{runner: r, root: root, config: &WorkspaceConfig{}}

	flakeRel := ws.resolveFlakePath(repoKey)
	if flakeRel != "flake.nix" {
		t.Fatalf("resolveFlakePath = %q, want %q (file path, not dir)", flakeRel, "flake.nix")
	}
	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, repoKey, dir, flakeRel, []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if !relocked {
		t.Errorf("relocked = false, want true (rev moved and committed)")
	}
	if subj := headSubject(t, dir); subj != "chore(deps): bump sib 1111111 -> 2222222" {
		t.Errorf("subject = %q", subj)
	}
	assertCleanTree(t, dir)
}

func TestPropagate_EmptyAliasesIsNoOp(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner()}
	ws := &Workspace{runner: r}
	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", nil)
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if relocked {
		t.Errorf("relocked = true, want false (no aliases → no-op)")
	}
	if len(r.nixArgs) != 0 {
		t.Errorf("nix called %d times, want 0 (no aliases)", len(r.nixArgs))
	}
	assertCleanTree(t, dir)
}

func TestPropagate_NixFailureErrorsCleanly(t *testing.T) {
	dir, _ := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), nixErr: fmt.Errorf("boom")}
	ws := &Workspace{runner: r}
	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if err == nil {
		t.Fatal("expected error when nix flake update fails")
	}
	if relocked {
		t.Errorf("relocked = true, want false on error path")
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1 (no partial commit)", n)
	}
}

// TestPropagate_CommitsUnderMissingConfigPreCommitHook is the tc-1zbpk regression:
// in an ephemeral update worktree the prek pre-commit hook (installed in the
// canonical gitdir, shared into the worktree) fires on the bump commit, but the
// worktree has no .pre-commit-config.yaml (a gitignored dev-shell symlink that
// only exists in the canonical checkout), so prek aborts with "config file not
// found". propagateWorkspaceEdges must pass PREK_ALLOW_NO_CONFIG on its commit so
// the bump still lands. We install a hook that mimics prek — fail unless
// PREK_ALLOW_NO_CONFIG is set — and assert the commit is created; without the fix
// the hook exits 1 and no bump commit lands.
func TestPropagate_CommitsUnderMissingConfigPreCommitHook(t *testing.T) {
	dir, writeLock := propEnv(t, "flake.nix", lockWith("1111111111111111111111111111111111111111", 1))
	// Installed AFTER propEnv's init commit so init is not blocked.
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte(
		"#!/bin/sh\n"+
			"if [ -z \"$PREK_ALLOW_NO_CONFIG\" ]; then\n"+
			"  echo 'config file not found: .pre-commit-config.yaml' >&2\n"+
			"  exit 1\n"+
			"fi\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("2222222222222222222222222222222222222222", 2))
	}}
	ws := &Workspace{runner: r}

	relocked, err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, "flake.nix", []string{"sib"})
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if !relocked {
		t.Errorf("relocked = false, want true (bump committed despite the prek hook)")
	}
	if n := commitCount(t, dir); n != 2 {
		t.Errorf("commit count = %d, want 2 (bump commit must land despite the prek hook)", n)
	}
	if subj := headSubject(t, dir); subj != "chore(deps): bump sib 1111111 -> 2222222" {
		t.Errorf("subject = %q", subj)
	}
	assertCleanTree(t, dir)
}

func TestWorkspaceAliasesFromLock(t *testing.T) {
	lock := &Lock{Edges: []LockEdge{
		{Consumer: "app", Alias: "zlib", Target: "lib"},
		{Consumer: "app", Alias: "abc", Target: "other"},
		{Consumer: "lib", Alias: "x", Target: "base"},
	}}
	got := workspaceAliasesFromLock(lock, "app")
	want := []string{"abc", "zlib"} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if n := workspaceAliasesFromLock(nil, "app"); n != nil {
		t.Errorf("nil lock: got %v", n)
	}
}

// --- existence-gate: update-locks.sh skipped (not failed) when absent --------

func TestUpdateViaWorktree_SkipsWhenNoUpdateLocks(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	if err := os.Remove(filepath.Join(wt, "update-locks.sh")); err != nil {
		t.Fatal(err) // this repo has no update-locks.sh
	}
	// Script every step EXCEPT ./update-locks.sh (the gate must skip it).
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("dead00000000000000000000000000000000beef\n")}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out strings.Builder
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/nix/store/x/lib/scripts"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "./update-locks.sh" {
			t.Error("update-locks.sh must not run when absent")
		}
	}
	if !strings.Contains(out.String(), "no update-locks.sh") {
		t.Errorf("expected skip notice; out=%q", out.String())
	}
}

func TestUpdate_InPlaceSkipsWhenNoUpdateLocks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo") // deliberately NO mkUpdateLocks
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out strings.Builder
	if err := w.Update(context.Background(), &out, UpdateOptions{InPlace: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "./update-locks.sh" {
			t.Error("update-locks.sh must not run when absent")
		}
	}
	if !strings.Contains(out.String(), "no update-locks.sh") {
		t.Errorf("expected skip notice; out=%q", out.String())
	}
}

// --- small git/file helpers (kept local to avoid touching shared helpers) ---

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := osexec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := osexec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func commitCount(t *testing.T, dir string) int {
	var n int
	_, _ = fmt.Sscanf(gitOut(t, dir, "rev-list", "--count", "HEAD"), "%d", &n)
	return n
}

func headSubject(t *testing.T, dir string) string { return gitOut(t, dir, "log", "-1", "--format=%s") }

func commitFiles(t *testing.T, dir string) []string {
	out := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD")
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	sort.Strings(files)
	return files
}

func assertCleanTree(t *testing.T, dir string) {
	t.Helper()
	if s := gitOut(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("working tree not clean:\n%s", s)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
