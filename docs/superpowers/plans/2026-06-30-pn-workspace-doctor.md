# pn workspace doctor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `pn workspace doctor` command that audits a pn workspace against the build-equality invariant, classifies findings as error/warning, and (with `--fix`) repairs the safe ones by delegating to existing `pn` command logic.

**Architecture:** A package-level `workspace.Doctor(ctx, root, runner, opts)` runs two phases — Phase 1 reads `pn-workspace.toml`/`.lock.json` raw (so a malformed file is diagnosable before `Open()` fails), Phase 2 opens the workspace and runs a registry of small `check` functions over a shared `doctorEnv` (mode + resolved terminal + memoized `refRev` map + effective lock). Each check emits `Finding`s; the fix engine applies the safe ones in dependency order and re-runs. Output is a human report or `--json`; exit code is carried by a typed `cli.ExitCodeError`.

**Tech Stack:** Go (package `github.com/phillipgreenii/nix-repo-base/modules/pn`), cobra CLI, `internal/exec.Runner` for subprocess calls, `git`/`nix` as external tools. Tests: Go `testing` with real `git` in `t.TempDir()` (the `propagate_test.go` pattern) and `exec.FakeRunner` only where a real call is impractical.

## Global Constraints

- Module path: `github.com/phillipgreenii/nix-repo-base/modules/pn`; doctor code lives in package `workspace` (`internal/workspace/`) and package `cli` (`internal/cli/`).
- Doctor owns orchestration only; every check delegates to an existing read primitive and every fix to an existing command method. Do not duplicate logic — extract a shared method where a command holds it inline.
- All subprocess calls go through `ws.runner` (an `exec.Runner`), never `os/exec` directly.
- Doctor never does destructive ops (no `reset --hard`, force-push, deleting repos, discarding uncommitted work). The only fix that pushes is `flake-lock-fresh` (delegates to `pn workspace update`); a push is acceptable when required to make the workspace consistent.
- "Dirty" = tracked-only via `isDirty` (`update.go:228`); untracked files are intentionally ignored.
- Tests that modify files MUST generate their scenario in a temp dir (`t.TempDir()`).
- Run all tests with `go test ./...` from `modules/pn`. Format with `nix fmt` / treefmt before committing; commit messages need no `Refs:` line (this is a phillipgreenii nix repo, not the ZR monorepo).
- Work on branch `pn-workspace-doctor` (already created).
- Spec: `docs/superpowers/specs/2026-06-30-pn-workspace-doctor-design.md`.

## File Structure

Create:

- `internal/workspace/doctor.go` — `Severity`, `FixState`, `Finding`, `DoctorReport`, `DoctorOptions`, `doctorEnv`, `check`, the `Doctor(...)` package func + orchestrator + registry.
- `internal/workspace/doctor_mode.go` — `workspaceMode`.
- `internal/workspace/doctor_refrev.go` — `resolveRefRevs` (ls-remote / captureHead).
- `internal/workspace/doctor_checks_structural.go` — structural checks (toml/lock).
- `internal/workspace/doctor_checks_repo.go` — repo presence/identity checks.
- `internal/workspace/doctor_checks_branch.go` — branch/tree checks.
- `internal/workspace/doctor_checks_terminal.go` — terminal/follows/flake-path checks.
- `internal/workspace/doctor_checks_flakelock.go` — flake-lock-fresh check.
- `internal/workspace/doctor_fix.go` — fix engine (`applyFixes`, dependency order, dry-run plan).
- `internal/workspace/doctor_render.go` — human + JSON rendering, severity rollup, exit code.
- `internal/workspace/branch_sync.go` — `switchToDefaultBranch`, `fastForwardIfBehind`.
- `internal/workspace/realgit_test.go` — shared real-git test helpers.
- `internal/cli/exit.go` — `ExitCodeError` typed error.
- Test files mirroring each source file.

Modify:

- `internal/cli/workspace.go` — register `workspaceDoctorCmd`.
- `cmd/pn/main.go` — map `cli.ExitCodeError` to its exit code.
- `internal/cli/root.go` — (only if needed) ensure the doctor's non-error exit path is preserved.

---

### Task 1: Typed exit-code plumbing

**Files:**

- Create: `internal/cli/exit.go`
- Modify: `cmd/pn/main.go`
- Test: `internal/cli/exit_test.go`

**Interfaces:**

- Produces: `cli.ExitCodeError{Code int}` (implements `error`); `cli.ExitCode(err error) int` returning the code for an `ExitCodeError`, `1` for any other non-nil error, `0` for nil.

- [ ] **Step 1: Write the failing test**

```go
// internal/cli/exit_test.go
package cli

import (
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("nil err: want 0, got %d", got)
	}
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Fatalf("plain err: want 1, got %d", got)
	}
	if got := ExitCode(ExitCodeError{Code: 2}); got != 2 {
		t.Fatalf("ExitCodeError{2}: want 2, got %d", got)
	}
	if got := ExitCodeError{Code: 2}.Error(); got == "" {
		t.Fatal("ExitCodeError.Error() must be non-empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/cli/ -run TestExitCode -v`
Expected: FAIL (`undefined: ExitCodeError`).

- [ ] **Step 3: Write minimal implementation**

```go
// internal/cli/exit.go
package cli

import (
	"errors"
	"fmt"
)

// ExitCodeError carries a process exit code up to main(). cobra only
// propagates an error, not a code, so commands that need a specific
// non-1 exit (e.g. doctor's 0/1/2) return this.
type ExitCodeError struct {
	Code int
	// Msg, when non-empty, is printed to stderr by main(); usually empty
	// because the command already rendered its own output.
	Msg string
}

func (e ExitCodeError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

// ExitCode maps an error to a process exit code: 0 for nil, the carried
// code for an ExitCodeError, 1 for any other error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec ExitCodeError
	if errors.As(err, &ec) {
		return ec.Code
	}
	return 1
}
```

- [ ] **Step 4: Update main.go to use ExitCode**

```go
// cmd/pn/main.go (replace the body of main)
func main() {
	err := cli.Execute(Version)
	if code := cli.ExitCode(err); code != 0 {
		// Print the message only for plain errors; ExitCodeError-carrying
		// commands have already rendered their own report to stdout.
		var ec cli.ExitCodeError
		if !errors.As(err, &ec) {
			fmt.Fprintln(os.Stderr, err)
		} else if ec.Msg != "" {
			fmt.Fprintln(os.Stderr, ec.Msg)
		}
		os.Exit(code)
	}
}
```

Add `"errors"` to the `cmd/pn/main.go` import block.

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/cli/ -run TestExitCode -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/cli/exit.go modules/pn/internal/cli/exit_test.go modules/pn/cmd/pn/main.go
git commit -m "feat(pn): add ExitCodeError plumbing for non-1 exit codes"
```

---

### Task 2: Shared real-git test helpers

**Files:**

- Create: `internal/workspace/realgit_test.go`
- Test: same file (a self-check test).

**Interfaces:**

- Produces (test-only, package `workspace`): `initRealRepo(t, dir)`, `addCommit(t, dir, file, content, msg) string` (returns the new HEAD sha), `headRev(t, dir) string`, `setupLocalBareRemote(t, dir) string` (creates a bare repo, sets it as `origin`, pushes the current branch, returns the bare path), `currentBranch(t, dir) string`, `runGitT(t, dir, args...) string`, `dirtyTrackedFile(t, dir, file, content)`.

- [ ] **Step 1: Write the helpers + a self-check test**

```go
// internal/workspace/realgit_test.go
package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitT runs git in dir and returns trimmed stdout, failing the test on error.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
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
	runGitT(t, ".", "init", "-q", "--bare", bare)
	runGitT(t, dir, "remote", "add", "origin", bare)
	runGitT(t, dir, "push", "-q", "origin", currentBranch(t, dir))
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
```

- [ ] **Step 2: Run the self-check**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestRealGitHelpers -v`
Expected: PASS (requires `git` on PATH, which CI has).

- [ ] **Step 3: Commit**

```bash
git add modules/pn/internal/workspace/realgit_test.go
git commit -m "test(pn): add shared real-git temp-dir test helpers"
```

---

### Task 3: Branch-sync helpers (`switchToDefaultBranch`, `fastForwardIfBehind`)

**Files:**

- Create: `internal/workspace/branch_sync.go`
- Test: `internal/workspace/branch_sync_test.go`

**Interfaces:**

- Consumes: `ws.runner` (`exec.Runner`), `ws.isDirty(ctx, dir)` (`update.go:228`), `branchInfo(ctx, dir)` (`status.go:89`).
- Produces:
  - `func (ws *Workspace) switchToDefaultBranch(ctx context.Context, repoDir, branch string) error` — `git -C <dir> switch <branch>`; refuses (returns error) if the tree is dirty.
  - `func (ws *Workspace) fastForwardIfBehind(ctx context.Context, repoDir, branch string) error` — `git -C <dir> merge --ff-only origin/<branch>`; returns error if not a fast-forward.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/branch_sync_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestSwitchToDefaultBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.switchToDefaultBranch(context.Background(), dir, "main"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if b := currentBranch(t, dir); b != "main" {
		t.Fatalf("want main, got %s", b)
	}
}

func TestSwitchToDefaultBranch_RefusesDirty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	dirtyTrackedFile(t, dir, "README.md", "changed\n")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.switchToDefaultBranch(context.Background(), dir, "main"); err == nil {
		t.Fatal("expected refusal on dirty tree")
	}
}

func TestFastForwardIfBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	setupLocalBareRemote(t, dir)
	// Advance the remote via a second clone, then reset local behind.
	want := addCommit(t, dir, "b.txt", "y", "add b")
	runGitT(t, dir, "push", "-q", "origin", "main")
	runGitT(t, dir, "reset", "-q", "--hard", "HEAD~1")
	runGitT(t, dir, "fetch", "-q", "origin")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.fastForwardIfBehind(context.Background(), dir, "main"); err != nil {
		t.Fatalf("ff: %v", err)
	}
	if got := headRev(t, dir); got != want {
		t.Fatalf("ff did not advance to remote: want %s got %s", want, got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestSwitchToDefaultBranch|TestFastForwardIfBehind' -v`
Expected: FAIL (`ws.switchToDefaultBranch undefined`).

- [ ] **Step 3: Implement**

```go
// internal/workspace/branch_sync.go
package workspace

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// switchToDefaultBranch checks out branch in repoDir. It refuses to switch a
// dirty working tree (tracked changes) so no local work is silently shelved.
func (ws *Workspace) switchToDefaultBranch(ctx context.Context, repoDir, branch string) error {
	if ws.isDirty(ctx, repoDir) {
		return fmt.Errorf("refusing to switch %s: working tree is dirty", repoDir)
	}
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "switch", branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git switch %s in %s: %w", branch, repoDir, err)
	}
	return nil
}

// fastForwardIfBehind fast-forwards branch to origin/<branch>. It uses
// --ff-only so a non-fast-forward (diverged/ahead) is an error, never a merge.
// Callers must have fetched first (doctor's refRev resolution does).
func (ws *Workspace) fastForwardIfBehind(ctx context.Context, repoDir, branch string) error {
	if _, err := ws.runner.Run(ctx, "git",
		[]string{"-C", repoDir, "merge", "--ff-only", "origin/" + branch}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("git merge --ff-only origin/%s in %s: %w", branch, repoDir, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestSwitchToDefaultBranch|TestFastForwardIfBehind' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/branch_sync.go modules/pn/internal/workspace/branch_sync_test.go
git commit -m "feat(pn): add switchToDefaultBranch + fastForwardIfBehind helpers"
```

---

### Task 4: Mode detection (`workspaceMode`)

**Files:**

- Create: `internal/workspace/doctor_mode.go`
- Test: `internal/workspace/doctor_mode_test.go`

**Interfaces:**

- Produces: `func (ws *Workspace) workspaceMode(ctx context.Context) string` returning `"worktree"` if any present member checkout is a linked worktree (its `git rev-parse --git-common-dir` resolves outside its own `.git`), else `"primary"`.

- [ ] **Step 1: Write the failing test (real git worktree)**

```go
// internal/workspace/doctor_mode_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestWorkspaceMode_Primary(t *testing.T) {
	root := t.TempDir()
	initRealRepo(t, filepath.Join(root, "repo-a"))
	ws := &Workspace{
		root:   root,
		runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}}},
	}
	if m := ws.workspaceMode(context.Background()); m != "primary" {
		t.Fatalf("want primary, got %s", m)
	}
}

func TestWorkspaceMode_Worktree(t *testing.T) {
	base := t.TempDir()
	canonical := filepath.Join(base, "canonical", "repo-a")
	initRealRepo(t, canonical)
	// Create a linked worktree of repo-a under a set dir.
	setRepo := filepath.Join(base, "set", "repo-a")
	runGitT(t, canonical, "worktree", "add", "-q", "-b", "feature", setRepo)
	ws := &Workspace{
		root:   filepath.Join(base, "set"),
		runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}}},
	}
	if m := ws.workspaceMode(context.Background()); m != "worktree" {
		t.Fatalf("want worktree, got %s", m)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestWorkspaceMode -v`
Expected: FAIL (`ws.workspaceMode undefined`).

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_mode.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// workspaceMode reports "worktree" when the resolved root's member checkouts
// are linked git worktrees, else "primary". Detection is intentionally behind
// this one function so the signal can change later.
//
// Signal: for a linked worktree, `git rev-parse --git-common-dir` points at the
// canonical repo's .git (outside this checkout), whereas for a normal clone it
// resolves to this checkout's own ".git". A submodule would also have a .git
// FILE, so we compare common-dir vs git-dir rather than stat'ing .git.
func (ws *Workspace) workspaceMode(ctx context.Context) string {
	for name := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if !dirExists(dir) {
			continue
		}
		gitDir := ws.gitRevParse(ctx, dir, "--git-dir")
		commonDir := ws.gitRevParse(ctx, dir, "--git-common-dir")
		if gitDir == "" || commonDir == "" {
			continue
		}
		if absUnder(dir, gitDir) != absUnder(dir, commonDir) {
			return "worktree"
		}
	}
	return "primary"
}

func (ws *Workspace) gitRevParse(ctx context.Context, dir, flag string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", flag}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// absUnder resolves p relative to base and returns the cleaned absolute path,
// so a relative ".git" and an absolute common-dir can be compared.
func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}
```

- [ ] **Step 4: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestWorkspaceMode -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/doctor_mode.go modules/pn/internal/workspace/doctor_mode_test.go
git commit -m "feat(pn): add workspaceMode detection (primary vs worktree)"
```

---

### Task 5: Reference-rev resolution (`resolveRefRevs`)

**Files:**

- Create: `internal/workspace/doctor_refrev.go`
- Test: `internal/workspace/doctor_refrev_test.go`

**Interfaces:**

- Consumes: `displayURL(RepoConfig)` (`canonical_url.go:141`), `captureHead(ctx, runner, dir)` (`update.go:239`), `ws.config`, `ws.root`, `ws.runner`.
- Produces: `func (ws *Workspace) resolveRefRevs(ctx context.Context, mode string, offline bool) (refRev map[string]string, skipped map[string]bool)` — for each repo: primary mode runs `git ls-remote <url> refs/heads/<branch>` (also `git -C <dir> fetch origin` so `fastForwardIfBehind` has the ref) and records the sha; `offline` or an unresolvable remote → `skipped[repo]=true`, `refRev[repo]=""`. Worktree mode → `refRev[repo]=captureHead(localdir)`, never skipped.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/doctor_refrev_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func newWS(t *testing.T, root string, repos map[string]RepoConfig) *Workspace {
	t.Helper()
	return &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: repos}}
}

func TestResolveRefRevs_PrimaryUsesRemote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo-a")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir)
	want := headRev(t, dir)
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: bare, Branch: "main"}})
	refRev, skipped := ws.resolveRefRevs(context.Background(), "primary", false)
	if skipped["repo-a"] {
		t.Fatal("repo-a unexpectedly skipped")
	}
	if refRev["repo-a"] != want {
		t.Fatalf("refRev: want %s got %s", want, refRev["repo-a"])
	}
}

func TestResolveRefRevs_OfflineSkips(t *testing.T) {
	root := t.TempDir()
	initRealRepo(t, filepath.Join(root, "repo-a"))
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: "git@x:o/r.git", Branch: "main"}})
	_, skipped := ws.resolveRefRevs(context.Background(), "primary", true)
	if !skipped["repo-a"] {
		t.Fatal("offline: repo-a should be skipped")
	}
}

