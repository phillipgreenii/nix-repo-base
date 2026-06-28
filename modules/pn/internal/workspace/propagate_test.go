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

// propEnv sets up a real git repo at <root>/<flakeRel> containing flake.lock,
// returns the repo dir and a writer that overwrites flake.lock.
func propEnv(t *testing.T, flakeRel, initialLock string) (dir string, writeLock func(string)) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	flakeDir := filepath.Join(dir, flakeRel)
	if err := osMkdirAll(flakeDir); err != nil {
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

func TestPropagate_BumpsAndCommitsOnRevChange(t *testing.T) {
	dir, writeLock := propEnv(t, ".", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("2222222222222222222222222222222222222222", 2))
	}}
	ws := &Workspace{runner: r}

	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, ".", []string{"sib"}); err != nil {
		t.Fatalf("propagate: %v", err)
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
	assertCleanTree(t, dir)
}

// TestPropagate_NoCommitOnLastModifiedChurn is the C2 regression test: a
// flake.lock that changed only its lastModified (no rev move) must produce NO
// commit AND leave a clean tree (so the subsequent rebase + update-locks
// clean-tree gate are not tripped).
func TestPropagate_NoCommitOnLastModifiedChurn(t *testing.T) {
	dir, writeLock := propEnv(t, ".", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("1111111111111111111111111111111111111111", 99)) // same rev, new lastModified
	}}
	ws := &Workspace{runner: r}

	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, ".", []string{"sib"}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1 (no bump commit)", n)
	}
	assertCleanTree(t, dir)
}

func TestPropagate_NoOpWhenUnchanged(t *testing.T) {
	dir, _ := propEnv(t, ".", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {}} // nix writes nothing
	ws := &Workspace{runner: r}
	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, ".", []string{"sib"}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1", n)
	}
	assertCleanTree(t, dir)
}

func TestPropagate_FlakeInSubdir(t *testing.T) {
	dir, writeLock := propEnv(t, "nix", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		writeLock(lockWith("3333333333333333333333333333333333333333", 2))
	}}
	ws := &Workspace{runner: r}
	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "homelab", dir, "nix", []string{"sib"}); err != nil {
		t.Fatalf("propagate: %v", err)
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

func TestPropagate_EmptyAliasesIsNoOp(t *testing.T) {
	dir, _ := propEnv(t, ".", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner()}
	ws := &Workspace{runner: r}
	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, ".", nil); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if len(r.nixArgs) != 0 {
		t.Errorf("nix called %d times, want 0 (no aliases)", len(r.nixArgs))
	}
	assertCleanTree(t, dir)
}

func TestPropagate_NixFailureErrorsCleanly(t *testing.T) {
	dir, _ := propEnv(t, ".", lockWith("1111111111111111111111111111111111111111", 1))
	r := &fsNixRunner{real: exec.NewRealRunner(), nixErr: fmt.Errorf("boom")}
	ws := &Workspace{runner: r}
	if err := ws.propagateWorkspaceEdges(context.Background(), io.Discard, "foo", dir, ".", []string{"sib"}); err == nil {
		t.Fatal("expected error when nix flake update fails")
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("commit count = %d, want 1 (no partial commit)", n)
	}
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
	fmt.Sscanf(gitOut(t, dir, "rev-list", "--count", "HEAD"), "%d", &n)
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