func TestResolveRefRevs_WorktreeUsesLocalHead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo-a")
	initRealRepo(t, dir)
	want := headRev(t, dir)
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}})
	refRev, skipped := ws.resolveRefRevs(context.Background(), "worktree", false)
	if skipped["repo-a"] || refRev["repo-a"] != want {
		t.Fatalf("worktree refRev: want %s got %s (skipped=%v)", want, refRev["repo-a"], skipped["repo-a"])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestResolveRefRevs -v`
Expected: FAIL (`ws.resolveRefRevs undefined`).

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_refrev.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// resolveRefRevs computes the reference rev for each configured repo.
//
//	primary  : remote default-branch HEAD via `git ls-remote <url> refs/heads/<branch>`,
//	           plus a best-effort `git fetch origin` so fastForwardIfBehind can run.
//	           offline or unresolvable remote -> skipped[repo]=true.
//	worktree : the member checkout's committed HEAD (captureHead); never skipped.
func (ws *Workspace) resolveRefRevs(ctx context.Context, mode string, offline bool) (map[string]string, map[string]bool) {
	refRev := make(map[string]string, len(ws.config.Repos))
	skipped := make(map[string]bool)

	for name, rc := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if mode == "worktree" {
			if !dirExists(dir) {
				continue
			}
			if sha, err := captureHead(ctx, ws.runner, dir); err == nil {
				refRev[name] = sha
			}
			continue
		}
		// primary
		if offline {
			skipped[name] = true
			continue
		}
		url := displayURL(rc)
		branch := rc.Branch
		if branch == "" {
			branch = "main"
		}
		sha := ws.lsRemoteHead(ctx, url, branch)
		if sha == "" {
			skipped[name] = true
			continue
		}
		refRev[name] = sha
		// Best-effort fetch so origin/<branch> exists for ff checks/fixes.
		if dirExists(dir) {
			_, _ = ws.runner.Run(ctx, "git", []string{"-C", dir, "fetch", "-q", "origin", branch}, exec.RunOptions{})
		}
	}
	return refRev, skipped
}

// lsRemoteHead returns the sha that refs/heads/<branch> points to at url, or "".
func (ws *Workspace) lsRemoteHead(ctx context.Context, url, branch string) string {
	if url == "" {
		return ""
	}
	res, err := ws.runner.Run(ctx, "git",
		[]string{"ls-remote", url, "refs/heads/" + branch}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(res.Stdout))
	if line == "" {
		return ""
	}
	return strings.Fields(line)[0]
}
```

- [ ] **Step 4: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestResolveRefRevs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/doctor_refrev.go modules/pn/internal/workspace/doctor_refrev_test.go
git commit -m "feat(pn): add refRev resolution (ls-remote primary / local HEAD worktree)"
```

---

### Task 6: Doctor core — types, env, registry, orchestrator

**Files:**

- Create: `internal/workspace/doctor.go`
- Test: `internal/workspace/doctor_test.go`

**Interfaces:**

- Produces:
  - `type Severity int` with `SevWarning`, `SevError`; `func (Severity) String() string` → `"WARN"`/`"ERROR"`.
  - `type Finding struct { CheckID, Repo string; Severity Severity; Message, Manual string; Fixable bool; Skipped bool; fix func(context.Context) error }`.
  - `type DoctorReport struct { Mode string; Findings []Finding; Skipped []string }` with `func (r *DoctorReport) HasErrors() bool` and `func (r *DoctorReport) ExitCode(strict bool) int`.
  - `type DoctorOptions struct { Fix, DryRun, Offline, JSON, Strict bool; Terminal string }`.
  - `type doctorEnv struct { ws *Workspace; mode, terminal string; offline bool; refRev map[string]string; skipped map[string]bool; lock *Lock }`.
  - `type check struct { id string; run func(ctx context.Context, env *doctorEnv) []Finding }`.
  - `func (ws *Workspace) registerChecks() []check` (returns the registry; starts empty-ish, grows in later tasks).
  - `func Doctor(ctx context.Context, root string, runner exec.Runner, opts DoctorOptions) (*DoctorReport, error)` — Phase 1 (raw toml/lock) + Phase 2 (Open + run checks + optional fix). Defined fully here; structural checks are wired in Task 7, others appended to `registerChecks` in Tasks 8–11, fixes in Task 12.
- Consumes: `ParseConfig` (`config.go:141`), `ReadLock` (`lock.go:113`), `Open` (`workspace.go:33`), `effectiveLock` (`derive_lock.go:104`), `resolveRefRevs` (Task 5), `workspaceMode` (Task 4).

- [ ] **Step 1: Write the failing test (orchestrator runs a stub check)**

```go
// internal/workspace/doctor_test.go
package workspace

import (
	"context"
	"testing"
)

func TestSeverityString(t *testing.T) {
	if SevError.String() != "ERROR" || SevWarning.String() != "WARN" {
		t.Fatalf("severity strings wrong: %s %s", SevError, SevWarning)
	}
}

func TestReportExitCode(t *testing.T) {
	clean := &DoctorReport{}
	warn := &DoctorReport{Findings: []Finding{{Severity: SevWarning}}}
	err := &DoctorReport{Findings: []Finding{{Severity: SevError}}}
	if clean.ExitCode(false) != 0 {
		t.Fatal("clean -> 0")
	}
	if warn.ExitCode(false) != 0 {
		t.Fatal("warn (non-strict) -> 0")
	}
	if warn.ExitCode(true) != 1 {
		t.Fatal("warn (strict) -> 1")
	}
	if err.ExitCode(false) != 1 {
		t.Fatal("error -> 1")
	}
}

func TestDoctorOrchestratorRunsChecks(t *testing.T) {
	env := &doctorEnv{}
	c := check{id: "stub", run: func(_ context.Context, _ *doctorEnv) []Finding {
		return []Finding{{CheckID: "stub", Severity: SevWarning, Message: "hi"}}
	}}
	got := runChecks(context.Background(), env, []check{c})
	if len(got) != 1 || got[0].CheckID != "stub" {
		t.Fatalf("runChecks: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestSeverityString|TestReportExitCode|TestDoctorOrchestratorRunsChecks' -v`
Expected: FAIL (undefined `SevError`, `runChecks`, etc.).

- [ ] **Step 3: Implement the core**

```go
// internal/workspace/doctor.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

type Severity int

const (
	SevWarning Severity = iota
	SevError
)

func (s Severity) String() string {
	if s == SevError {
		return "ERROR"
	}
	return "WARN"
}

// Finding is one issue (or skipped check) the doctor reports. fix is non-nil
// only when the finding is safely auto-fixable.
type Finding struct {
	CheckID  string
	Repo     string // "" for workspace-level findings
	Severity Severity
	Message  string
	Manual   string // copy-pasteable command for non-auto-fixable findings
	Fixable  bool
	Skipped  bool
	fix      func(ctx context.Context) error
}

type DoctorReport struct {
	Mode     string
	Findings []Finding
	Skipped  []string // check IDs skipped (e.g. --offline)
}

func (r *DoctorReport) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SevError && !f.Skipped {
			return true
		}
	}
	return false
}

func (r *DoctorReport) hasAny() bool {
	for _, f := range r.Findings {
		if !f.Skipped {
			return true
		}
	}
	return false
}

// ExitCode maps the report to 0 (clean), 1 (errors, or any finding under strict).
// Code 2 (doctor itself failed) is returned by Doctor's error path, not here.
func (r *DoctorReport) ExitCode(strict bool) int {
	if r.HasErrors() {
		return 1
	}
	if strict && r.hasAny() {
		return 1
	}
	return 0
}

type DoctorOptions struct {
	Fix      bool
	DryRun   bool
	Offline  bool
	JSON     bool
	Strict   bool
	Terminal string
}

// doctorEnv is the shared context passed to every check.
type doctorEnv struct {
	ws       *Workspace
	mode     string // "primary" | "worktree"
	terminal string // resolved terminal repo key ("" if none)
	offline  bool
	refRev   map[string]string
	skipped  map[string]bool
	lock     *Lock // effective lock (derived if the disk lock is stale)
}

type check struct {
	id  string
	run func(ctx context.Context, env *doctorEnv) []Finding
}

// runChecks executes each check and concatenates findings, in registry order.
func runChecks(ctx context.Context, env *doctorEnv, checks []check) []Finding {
	var out []Finding
	for _, c := range checks {
		out = append(out, c.run(ctx, env)...)
	}
	return out
}

// Doctor audits the workspace rooted at root. Phase 1 reads toml/lock raw so a
// malformed file is diagnosable before Open() would fail; Phase 2 opens the
// workspace, runs the check registry, optionally applies fixes, and returns the
// report. The returned error is non-nil only when the doctor itself cannot run
// (mapped to exit 2 by the CLI).
func Doctor(ctx context.Context, root string, runner exec.Runner, opts DoctorOptions) (*DoctorReport, error) {
	report := &DoctorReport{}

	// --- Phase 1: structural, raw reads (no Open) ---
	tomlPath := filepath.Join(root, ConfigFileName)
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			CheckID: "toml-present", Severity: SevError,
			Message: fmt.Sprintf("%s missing or unreadable: %v", ConfigFileName, err),
		})
		return report, nil // cannot proceed; report carries the error finding
	}
	if _, perr := ParseConfig(data); perr != nil {
		report.Findings = append(report.Findings, Finding{
			CheckID: "toml-valid", Severity: SevError,
			Message: fmt.Sprintf("%s invalid: %v", ConfigFileName, perr),
		})
		return report, nil
	}

	// --- Phase 2: open + run checks ---
	ws, err := Open(root, runner)
	if err != nil {
		// Open can still fail (e.g. malformed lock). Surface as a structural finding.
		report.Findings = append(report.Findings, structuralOpenFailure(err))
		return report, nil
	}
	defer ws.Close()

	mode := ws.workspaceMode(ctx)
	report.Mode = mode
	refRev, skipped := ws.resolveRefRevs(ctx, mode, opts.Offline)
	effLock, _, _ := ws.effectiveLock(ctx) // best-effort; nil-safe checks handle a bad lock
	env := &doctorEnv{
		ws:       ws,
		mode:     mode,
		terminal: ws.resolveTerminalForDoctor(opts.Terminal),
		offline:  opts.Offline,
		refRev:   refRev,
		skipped:  skipped,
		lock:     effLock,
	}

	checks := ws.registerChecks()
	report.Findings = append(report.Findings, runChecks(ctx, env, checks)...)
	report.Skipped = collectSkipped(report.Findings)
	sortFindings(report.Findings)

	if opts.Fix {
		applyFixes(ctx, env, report, opts) // defined in doctor_fix.go (Task 12)
	}
	return report, nil
}

// resolveTerminalForDoctor returns the effective terminal: the --terminal flag
// if set, else workspace.terminal (may be "").
func (ws *Workspace) resolveTerminalForDoctor(flag string) string {
	if flag != "" {
		return flag
	}
	return ws.config.Workspace.Terminal
}

func structuralOpenFailure(err error) Finding {
	return Finding{
		CheckID: "lock-valid", Severity: SevError,
		Message: fmt.Sprintf("workspace failed to open (likely a malformed lock): %v", err),
		Manual:  "regenerate the lock:  pn workspace lock",
	}
}

func collectSkipped(findings []Finding) []string {
	seen := map[string]bool{}
	var ids []string
	for _, f := range findings {
		if f.Skipped && !seen[f.CheckID] {
			seen[f.CheckID] = true
			ids = append(ids, f.CheckID)
		}
	}
	sort.Strings(ids)
	return ids
}

// sortFindings orders errors before warnings, then by repo, then check id.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity > fs[j].Severity // SevError(1) before SevWarning(0)
		}
		if fs[i].Repo != fs[j].Repo {
			return fs[i].Repo < fs[j].Repo
		}
		return fs[i].CheckID < fs[j].CheckID
	})
}

// registerChecks returns the check registry. Checks are appended here as they
// are implemented (Tasks 7–11). Phase-1 structural toml checks already ran in
// Doctor(); lock-level structural checks (which need an opened workspace) live
// in the registry.
func (ws *Workspace) registerChecks() []check {
	return []check{
		// appended in later tasks
	}
}
```

- [ ] **Step 4: Add a temporary no-op `applyFixes` so the package compiles**

The real fix engine is Task 12; add a stub now so `doctor.go` compiles:

```go
// internal/workspace/doctor_fix.go  (temporary stub; replaced in Task 12)
package workspace

import "context"

func applyFixes(ctx context.Context, env *doctorEnv, report *DoctorReport, opts DoctorOptions) {}
```

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestSeverityString|TestReportExitCode|TestDoctorOrchestratorRunsChecks' -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/doctor.go modules/pn/internal/workspace/doctor_fix.go modules/pn/internal/workspace/doctor_test.go
git commit -m "feat(pn): doctor core types, env, orchestrator, registry"
```

---

### Task 7: Structural lock checks (`lock-present`, `lock-current`, `lock-legacy`)

**Files:**

- Create: `internal/workspace/doctor_checks_structural.go`
- Modify: `internal/workspace/doctor.go` (append to `registerChecks`)
- Test: `internal/workspace/doctor_checks_structural_test.go`

**Interfaces:**

- Consumes: `ws.lock` (disk lock), `lockMatchesConfig` (`derive_lock.go:117`), `deriveLock` (`derive_lock.go:24`), `LockFileName`/`LockFileNameLegacy` (`lock.go:52,55`), `env.lock` (effective).
- Produces: `func (ws *Workspace) checkLock(ctx, env) []Finding`.

- [ ] **Step 1: Write the failing tests (real files in temp workspace)**

```go
// internal/workspace/doctor_checks_structural_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckLock_MissingIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock()}
	env := &doctorEnv{ws: ws, mode: "primary", lock: emptyLock()}
	fs := ws.checkLock(context.Background(), env)
	if !hasFinding(fs, "lock-present", SevWarning) {
		t.Fatalf("expected lock-present warning, got %+v", fs)
	}
}

func TestCheckLock_LegacyIsWarning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, LockFileNameLegacy), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// also write a current lock so lock-present passes
	if err := os.WriteFile(filepath.Join(root, LockFileName), []byte(`{"order":[],"repos":{},"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock()}
	env := &doctorEnv{ws: ws, mode: "primary", lock: emptyLock()}
	fs := ws.checkLock(context.Background(), env)
	if !hasFinding(fs, "lock-legacy", SevWarning) {
		t.Fatalf("expected lock-legacy warning, got %+v", fs)
	}
}

// hasFinding is a shared test predicate (define once here).
func hasFinding(fs []Finding, id string, sev Severity) bool {
	for _, f := range fs {
		if f.CheckID == id && f.Severity == sev {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckLock -v`
Expected: FAIL (`ws.checkLock undefined`).

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_checks_structural.go
package workspace

import (
	"context"
	"path/filepath"
	"reflect"
)

// checkLock emits lock-present / lock-legacy / lock-current findings.
//
//	lock-present : warning when no lock.json on disk (effectiveLock derives it).
//	lock-legacy  : warning when the legacy pn-workspace.lock file is present.
//	lock-current : ERROR only when the disk lock's repo-set matches config (so
//	               effectiveLock + overrideInputArgsFor consume it as-is) but its
//	               edges/order differ from a fresh derive; otherwise no finding.
func (ws *Workspace) checkLock(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding
	lockPath := filepath.Join(ws.root, LockFileName)

	if !fileExists(lockPath) {
		fs = append(fs, Finding{
			CheckID: "lock-present", Severity: SevWarning,
			Message:  "pn-workspace.lock.json is absent (the DAG is derived dynamically)",
			Fixable:  true,
			fix:      func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
		})
	}
	if fileExists(filepath.Join(ws.root, LockFileNameLegacy)) {
		fs = append(fs, Finding{
			CheckID: "lock-legacy", Severity: SevWarning,
			Message:  "legacy pn-workspace.lock present; superseded by pn-workspace.lock.json",
			Fixable:  true,
			fix:      func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
		})
	}

	// lock-current: only meaningful when a disk lock exists and matches config.
	if ws.lock != nil && len(ws.lock.Repos) > 0 && lockMatchesConfig(ws.lock, ws.config) {
		fresh, _, err := deriveLock(ctx, ws, "")
		if err == nil && fresh != nil {
			if !reflect.DeepEqual(ws.lock.Edges, fresh.Edges) || !reflect.DeepEqual(ws.lock.Order, fresh.Order) {
				fs = append(fs, Finding{
					CheckID: "lock-current", Severity: SevError,
					Message:  "pn-workspace.lock.json is stale (edges/order differ from a fresh derive) and is consumed as-is",
					Fixable:  true,
					fix:      func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
				})
			}
		}
	}
	return fs
}
```

- [ ] **Step 4: Wire into the registry**

In `internal/workspace/doctor.go`, replace the body of `registerChecks`:

```go
func (ws *Workspace) registerChecks() []check {
	return []check{
		{id: "lock", run: ws.checkLock},
	}
}
```

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckLock -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/doctor_checks_structural.go modules/pn/internal/workspace/doctor_checks_structural_test.go modules/pn/internal/workspace/doctor.go
git commit -m "feat(pn): doctor lock-present/legacy/current checks"
```

---

### Task 8: Repo presence + identity checks

**Files:**

- Create: `internal/workspace/doctor_checks_repo.go`
- Modify: `internal/workspace/doctor.go` (`registerChecks`)
- Test: `internal/workspace/doctor_checks_repo_test.go`

**Interfaces:**

- Consumes: `isGitRepo` (`init.go:198`), `dirExists`/`fileExists` (`helpers.go`), `readGitRemotes` (`remotes.go:16`), `checkRemoteAgreement` (`sanity.go:16`), `reconcileFromFilesystem` (`init.go:301`), `Clone` (`clone.go:29`), `env.terminal`.
- Produces: `func (ws *Workspace) checkRepos(ctx, env) []Finding` emitting `repos-present`, `repos-extra`, `repo-is-git`, `repo-identity`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/doctor_checks_repo_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckRepos_MissingNonTerminalIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{
				"term": {URL: "u", Branch: "main"},
				"dep":  {URL: "u2", Branch: "main"},
			}}}
	initRealRepo(t, filepath.Join(root, "term")) // term present, dep missing
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-present", "dep", SevWarning) {
		t.Fatalf("missing non-terminal dep should be warning: %+v", fs)
	}
}

func TestCheckRepos_MissingTerminalIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos:     map[string]RepoConfig{"term": {URL: "u", Branch: "main"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-present", "term", SevError) {
		t.Fatalf("missing terminal should be error: %+v", fs)
	}
}

func TestCheckRepos_PresentNotGitIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}}
	if err := os.MkdirAll(filepath.Join(root, "dep"), 0o755); err != nil { // dir, no .git
		t.Fatal(err)
	}
	env := &doctorEnv{ws: ws, mode: "primary"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repo-is-git", "dep", SevError) {
		t.Fatalf("present-not-git should be error: %+v", fs)
	}
}

func TestCheckRepos_ExtraIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}}
	initRealRepo(t, filepath.Join(root, "stray"))
	env := &doctorEnv{ws: ws, mode: "primary"}
	fs := ws.checkRepos(context.Background(), env)
	if !hasFindingForRepo(fs, "repos-extra", "stray", SevWarning) {
		t.Fatalf("extra repo should be warning: %+v", fs)
	}
}

func hasFindingForRepo(fs []Finding, id, repo string, sev Severity) bool {
	for _, f := range fs {
		if f.CheckID == id && f.Repo == repo && f.Severity == sev {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckRepos -v`
Expected: FAIL (`ws.checkRepos undefined`).

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_checks_repo.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// checkRepos audits config↔disk agreement: missing repos, present-but-not-git
// dirs, extra on-disk repos, and origin/url identity.
func (ws *Workspace) checkRepos(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding

	// 1. Configured repos: present? a git repo? identity matches?
	for name, rc := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		switch {
		case !dirExists(dir):
			sev := SevWarning
			msg := fmt.Sprintf("repo %q is not cloned (its override is skipped; build falls back to flake.lock)", name)
			if name == env.terminal {
				sev = SevError
				msg = fmt.Sprintf("terminal repo %q is not cloned; apply/build cannot target it", name)
			}
			rcCopy := rc
			fs = append(fs, Finding{
				CheckID: "repos-present", Repo: name, Severity: sev, Message: msg, Fixable: true,
				fix: func(c context.Context) error { return ws.Clone(c, os.Stderr, CloneOptions{}) },
				Manual: "pn workspace clone",
			})
			_ = rcCopy
		case !isGitRepo(dir):
			fs = append(fs, Finding{
				CheckID: "repo-is-git", Repo: name, Severity: SevError,
				Message: fmt.Sprintf("repo %q exists on disk but is not a git repo", name),
				Manual:  fmt.Sprintf("rm -rf %s && pn workspace clone", dir),
			})
		default:
			if f := ws.checkRepoIdentity(ctx, name, rc, dir); f != nil {
				fs = append(fs, *f)
			}
		}
	}

	// 2. Extra on-disk repos not in config.
	fs = append(fs, ws.checkExtraRepos()...)
	return fs
}

func (ws *Workspace) checkRepoIdentity(ctx context.Context, name string, rc RepoConfig, dir string) *Finding {
	remotes, err := readGitRemotes(ctx, ws.runner, dir)
	if err != nil {
		return nil // tolerate; readGitRemotes already degrades gracefully
	}
	if err := checkRemoteAgreement(name, rc, remotes); err != nil {
		return &Finding{
			CheckID: "repo-identity", Repo: name, Severity: SevError,
			Message: err.Error(),
			Manual:  fmt.Sprintf("align the remote or pn-workspace.toml, e.g.:  git -C %s remote set-url origin %s", dir, displayURL(rc)),
		}
	}
	return nil
}

// checkExtraRepos flags on-disk git repos at the workspace root that are not in
// config. Fix: reconcileFromFilesystem (adds git repos with a resolvable origin).
func (ws *Workspace) checkExtraRepos() []Finding {
	entries, err := os.ReadDir(ws.root)
	if err != nil {
		return nil
	}
	var fs []Finding
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, configured := ws.config.Repos[name]; configured {
			continue
		}
		dir := filepath.Join(ws.root, name)
		if !isGitRepo(dir) {
			continue // not a repo; ignore (.worktrees, .beads, docs, etc.)
		}
		fs = append(fs, Finding{
			CheckID: "repos-extra", Repo: name, Severity: SevWarning,
			Message: fmt.Sprintf("git repo %q is on disk but not in pn-workspace.toml", name),
			Fixable: true,
			fix:     func(c context.Context) error { return ws.reconcileFromFilesystem(c) },
			Manual:  "pn workspace init",
		})
	}
	return fs
}
```

Note: `reconcileFromFilesystem` is best-effort (adds only git repos with a resolvable origin); an origin-less extra repo stays reported after `--fix` and the re-run shows it again — acceptable per spec decision 4.

- [ ] **Step 4: Wire into registry**

In `doctor.go` `registerChecks`, add `{id: "repos", run: ws.checkRepos},`.

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckRepos -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/doctor_checks_repo.go modules/pn/internal/workspace/doctor_checks_repo_test.go modules/pn/internal/workspace/doctor.go
git commit -m "feat(pn): doctor repos-present/extra/is-git/identity checks"
```

---

### Task 9: Branch + tree checks

**Files:**

- Create: `internal/workspace/doctor_checks_branch.go`
- Modify: `internal/workspace/doctor.go` (`registerChecks`)
- Test: `internal/workspace/doctor_checks_branch_test.go`

**Interfaces:**

- Consumes: `branchInfo` (`status.go:89`), `ws.isDirty` (`update.go:228`), `env.refRev`, `env.skipped`, `env.mode`, `RepoConfig.Branch`, `switchToDefaultBranch`/`fastForwardIfBehind` (Task 3), `captureHead`.
- Produces: `func (ws *Workspace) checkBranches(ctx, env) []Finding` emitting `branch-current`/`branch-uniform`, `branch-synced` (primary), `tree-clean`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/doctor_checks_branch_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckBranches_WrongBranchIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	fs := ws.checkBranches(context.Background(), env)
	if !hasFindingForRepo(fs, "branch-current", "dep", SevError) {
		t.Fatalf("wrong branch should be error: %+v", fs)
	}
}

func TestCheckBranches_DirtyIsErrorPrimaryWarningWorktree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	dirtyTrackedFile(t, dir, "README.md", "changed\n")
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}
	ws := &Workspace{root: root, runner: exec.NewRealRunner(), config: cfg}

	envP := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envP), "tree-clean", "dep", SevError) {
		t.Fatal("dirty primary should be error")
	}
	envW := &doctorEnv{ws: ws, mode: "worktree", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envW), "tree-clean", "dep", SevWarning) {
		t.Fatal("dirty worktree should be warning")
	}
}

func TestCheckBranches_AheadOfRemoteIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}}
	// refRev (remote) differs from local HEAD => not synced.
	env := &doctorEnv{ws: ws, mode: "primary",
		refRev:  map[string]string{"dep": "0000000000000000000000000000000000000000"},
		skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), env), "branch-synced", "dep", SevError) {
		t.Fatal("local != remote should be branch-synced error")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckBranches -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_checks_branch.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkBranches audits branch placement, local-vs-remote sync, and tree
// cleanliness. Worktree mode relaxes: branch-uniform instead of branch-current,
// branch-synced dropped, tree-clean is a warning.
func (ws *Workspace) checkBranches(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding
	present := ws.presentRepoDirs()

	if env.mode == "worktree" {
		fs = append(fs, ws.checkBranchUniform(ctx, present)...)
	}

	for name, dir := range present {
		rc := ws.config.Repos[name]
		branch := rc.Branch
		if branch == "" {
			branch = "main"
		}

		// branch-current (primary only; worktree uniformity handled above)
		if env.mode == "primary" {
			cur, detached, _, ok := ws.branchInfo(ctx, dir)
			if ok && (detached || cur != branch) {
				fs = append(fs, Finding{
					CheckID: "branch-current", Repo: name, Severity: SevError,
					Message: fmt.Sprintf("repo %q is not on its default branch %q (on %q)", name, branch, branchOrDetached(cur, detached)),
					Fixable: true,
					fix: func(c context.Context) error {
						return ws.switchToDefaultBranch(c, dir, branch)
					},
					Manual: fmt.Sprintf("git -C %s switch %s", dir, branch),
				})
			}
		}

		// tree-clean
		if ws.isDirty(ctx, dir) {
			sev := SevError
			if env.mode == "worktree" {
				sev = SevWarning
			}
			fs = append(fs, Finding{
				CheckID: "tree-clean", Repo: name, Severity: sev,
				Message: fmt.Sprintf("repo %q has uncommitted tracked changes (local build would differ from remote)", name),
				Manual:  fmt.Sprintf("commit or stash:  git -C %s stash", dir),
			})
		}

		// branch-synced (primary only)
		if env.mode == "primary" {
			if env.skipped[name] {
				fs = append(fs, Finding{CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote comparison skipped"})
				continue
			}
			ref := env.refRev[name]
			if ref == "" {
				fs = append(fs, Finding{CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote rev unresolved (no upstream?)"})
				continue
			}
			local, err := captureHead(ctx, ws.runner, dir)
			if err == nil && local != ref {
				fs = append(fs, ws.branchSyncedFinding(ctx, name, dir, branch, local, ref))
			}
		}
	}
	return fs
}

func (ws *Workspace) branchSyncedFinding(ctx context.Context, name, dir, branch, local, ref string) Finding {
	behind := ws.isStrictlyBehind(ctx, dir, branch)
	f := Finding{
		CheckID: "branch-synced", Repo: name, Severity: SevError,
		Message: fmt.Sprintf("repo %q local HEAD %s != remote %s (%s)", name, short(local), short(ref), ws.aheadBehind(ctx, dir)),
	}
	if behind {
		f.Fixable = true
		f.fix = func(c context.Context) error { return ws.fastForwardIfBehind(c, dir, branch) }
		f.Manual = fmt.Sprintf("git -C %s merge --ff-only origin/%s", dir, branch)
	} else {
		f.Manual = fmt.Sprintf("local diverged/ahead — resolve by hand:  git -C %s rebase origin/%s", dir, branch)
	}
	return f
}

// isStrictlyBehind reports whether HEAD is an ancestor of origin/<branch>
// (i.e. a fast-forward is possible). Requires a prior fetch (refRev did it).
func (ws *Workspace) isStrictlyBehind(ctx context.Context, dir, branch string) bool {
	_, err := ws.runner.Run(ctx, "git",
		[]string{"-C", dir, "merge-base", "--is-ancestor", "HEAD", "origin/" + branch}, exec.RunOptions{})
	return err == nil
}

// checkBranchUniform (worktree mode) verifies all present members share one
// branch name; a member on a different branch is a branch-uniform error, and a
// uniform branch that differs from the set-dir name is a naming-hygiene warning.
func (ws *Workspace) checkBranchUniform(ctx context.Context, present map[string]string) []Finding {
	branches := map[string]string{} // repo -> branch
	counts := map[string]int{}
	for name, dir := range present {
		cur, detached, _, ok := ws.branchInfo(ctx, dir)
		if !ok || detached {
			cur = "(detached)"
		}
		branches[name] = cur
		counts[cur]++
	}
	if len(counts) <= 1 {
		// uniform; optionally compare to set-dir name
		setName := filepath.Base(ws.root)
		for _, b := range branches {
			if b != setName && b != "(detached)" {
				return []Finding{{CheckID: "branch-uniform", Severity: SevWarning,
					Message: fmt.Sprintf("worktree members are on %q but the set dir is %q", b, setName)}}
			}
			break
		}
		return nil
	}
	var fs []Finding
	for name, b := range branches {
		fs = append(fs, Finding{CheckID: "branch-uniform", Repo: name, Severity: SevError,
			Message: fmt.Sprintf("worktree member %q is on %q; members must share one branch", name, b),
			Manual:  fmt.Sprintf("git -C %s switch <set-branch>", filepath.Join(ws.root, name))})
	}
	return fs
}

// presentRepoDirs returns configured repos that exist as git repos on disk.
func (ws *Workspace) presentRepoDirs() map[string]string {
	out := map[string]string{}
	for name := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if isGitRepo(dir) {
			out[name] = dir
		}
	}
	return out
}

func branchOrDetached(cur string, detached bool) string {
	if detached {
		return "(detached HEAD)"
	}
	return cur
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
```

Add `"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"` to the import block (used by `isStrictlyBehind`).

- [ ] **Step 4: Wire into registry**

In `doctor.go` `registerChecks`, add `{id: "branches", run: ws.checkBranches},`.

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckBranches -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/doctor_checks_branch.go modules/pn/internal/workspace/doctor_checks_branch_test.go modules/pn/internal/workspace/doctor.go
git commit -m "feat(pn): doctor branch-current/uniform/synced + tree-clean checks"
```

---

### Task 10: Terminal, follows, and flake-path checks

**Files:**

- Create: `internal/workspace/doctor_checks_terminal.go`
- Modify: `internal/workspace/doctor.go` (`registerChecks`)
- Test: `internal/workspace/doctor_checks_terminal_test.go`

**Interfaces:**

- Consumes: `deriveLock` (returns `[]ValidationError` with `.Code`/`.Message`), `checkFollows(terminalDir, inputNames)` (`follows.go:27`), `workspaceInputNamesFromEdges` (`helpers.go:97`), `resolveFlakePath` (`flake_path.go:22`), `env.lock`, `env.terminal`, `terminalWarningMessage` (`terminal_guard.go:28`).
- Produces: `func (ws *Workspace) checkTerminal(ctx, env) []Finding` emitting `terminal-resolvable`, `follows-correct`, `flake-path-resolves`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/doctor_checks_terminal_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckTerminal_NoTerminalIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "", lock: emptyLock()}
	if !hasFinding(ws.checkTerminal(context.Background(), env), "terminal-resolvable", SevError) {
		t.Fatal("no terminal should be terminal-resolvable error")
	}
}

func TestCheckTerminal_FollowsViolationIsError(t *testing.T) {
	root := t.TempDir()
	term := filepath.Join(root, "term")
	initRealRepo(t, term)
	// flake.lock where workspace inputs a and b do NOT follow each other.
	lock := `{"nodes":{"root":{"inputs":{"a":"a","b":"b"}},"a":{"inputs":{"b":"b"}},"b":{}}}`
	if err := os.WriteFile(filepath.Join(term, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{"term": {URL: "u", Branch: "main"}}},
		lock: &Lock{Terminal: "term",
			Edges: []LockEdge{{Consumer: "term", Alias: "a", Target: "x"}, {Consumer: "term", Alias: "b", Target: "y"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term", lock: ws.lock}
	if !hasFinding(ws.checkTerminal(context.Background(), env), "follows-correct", SevError) {
		t.Fatal("unfollowed workspace input should be follows-correct error")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckTerminal -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_checks_terminal.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkTerminal audits the terminal's resolvability, the follows-correctness of
// its workspace inputs, and flake-path drift in the lock.
func (ws *Workspace) checkTerminal(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding

	// terminal-resolvable: surface deriveLock validation errors (missing_terminal,
	// terminal_not_sink, missing_flake_path).
	if _, verrs, err := deriveLock(ctx, ws, env.terminal); err == nil {
		for _, ve := range verrs {
			fs = append(fs, Finding{
				CheckID: "terminal-resolvable", Severity: SevError,
				Message: fmt.Sprintf("%s: %s", ve.Code, ve.Message),
				Manual:  terminalWarningMessage,
			})
		}
	}

	// follows-correct: only when the terminal is present on disk.
	if env.terminal != "" {
		termDir := filepath.Join(ws.root, env.terminal)
		if isGitRepo(termDir) {
			names := ws.workspaceInputNamesFromEdges(env.terminal)
			if err := checkFollows(termDir, names); err != nil {
				fs = append(fs, Finding{
					CheckID: "follows-correct", Repo: env.terminal, Severity: SevError,
					Message: err.Error(),
					Manual:  "edit the terminal flake.nix to add the inputs.<a>.inputs.<b>.follows lines shown above, then re-lock",
				})
			}
		}
	}

	// flake-path-resolves: lock's recorded FlakePath must match on-disk resolution.
	if env.lock != nil {
		for name := range ws.config.Repos {
			dir := filepath.Join(ws.root, name)
			if !isGitRepo(dir) {
				continue
			}
			recorded := env.lock.Repos[name].FlakePath
			actual := ws.resolveFlakePath(name)
			if recorded != "" && actual != "" && recorded != actual {
				rec := recorded
				fs = append(fs, Finding{
					CheckID: "flake-path-resolves", Repo: name, Severity: SevError,
					Message: fmt.Sprintf("repo %q lock flake_path %q != on-disk %q (wrong flake would be evaluated)", name, rec, actual),
					Fixable: true,
					fix:     func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
					Manual:  "pn workspace lock",
				})
			}
		}
	}
	return fs
}
```

- [ ] **Step 4: Wire into registry**

In `doctor.go` `registerChecks`, add `{id: "terminal", run: ws.checkTerminal},`.

- [ ] **Step 5: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckTerminal -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/doctor_checks_terminal.go modules/pn/internal/workspace/doctor_checks_terminal_test.go modules/pn/internal/workspace/doctor.go
git commit -m "feat(pn): doctor terminal-resolvable/follows-correct/flake-path checks"
```

---

### Task 11: `flake-lock-fresh` check

**Files:**

- Create: `internal/workspace/doctor_checks_flakelock.go`
- Modify: `internal/workspace/doctor.go` (`registerChecks`)
- Test: `internal/workspace/doctor_checks_flakelock_test.go`

**Interfaces:**

- Consumes: `env.lock` (effective), `workspaceAliasesFromLock(lock, consumer)` (`propagate.go:20`), `readAliasRevs(lockPath, aliases)` (`propagate.go:52`), `resolveFlakePath`, `env.refRev`, `env.skipped`.
- Produces: `func (ws *Workspace) checkFlakeLockFresh(ctx, env) []Finding` emitting `flake-lock-fresh` per stale edge. Fix delegates to `pn workspace update` for affected consumers (the only pushing fix).

- [ ] **Step 1: Write the failing test (pure-file: real flake.lock + lock edges)**

```go
// internal/workspace/doctor_checks_flakelock_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckFlakeLockFresh_StaleIsError(t *testing.T) {
	root := t.TempDir()
	consumer := filepath.Join(root, "consumer")
	initRealRepo(t, consumer)
	// consumer's flake.lock pins input "dep" at an OLD rev.
	old := "1111111111111111111111111111111111111111"
	lock := `{"nodes":{"root":{"inputs":{"dep":"dep"}},"dep":{"locked":{"rev":"` + old + `"}}}}`
	if err := os.WriteFile(filepath.Join(consumer, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "2222222222222222222222222222222222222222"
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"consumer": {URL: "u1", Branch: "main"}, "dep": {URL: "u2", Branch: "main"}}},
		lock: &Lock{
			Repos: map[string]LockRepoEntry{"consumer": {FlakePath: "flake.nix"}, "dep": {FlakePath: "flake.nix"}},
			Edges: []LockEdge{{Consumer: "consumer", Alias: "dep", Target: "dep"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", lock: ws.lock,
		refRev: map[string]string{"dep": want, "consumer": "x"}, skipped: map[string]bool{}}
	fs := ws.checkFlakeLockFresh(context.Background(), env)
	if !hasFindingForRepo(fs, "flake-lock-fresh", "consumer", SevError) {
		t.Fatalf("stale flake.lock should be error: %+v", fs)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckFlakeLockFresh -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_checks_flakelock.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkFlakeLockFresh verifies each consumer's flake.lock pins every workspace
// edge to refRev(target). The per-alias rev read is nix-free; the edge set comes
// from env.lock (effective — may have been derived via nix when the disk lock
// was stale). The fix delegates to `pn workspace update` (relock→commit→push).
func (ws *Workspace) checkFlakeLockFresh(ctx context.Context, env *doctorEnv) []Finding {
	if env.lock == nil {
		return nil
	}
	var fs []Finding
	staleConsumers := map[string]bool{}

	for consumer := range ws.config.Repos {
		consumerDir := filepath.Join(ws.root, consumer)
		if !isGitRepo(consumerDir) {
			continue
		}
		aliases := workspaceAliasesFromLock(env.lock, consumer)
		if len(aliases) == 0 {
			continue
		}
		flakeRel := ws.resolveFlakePath(consumer)
		if flakeRel == "" {
			continue
		}
		lockPath := filepath.Join(consumerDir, filepath.Dir(flakeRel), "flake.lock")
		locked, err := readAliasRevs(lockPath, aliases)
		if err != nil {
			continue
		}
		for _, alias := range aliases {
			target := ws.edgeTarget(env.lock, consumer, alias)
			if target == "" {
				continue
			}
			if env.skipped[target] {
				fs = append(fs, Finding{CheckID: "flake-lock-fresh", Repo: consumer, Severity: SevError,
					Skipped: true, Message: fmt.Sprintf("freshness of input %q skipped (remote of %q unresolved)", alias, target)})
				continue
			}
			want := env.refRev[target]
			got := locked[alias]
			if want == "" || got == "" {
				continue
			}
			if got != want {
				staleConsumers[consumer] = true
				fs = append(fs, Finding{
					CheckID: "flake-lock-fresh", Repo: consumer, Severity: SevError,
					Message: fmt.Sprintf("flake.lock input %q (→ %q) pins %s but %q is at %s", alias, target, short(got), target, short(want)),
				})
			}
		}
	}

	// Attach a single update-delegating fix to the first finding per consumer.
	attachFlakeLockFix(ws, fs, staleConsumers)
	return fs
}

func (ws *Workspace) edgeTarget(lock *Lock, consumer, alias string) string {
	for _, e := range lock.Edges {
		if e.Consumer == consumer && e.Alias == alias {
			return e.Target
		}
	}
	return ""
}

// attachFlakeLockFix marks the first flake-lock-fresh finding fixable; the fix
// runs `pn workspace update` (the proven relock→commit→push, topo-ordered flow).
// This is the ONLY fix that pushes — acceptable per spec decision 9.
func attachFlakeLockFix(ws *Workspace, fs []Finding, stale map[string]bool) {
	done := map[string]bool{}
	for i := range fs {
		if fs[i].CheckID != "flake-lock-fresh" || fs[i].Skipped {
			continue
		}
		c := fs[i].Repo
		fs[i].Manual = "pn workspace update"
		if stale[c] && !done[c] {
			done[c] = true
			fs[i].Fixable = true
			fs[i].fix = func(ctx context.Context) error {
				return ws.Update(ctx, osStderr(), UpdateOptions{InPlace: true})
			}
		}
	}
}
```

- [ ] **Step 4: Add the `osStderr` helper (avoid importing os in every checks file)**

In `doctor.go`, add:

```go
import "os" // already imported in doctor.go

func osStderr() *os.File { return os.Stderr }
```

(If `os` is already imported in `doctor.go`, just add the `osStderr` func.)

- [ ] **Step 5: Wire into registry**

In `doctor.go` `registerChecks`, add `{id: "flake-lock", run: ws.checkFlakeLockFresh},`.

- [ ] **Step 6: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestCheckFlakeLockFresh -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add modules/pn/internal/workspace/doctor_checks_flakelock.go modules/pn/internal/workspace/doctor_checks_flakelock_test.go modules/pn/internal/workspace/doctor.go
git commit -m "feat(pn): doctor flake-lock-fresh check (fix via pn workspace update)"
```

---

### Task 12: Fix engine (`--fix`, `--dry-run`, ordering, re-run)

**Files:**

- Modify (replace stub): `internal/workspace/doctor_fix.go`
- Test: `internal/workspace/doctor_fix_test.go`

**Interfaces:**

- Consumes: the `Finding.fix` funcs set by checks; `runChecks` + `ws.registerChecks` (to re-run); `env`.
- Produces:
  - `func applyFixes(ctx context.Context, env *doctorEnv, report *DoctorReport, opts DoctorOptions)` — applies fixable findings in dependency order; on `DryRun`, records the plan into `report` and mutates nothing; otherwise runs each fix, marks it applied, then re-runs all checks and replaces `report.Findings` with the residual set.
  - `func fixOrder(checkID string) int` — the dependency rank used to order fixes.
  - A `Finding.Applied bool` field (add to the struct in `doctor.go`) and a `Plan []string` field on `DoctorReport`.

- [ ] **Step 1: Add fields to existing types**

In `doctor.go`: add `Applied bool` to `Finding`, and `Plan []string` to `DoctorReport`.

- [ ] **Step 2: Write the failing tests**

```go
// internal/workspace/doctor_fix_test.go
package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestFixOrderRanks(t *testing.T) {
	if !(fixOrder("repos-present") < fixOrder("lock-present") &&
		fixOrder("lock-present") < fixOrder("flake-lock-fresh")) {
		t.Fatal("fix order ranks wrong")
	}
}

func TestApplyFixes_DryRunMutatesNothing(t *testing.T) {
	ran := false
	report := &DoctorReport{Findings: []Finding{
		{CheckID: "lock-present", Severity: SevWarning, Fixable: true,
			fix: func(context.Context) error { ran = true; return nil }},
	}}
	env := &doctorEnv{ws: &Workspace{config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}}}
	applyFixes(context.Background(), env, report, DoctorOptions{Fix: true, DryRun: true})
	if ran {
		t.Fatal("dry-run must not execute fixes")
	}
	if len(report.Plan) == 0 {
		t.Fatal("dry-run must record a plan")
	}
}

func TestApplyFixes_RunsInOrderAndReRuns(t *testing.T) {
	var order []string
	mk := func(id string) Finding {
		return Finding{CheckID: id, Severity: SevError, Fixable: true,
			fix: func(context.Context) error { order = append(order, id); return nil }}
	}
	report := &DoctorReport{Findings: []Finding{mk("flake-lock-fresh"), mk("repos-present")}}
	// stub registry so the re-run returns no findings
	ws := &Workspace{config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, root: t.TempDir(),
		registerChecksFn: func() []check { return nil }}
	env := &doctorEnv{ws: ws}
	applyFixes(context.Background(), env, report, DoctorOptions{Fix: true})
	if len(order) != 2 || order[0] != "repos-present" || order[1] != "flake-lock-fresh" {
		t.Fatalf("fixes ran out of order: %v", order)
	}
}

func TestApplyFixes_FixErrorIsReported(t *testing.T) {
	report := &DoctorReport{Findings: []Finding{
		{CheckID: "lock-present", Severity: SevWarning, Fixable: true,
			fix: func(context.Context) error { return errors.New("boom") }},
	}}
	ws := &Workspace{config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, root: t.TempDir(),
		registerChecksFn: func() []check { return nil }}
	applyFixes(context.Background(), &doctorEnv{ws: ws}, report, DoctorOptions{Fix: true})
	if !hasFinding(report.Findings, "fix-failed", SevError) {
		t.Fatalf("fix error should surface as fix-failed: %+v", report.Findings)
	}
}
```

- [ ] **Step 3: Add an overridable registry hook to `Workspace` (for the re-run + tests)**

In `doctor.go`, change `registerChecks` to consult an optional override field so tests can stub the re-run:

```go
// add to the Workspace struct in workspace.go:
//   registerChecksFn func() []check   // nil in production; set in tests
//
// then in doctor.go:
func (ws *Workspace) registerChecks() []check {
	if ws.registerChecksFn != nil {
		return ws.registerChecksFn()
	}
	return []check{
		{id: "lock", run: ws.checkLock},
		{id: "repos", run: ws.checkRepos},
		{id: "branches", run: ws.checkBranches},
		{id: "terminal", run: ws.checkTerminal},
		{id: "flake-lock", run: ws.checkFlakeLockFresh},
	}
}
```

Add the field `registerChecksFn func() []check` to the `Workspace` struct in `workspace.go`.

- [ ] **Step 4: Implement the fix engine**

```go
// internal/workspace/doctor_fix.go  (replaces the Task 6 stub)
package workspace

import (
	"context"
	"fmt"
	"sort"
)

// fixOrder ranks a check's fix in the dependency order required for a coherent
// world: clone -> reconcile -> switch -> ff-pull -> lock -> flake-lock(update).
func fixOrder(checkID string) int {
	switch checkID {
	case "repos-present":
		return 0
	case "repos-extra":
		return 1
	case "branch-current", "branch-uniform":
		return 2
	case "branch-synced":
		return 3
	case "lock-present", "lock-legacy", "lock-current", "flake-path-resolves":
		return 4
	case "flake-lock-fresh":
		return 5
	default:
		return 9
	}
}

// applyFixes applies the fixable findings (dependency order). On DryRun it only
// records report.Plan. Otherwise it runs each fix, then re-runs all checks and
// replaces report.Findings with the residual set (so the caller sees what's left).
func applyFixes(ctx context.Context, env *doctorEnv, report *DoctorReport, opts DoctorOptions) {
	fixable := make([]Finding, 0, len(report.Findings))
	for _, f := range report.Findings {
		if f.Fixable && f.fix != nil && !f.Skipped {
			fixable = append(fixable, f)
		}
	}
	sort.SliceStable(fixable, func(i, j int) bool {
		return fixOrder(fixable[i].CheckID) < fixOrder(fixable[j].CheckID)
	})

	if opts.DryRun {
		for _, f := range fixable {
			label := f.CheckID
			if f.Repo != "" {
				label += " (" + f.Repo + ")"
			}
			report.Plan = append(report.Plan, "would fix: "+label+" — "+planAction(f.CheckID))
		}
		return
	}

	var fixErrs []Finding
	for _, f := range fixable {
		if err := f.fix(ctx); err != nil {
			fixErrs = append(fixErrs, Finding{
				CheckID: "fix-failed", Repo: f.Repo, Severity: SevError,
				Message: fmt.Sprintf("fixing %s failed: %v", f.CheckID, err),
			})
		}
	}

	// Re-run all checks against a freshly recomputed view of the workspace.
	residual := runChecks(ctx, env, env.ws.registerChecks())
	residual = append(residual, fixErrs...)
	sortFindings(residual)
	report.Findings = residual
	report.Skipped = collectSkipped(residual)
}

// planAction returns the existing command a fix delegates to (for --dry-run).
func planAction(checkID string) string {
	switch checkID {
	case "repos-present":
		return "pn workspace clone"
	case "repos-extra":
		return "pn workspace init (reconcile)"
	case "branch-current", "branch-uniform":
		return "git switch <branch>"
	case "branch-synced":
		return "git merge --ff-only origin/<branch>"
	case "lock-present", "lock-legacy", "lock-current", "flake-path-resolves":
		return "pn workspace lock (WriteDerivedLock)"
	case "flake-lock-fresh":
		return "pn workspace update (relock→commit→push)"
	default:
		return "(delegated fix)"
	}
}
```

Note: the re-run re-reads on-disk state via the same check functions, but `env.refRev`/`env.lock` were memoized before the fixes. For correctness of the residual report after a ff-pull/update, recompute them: see Step 5.

- [ ] **Step 5: Recompute env before the re-run**

Update `applyFixes` to refresh `env.refRev`/`env.skipped`/`env.lock` after fixes (before the re-run), since ff-pull/update changed remote/local state:

```go
	// (insert before `residual := runChecks(...)`)
	env.refRev, env.skipped = env.ws.resolveRefRevs(ctx, env.mode, env.offline)
	if l, _, err := env.ws.effectiveLock(ctx); err == nil {
		env.lock = l
	}
```

- [ ] **Step 6: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestFixOrderRanks|TestApplyFixes' -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 7: Commit**

```bash
git add modules/pn/internal/workspace/doctor_fix.go modules/pn/internal/workspace/doctor_fix_test.go modules/pn/internal/workspace/doctor.go modules/pn/internal/workspace/workspace.go
git commit -m "feat(pn): doctor fix engine (--fix/--dry-run, ordering, re-run)"
```

---

### Task 13: Rendering (human + JSON) and exit code

**Files:**

- Create: `internal/workspace/doctor_render.go`
- Test: `internal/workspace/doctor_render_test.go`

**Interfaces:**

- Consumes: `DoctorReport`, `DoctorOptions`, `colorEnabled` (`tree.go:191`).
- Produces:
  - `func RenderDoctor(w io.Writer, report *DoctorReport, opts DoctorOptions) error` — JSON (when `opts.JSON`) or the human report.
  - JSON shape: `{"mode": "...", "findings": [{"check","repo","severity","message","fixable","manual","skipped"}...], "skipped": [...]}`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/workspace/doctor_render_test.go
package workspace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderDoctor_JSONOnlyFindings(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Findings: []Finding{
		{CheckID: "tree-clean", Repo: "dep", Severity: SevError, Message: "dirty"},
	}}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if out["mode"] != "primary" {
		t.Fatalf("mode missing: %v", out)
	}
	if strings.Contains(buf.String(), "===") {
		t.Fatal("JSON output must not contain human chrome")
	}
}

func TestRenderDoctor_HumanCleanRun(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, &DoctorReport{Mode: "primary"}, DoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no errors") {
		t.Fatalf("clean run should reassure: %q", buf.String())
	}
}

func TestRenderDoctor_HumanGroupsAndMarks(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Findings: []Finding{
		{CheckID: "branch-synced", Repo: "dep", Severity: SevError, Message: "ahead", Manual: "git ..."},
		{CheckID: "repos-extra", Repo: "stray", Severity: SevWarning, Message: "extra", Fixable: true},
	}}
	var buf bytes.Buffer
	_ = RenderDoctor(&buf, r, DoctorOptions{})
	s := buf.String()
	for _, want := range []string{"=== dep ===", "=== stray ===", "ERROR", "WARN", "[manual]", "[fixable]"} {
		if !strings.Contains(s, want) {
			t.Fatalf("human output missing %q in:\n%s", want, s)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestRenderDoctor -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/workspace/doctor_render.go
package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type jsonFinding struct {
	Check    string `json:"check"`
	Repo     string `json:"repo,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Fixable  bool   `json:"fixable"`
	Manual   string `json:"manual,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// RenderDoctor writes the report to w as JSON (opts.JSON) or a human report.
func RenderDoctor(w io.Writer, report *DoctorReport, opts DoctorOptions) error {
	if opts.JSON {
		out := struct {
			Mode     string        `json:"mode"`
			Findings []jsonFinding `json:"findings"`
			Skipped  []string      `json:"skipped"`
			Plan     []string      `json:"plan,omitempty"`
		}{Mode: report.Mode, Skipped: report.Skipped, Plan: report.Plan}
		for _, f := range report.Findings {
			out.Findings = append(out.Findings, jsonFinding{
				Check: f.CheckID, Repo: f.Repo, Severity: f.Severity.String(),
				Message: f.Message, Fixable: f.Fixable, Manual: f.Manual, Skipped: f.Skipped,
			})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return renderHuman(w, report, opts)
}

func renderHuman(w io.Writer, report *DoctorReport, opts DoctorOptions) error {
	// Mode banner.
	if report.Mode == "worktree" {
		fmt.Fprintln(w, "workspace doctor — worktree set (relaxed: dirty=warning, remote-sync not checked)")
	} else {
		fmt.Fprintln(w, "workspace doctor — primary checkouts (origin/<branch> is the baseline)")
	}

	// Dry-run plan.
	if opts.DryRun && len(report.Plan) > 0 {
		fmt.Fprintln(w, "\nfix plan (--dry-run, nothing applied):")
		for _, p := range report.Plan {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}

	// Group findings by repo ("" -> workspace).
	groups := map[string][]Finding{}
	var order []string
	for _, f := range report.Findings {
		key := f.Repo
		if key == "" {
			key = "workspace"
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], f)
	}
	sort.Strings(order)

	var nErr, nWarn int
	for _, key := range order {
		fmt.Fprintf(w, "\n=== %s ===\n", key)
		for _, f := range groups[key] {
			tag := "[manual]"
			switch {
			case f.Skipped:
				tag = "[—]"
			case f.Applied:
				tag = "[fixed]"
			case opts.DryRun && f.Fixable:
				tag = "[would fix]"
			case f.Fixable:
				tag = "[fixable]"
			}
			sev := f.Severity.String()
			if f.Skipped {
				sev = "SKIP"
			} else if f.Severity == SevError {
				nErr++
			} else {
				nWarn++
			}
			fmt.Fprintf(w, "  %-5s %-20s %s %s\n", sev, f.CheckID, f.Message, tag)
			if tag == "[manual]" && f.Manual != "" {
				fmt.Fprintf(w, "          ↳ %s\n", f.Manual)
			}
		}
	}

	// Summary.
	fmt.Fprintln(w)
	switch {
	case nErr == 0 && len(report.Skipped) > 0:
		fmt.Fprintf(w, "workspace doctor: no errors (%d warnings), %d checks SKIPPED. remote equivalence NOT verified.\n", nWarn, len(report.Skipped))
	case nErr == 0:
		fmt.Fprintf(w, "workspace doctor: no errors (%d warnings). local and remote builds will match.\n", nWarn)
	default:
		fmt.Fprintf(w, "workspace doctor: %d errors, %d warnings.\n", nErr, nWarn)
	}
	return nil
}
```

(`colorEnabled(w)` may be layered onto the `sev` token in a follow-up; the text token is the required signal and is covered by the tests.)

- [ ] **Step 4: Run tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestRenderDoctor -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/doctor_render.go modules/pn/internal/workspace/doctor_render_test.go
git commit -m "feat(pn): doctor human + JSON rendering with severity markers"
```

---

### Task 14: CLI wiring (`pn workspace doctor`)

**Files:**

- Modify: `internal/cli/workspace.go`
- Test: `internal/cli/workspace_doctor_test.go`

**Interfaces:**

- Consumes: `workspace.Doctor(ctx, root, runner, opts)`, `workspace.RenderDoctor`, `resolveWorkspaceRoot` (`workspace.go:85`), `exec.NewRealRunner`, `ExitCodeError` (Task 1).
- Produces: `workspaceDoctorCmd(terminal *string) *cobra.Command`, registered via `ws.AddCommand(workspaceDoctorCmd(&terminalFlag))`.

- [ ] **Step 1: Write the failing test (CLI returns exit-code error on errors)**

```go
// internal/cli/workspace_doctor_test.go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorCmd_BrokenTomlExitsNonZero(t *testing.T) {
	root := t.TempDir()
	// invalid toml (repo missing url/remotes)
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"),
		[]byte("[repos.bad]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PN_WORKSPACE_ROOT", root)

	var out, errBuf bytes.Buffer
	err := executeWithVersion("test", []string{"workspace", "doctor"}, &out, &errBuf)
	if ExitCode(err) == 0 {
		t.Fatalf("broken toml should exit non-zero; out=%s err=%s", out.String(), errBuf.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/pn && go test ./internal/cli/ -run TestDoctorCmd -v`
Expected: FAIL (`unknown command "doctor"`).

- [ ] **Step 3: Implement the command**

```go
// add to internal/cli/workspace.go

func workspaceDoctorCmd(terminal *string) *cobra.Command {
	var (
		fix, dryRun, offline, jsonOut, strict bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Audit (and optionally repair) workspace drift",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && !fix {
				return fmt.Errorf("--dry-run requires --fix")
			}
			root, err := resolveWorkspaceRoot("")
			if err != nil {
				return err
			}
			_ = os.Setenv("PN_WORKSPACE_ROOT", root)
			_ = os.Setenv("WORKSPACE_ROOT", root)

			opts := workspace.DoctorOptions{
				Fix: fix, DryRun: dryRun, Offline: offline, JSON: jsonOut,
				Strict: strict, Terminal: *terminal,
			}
			report, derr := workspace.Doctor(cmd.Context(), root, exec.NewRealRunner(), opts)
			if derr != nil {
				// doctor itself failed -> exit 2
				return ExitCodeError{Code: 2, Msg: derr.Error()}
			}
			if rerr := workspace.RenderDoctor(cmd.OutOrStdout(), report, opts); rerr != nil {
				return rerr
			}
			if code := report.ExitCode(strict); code != 0 {
				return ExitCodeError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe fixes")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --fix: print the fix plan, change nothing")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip remote-dependent checks")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit findings as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors for the exit code")
	return cmd
}
```

Register it: in `addWorkspaceCmd`, add `ws.AddCommand(workspaceDoctorCmd(&terminalFlag))`.

Ensure `internal/cli/workspace.go` imports `"fmt"` (already present).

- [ ] **Step 4: Run tests**

Run: `cd modules/pn && go test ./internal/cli/ -run TestDoctorCmd -v`
Expected: PASS.

- [ ] **Step 5: Verify the command exists end-to-end**

Run: `cd modules/pn && go run ./cmd/pn workspace doctor --help`
Expected: help text listing `--fix`, `--dry-run`, `--offline`, `--json`, `--strict`.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/cli/workspace.go modules/pn/internal/cli/workspace_doctor_test.go
git commit -m "feat(pn): wire 'pn workspace doctor' CLI command + exit codes"
```

---

### Task 15: Convergence / idempotence integration test

**Files:**

- Create: `internal/workspace/doctor_convergence_test.go`

**Interfaces:**

- Consumes: `Doctor`, the real-git helpers, `exec.NewRealRunner`.

This is a state-transition test with multiple simultaneous drifts that does NOT need nix: missing non-terminal repo, a repo on the wrong branch, a repo behind its remote, and a missing lock. After `--fix`, a second `Doctor` run reports zero **errors**. (Flake-lock/nix fixes are exercised by an opt-in `fsNixRunner`/smoke test, not here.)

- [ ] **Step 1: Write the convergence test**

```go
// internal/workspace/doctor_convergence_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestDoctor_ConvergesAfterFix(t *testing.T) {
	root := t.TempDir()

	// Two repos with bare remotes; "term" is the terminal.
	term := filepath.Join(root, "term")
	dep := filepath.Join(root, "dep")
	initRealRepo(t, term)
	setupLocalBareRemote(t, term)
	initRealRepo(t, dep)
	bareDep := setupLocalBareRemote(t, dep)

	// Drift 1: dep is behind its remote (advance remote, reset local).
	addCommit(t, dep, "x.txt", "new", "advance")
	runGitT(t, dep, "push", "-q", "origin", "main")
	runGitT(t, dep, "reset", "-q", "--hard", "HEAD~1")
	// Drift 2: term is on a feature branch.
	runGitT(t, term, "switch", "-q", "-c", "feature")

	// Write a config (no lock.json on disk -> lock-present warning, fixed by --fix).
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"term": {URL: term + ".git", Branch: "main"},
			"dep":  {URL: bareDep, Branch: "main"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := exec.NewRealRunner()

	// First run: expect errors (branch-current term, branch-synced dep).
	r1, err := Doctor(ctx, root, runner, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.HasErrors() {
		t.Fatalf("expected errors before fix; got %+v", r1.Findings)
	}

	// Fix.
	if _, err := Doctor(ctx, root, runner, DoctorOptions{Fix: true}); err != nil {
		t.Fatal(err)
	}

	// Second run: branch + sync errors resolved.
	r2, err := Doctor(ctx, root, runner, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r2.Findings {
		if f.Severity == SevError && !f.Skipped &&
			(f.CheckID == "branch-current" || f.CheckID == "branch-synced") {
			t.Fatalf("residual error after fix: %s (%s) %s", f.CheckID, f.Repo, f.Message)
		}
	}
}
```

- [ ] **Step 2: Run the test**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestDoctor_ConvergesAfterFix -v`
Expected: PASS. (If `flake-lock-fresh`/`terminal-resolvable` emit nix-driven findings in this no-flake fixture, they will not be `branch-current`/`branch-synced`, so the assertion above stays valid; broaden only if needed.)

- [ ] **Step 3: Commit**

```bash
git add modules/pn/internal/workspace/doctor_convergence_test.go
git commit -m "test(pn): doctor multi-drift convergence/idempotence"
```

---

### Task 16: Docs

**Files:**

- Modify: `modules/pn/README.md` (or the nearest `pn` docs index) — add a `pn workspace doctor` section.
- Modify: `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` — mention `doctor` in the verb list and the completion gate.

- [ ] **Step 1: Add a doctor section to the pn README**

Document: purpose, the invariant, the two modes, the flags (`--fix`/`--dry-run`/`--offline`/`--json`/`--strict`), exit codes (0/1/2), and that `flake-lock-fresh --fix` runs `pn workspace update` (pushes). Use a fenced example of a clean run and a run with findings.

- [ ] **Step 2: Update the pn-workspace-rules skill**

Add `doctor` to the list of `pn workspace` verbs the skill recognizes, and note it as a recommended completion gate ("run `pn workspace doctor` before declaring a workspace task done").

- [ ] **Step 3: Run the full suite + formatter**

Run: `cd modules/pn && go test ./... && cd .. && nix fmt`
Expected: all tests pass; formatter clean.

- [ ] **Step 4: Commit**

```bash
git add modules/pn/README.md phillipg-nix-repo-base/pn-workspace-rules/skills/pn-workspace-rules/SKILL.md
git commit -m "docs(pn): document pn workspace doctor"
```

---

## Final verification

- [ ] Run the whole module test suite:

Run: `cd modules/pn && go test ./...`
Expected: PASS.

- [ ] Run pre-commit (treefmt etc.) over the repo:

Run: from repo root, `pre-commit run --all-files` (or commit, letting the prek hook run) — fix any formatting and re-commit.

- [ ] Confirm `pn workspace doctor` runs against the real workspace:

Run: `go run ./cmd/pn workspace doctor` from a workspace checkout and sanity-check the report.

## Self-Review notes (plan author)

- **Spec coverage:** every spec check maps to a task — structural (T7), repos/identity/is-git (T8), branch/tree (T9), terminal/follows/flake-path (T10), flake-lock-fresh (T11); fix engine + `--dry-run` (T12); output/`--json`/`--strict`/markers (T13); CLI + exit codes (T1, T14); modes (T4); refRev incl. multi-remote URL resolution (T5); convergence (T15); docs (T16). `revs` checks intentionally absent (removed per spec / bead `pg2-f1k1`).
- **Type consistency:** `Finding` fields (`CheckID`, `Repo`, `Severity`, `Message`, `Manual`, `Fixable`, `Skipped`, `Applied`, `fix`) are introduced in T6/T12 and used unchanged thereafter; `doctorEnv` and `check` likewise. `Doctor`, `RenderDoctor`, `ExitCode`, `ExitCodeError` signatures are stable across T1/T6/T13/T14.
- **Known follow-ups (not blockers):** layering `colorEnabled` onto the `ERROR/WARN/SKIP` tokens (T13 note); an opt-in real-nix smoke scenario for the `flake-lock-fresh` fix and `lock-current` nix-derive path (the in-package coverage uses real git + the `update`/`WriteDerivedLock` paths, which call nix only when a flake is present — the convergence fixture avoids that by design).
