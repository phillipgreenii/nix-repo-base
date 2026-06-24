# pn workspace update — per-repo worktree isolation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pn workspace update` run each repo's `update-locks.sh` in an ephemeral per-repo git worktree and fast-forward the result back onto the primary `main`, becoming the default with an `--in-place` escape hatch.

**Architecture:** Keep the existing in-place `Update` body verbatim behind `--in-place`; add a new `updateViaWorktree` path that, per repo in topological order, creates a worktree+branch off local `main`, rebases onto local then remote `main`, runs the existing `update-locks.sh` in the worktree, pushes the branch to remote `main`, fast-forwards the primary `main` (smartly, by checkout state), and removes the worktree+branch on success. Leave-on-failure: any failed step leaves the worktree+branch and the sweep continues. All correctness comes from reusing `update-locks.sh` unchanged (remote relock) and the existing topo order.

**Tech Stack:** Go (`modules/pn`), `cobra` CLI, the `exec.Runner` abstraction (real + `FakeRunner`), the JSONL `eventlog`, and the bash bare-remote smoke harness.

**Spec:** `docs/superpowers/specs/2026-06-24-pn-workspace-update-worktree-isolation-design.md` · **ADR:** `docs/adr/0009-pn-workspace-update-worktree-isolation.md`

**Working dir for all paths below:** `phillipg-nix-repo-base/` (the repo root). Run `go` commands from `modules/pn`.

---

## File structure

- `modules/pn/internal/workspace/update.go` — MODIFY. Add `InPlace bool` to `UpdateOptions`; rename the current `Update` body to `updateInPlace`; new `Update` dispatches on `opts.InPlace`.
- `modules/pn/internal/workspace/update_worktree.go` — CREATE. `updateViaWorktree`, the per-repo `updateRepoViaWorktree`, the primary-state probe, the run-stamp var, the summary printer, and the per-repo eventlog emit.
- `modules/pn/internal/workspace/update_worktree_test.go` — CREATE. Unit tests (happy path, push-defer, other-branch ff, dirty-main defer, rebase-abort leave, empty-`UL_LIB_DIR` hard error).
- `modules/pn/internal/workspace/worktree.go` — MODIFY. `WorktreeList` skips dot-prefixed entries (so `.pn-update/` is never listed as a set).
- `modules/pn/internal/workspace/worktree_test.go` — MODIFY. Add a dot-skip test.
- `modules/pn/internal/workspace/upgrade.go` — MODIFY. Add `InPlace bool` to `UpgradeOptions`; forward to `Update`.
- `modules/pn/internal/cli/workspace.go` — MODIFY. Add `--in-place` to the `update` and `upgrade` commands; pass through.
- `modules/pn/internal/workspace/smoke/scenarios/s33-worktree-update/{setup.sh,command.txt}` — CREATE. Happy-path worktree update against bare remotes.
- `modules/pn/internal/workspace/smoke/scenarios/s20-happy-path-update/command.txt` and `s32-update-events-jsonl/command.txt` — MODIFY. Add `--in-place` so they keep testing the in-place flow (see Task 6 for why).
- Consumer `update-locks.sh` (6 repos) — AUDIT only (Task 7).
- `docs/worktrees.md`, `pn-workspace-rules/CLAUDE.md`, `pn-workspace-rules/USER_JOURNEYS.md` — MODIFY (Task 8).

---

## Task 1: `--in-place` flag, default flip, dispatch

**Files:**

- Modify: `modules/pn/internal/workspace/update.go`
- Modify: `modules/pn/internal/workspace/upgrade.go`
- Modify: `modules/pn/internal/cli/workspace.go`
- Test: `modules/pn/internal/workspace/update_test.go`

- [ ] **Step 1: Write a failing test that `--in-place` still runs the old flow and is the default-off**

Add to `modules/pn/internal/workspace/update_test.go`:

```go
// TestUpdate_InPlaceDispatch: with InPlace=true, Update runs the legacy
// direct-on-main flow (dirty check → no upstream → update-locks → rev capture)
// and never creates a worktree.
func TestUpdate_InPlaceDispatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	foo := filepath.Join(root, "foo")
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{InPlace: true, ULLibDir: "/nix/store/x/lib/scripts"}); err != nil {
		t.Fatalf("Update --in-place: %v", err)
	}
	for _, c := range f.Calls() {
		if len(c.Args) >= 2 && c.Args[0] == "-C" && c.Name == "git" && len(c.Args) > 2 && c.Args[2] == "worktree" {
			t.Fatalf("in-place must not call git worktree; got %v", c.Args)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails to compile / fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestUpdate_InPlaceDispatch`
Expected: FAIL — `UpdateOptions has no field InPlace` (compile error).

- [ ] **Step 3: Add `InPlace` to `UpdateOptions` and split `Update`**

In `modules/pn/internal/workspace/update.go`, add the field to the options struct (next to `Recreate`):

```go
	// InPlace selects the legacy direct-on-main flow (pull → update-locks →
	// push in each primary checkout). When false (the default), Update isolates
	// each repo in an ephemeral worktree and fast-forwards back to main.
	InPlace bool
```

Then rename the existing method `func (ws *Workspace) Update(...)` to `func (ws *Workspace) updateInPlace(...)` (change only the method name on the existing function; leave its body untouched), and add the dispatcher above it:

```go
// Update runs the workspace update. By default each repo is updated in an
// ephemeral git worktree and fast-forwarded back onto the primary main
// (updateViaWorktree); opts.InPlace selects the legacy direct-on-main flow
// (updateInPlace). See ADR 0009.
func (ws *Workspace) Update(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if opts.InPlace {
		return ws.updateInPlace(ctx, out, opts)
	}
	return ws.updateViaWorktree(ctx, out, opts)
}
```

> `updateViaWorktree` is added in Task 4. Until then the package will not compile — that's expected; Tasks 1–4 land together conceptually. If you need Task 1 to compile in isolation, add a temporary `func (ws *Workspace) updateViaWorktree(ctx context.Context, out io.Writer, opts UpdateOptions) error { return ws.updateInPlace(ctx, out, opts) }` stub and delete it in Task 4.

- [ ] **Step 4: Add the temporary stub so the package compiles**

Append to `modules/pn/internal/workspace/update.go` (deleted in Task 4):

```go
// TEMP stub — replaced by update_worktree.go in Task 4.
func (ws *Workspace) updateViaWorktree(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	return ws.updateInPlace(ctx, out, opts)
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestUpdate_InPlaceDispatch`
Expected: PASS.

- [ ] **Step 6: Wire `--in-place` into the CLI for `update` and `upgrade`**

In `modules/pn/internal/cli/workspace.go`, change `workspaceUpdateCmd` to register the flag and pass it. Replace the function with:

```go
func workspaceUpdateCmd(terminal *string) *cobra.Command {
	var inPlace bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update each workspace repo (worktree-isolated; --in-place for direct-on-main)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()

			lw, err := eventlog.New(eventlog.DefaultPath())
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "pn: event log unavailable: %v\n", err)
			} else {
				defer func() { _ = lw.Close() }()
			}

			return runWithHooks(ctx, w, "update", func() error {
				return w.Update(ctx, out, workspace.UpdateOptions{Terminal: *terminal, Log: lw, InPlace: inPlace})
			})
		},
	}
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "update each repo directly on its primary main instead of in an isolated worktree")
	return cmd
}
```

And `workspaceUpgradeCmd`:

```go
func workspaceUpgradeCmd(terminal *string) *cobra.Command {
	var inPlace bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update + apply each workspace repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			out := cmd.OutOrStdout()
			return runWithHooks(ctx, w, "upgrade", func() error {
				return w.Upgrade(ctx, out, workspace.UpgradeOptions{Terminal: *terminal, InPlace: inPlace})
			})
		},
	}
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "update phase runs directly on primary main instead of in an isolated worktree")
	return cmd
}
```

- [ ] **Step 7: Forward `InPlace` through `upgrade.go`**

In `modules/pn/internal/workspace/upgrade.go`, add to `UpgradeOptions`:

```go
	// InPlace forwards to Update.InPlace (legacy direct-on-main update phase).
	InPlace bool
```

and change the `Update` call inside `Upgrade` to pass it:

```go
	if err := ws.Update(ctx, out, UpdateOptions{Terminal: opts.Terminal, Recreate: true, ULLibDir: opts.ULLibDir, InPlace: opts.InPlace}); err != nil {
```

- [ ] **Step 8: Build and run the existing update tests**

Run: `cd modules/pn && go build ./... && go test ./internal/workspace/ ./internal/cli/ -run 'Update|Upgrade'`
Expected: PASS (the stub makes the default path behave like in-place for now).

- [ ] **Step 9: Commit**

```bash
git add modules/pn/internal/workspace/update.go modules/pn/internal/workspace/upgrade.go modules/pn/internal/cli/workspace.go modules/pn/internal/workspace/update_test.go
git commit -m "feat(pn): add --in-place flag and Update dispatch for worktree isolation"
```

---

## Task 2: `WorktreeList` skips dot-prefixed entries

**Files:**

- Modify: `modules/pn/internal/workspace/worktree.go:124-142`
- Test: `modules/pn/internal/workspace/worktree_test.go`

- [ ] **Step 1: Write the failing test**

Add to `modules/pn/internal/workspace/worktree_test.go`:

```go
// TestWorktreeList_SkipsDotEntries: a dot-prefixed dir under worktrees_dir
// (e.g. the .pn-update update-worktree area) is not listed as a set.
func TestWorktreeList_SkipsDotEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:o/foo\"\n")
	wtDir := filepath.Join(root, ".worktrees")
	if err := os.MkdirAll(filepath.Join(wtDir, "my-feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtDir, ".pn-update"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf bytes.Buffer
	if err := w.WorktreeList(context.Background(), &buf, &bytes.Buffer{}, WorktreeListOptions{}); err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "my-feature") {
		t.Errorf("expected my-feature listed, got %q", got)
	}
	if strings.Contains(got, ".pn-update") {
		t.Errorf(".pn-update must not be listed as a set, got %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestWorktreeList_SkipsDotEntries`
Expected: FAIL — output contains `.pn-update`.

- [ ] **Step 3: Add the dot-skip**

In `modules/pn/internal/workspace/worktree.go`, inside the `WorktreeList` loop, change the body to skip dot-prefixed names:

```go
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		setName := e.Name()
		// Dot-prefixed dirs (e.g. .pn-update, the ephemeral update-worktree area)
		// are not coordinated sets — skip them.
		if strings.HasPrefix(setName, ".") {
			continue
		}
		// The set dir name IS the branch by construction. Print the name directly.
		fmt.Fprintf(out, "%s\t%s\n", setName, setName)
	}
```

(`strings` is already imported in `worktree.go`.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestWorktreeList_SkipsDotEntries`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/worktree.go modules/pn/internal/workspace/worktree_test.go
git commit -m "fix(pn): WorktreeList skips dot-prefixed entries (.pn-update)"
```

---

## Task 3: primary-state probe + run-stamp helper

**Files:**

- Create: `modules/pn/internal/workspace/update_worktree.go` (start the file with just the helpers)
- Test: `modules/pn/internal/workspace/update_worktree_test.go`

- [ ] **Step 1: Write the failing test for `primaryMainState`**

Create `modules/pn/internal/workspace/update_worktree_test.go`:

```go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPrimaryMainState(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:o/foo\"\n")
	foo := filepath.Join(root, "foo")

	cases := []struct {
		name   string
		branch string
		exit   int // exit code of diff --quiet (0 clean, 1 dirty)
		want   primaryState
	}{
		{"clean main", "main", 0, primaryOnCleanMain},
		{"other branch", "feature-x", 0, primaryOnOtherBranch},
		{"dirty main", "main", 1, primaryOnDirtyMain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := exec.NewFakeRunner()
			f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"},
				exec.Result{Stdout: []byte(tc.branch + "\n")}, nil)
			if tc.branch == "main" {
				var derr error
				if tc.exit != 0 {
					derr = &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: tc.exit}}
				}
				f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: tc.exit}, derr)
				if tc.exit == 0 {
					f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
				}
			}
			w, _ := Open(root, f)
			if got := w.primaryMainState(context.Background(), foo); got != tc.want {
				t.Errorf("primaryMainState = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestPrimaryMainState`
Expected: FAIL — `undefined: primaryState` (compile error).

- [ ] **Step 3: Create `update_worktree.go` with the helpers**

Create `modules/pn/internal/workspace/update_worktree.go`:

```go
package workspace

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// primaryState classifies a primary checkout for smart integration (step 7).
type primaryState int

const (
	primaryOnCleanMain   primaryState = iota // on main, clean → merge --ff-only
	primaryOnOtherBranch                     // main not checked out → ff the ref
	primaryOnDirtyMain                       // on main but dirty → defer
)

// updateWorktreesSubdir is the dot-prefixed dir under WorktreesDir() holding the
// ephemeral per-repo update worktrees. Dot-prefixed so WorktreeList and the
// filesystem scanners skip it.
const updateWorktreesSubdir = ".pn-update"

// updateRunStampFn produces the per-run suffix used for the shared branch name
// and per-repo worktree dir names. Time + PID so concurrent runs don't collide
// on a coarse timestamp. A package var so tests can pin it deterministically.
var updateRunStampFn = func() string {
	return fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405"), os.Getpid())
}

// primaryMainState probes the primary checkout's branch + cleanliness to decide
// how step 7 advances main. A non-"main" current branch (or a probe error) is
// treated as primaryOnOtherBranch: main is not checked out, so its ref can be
// fast-forwarded without touching the working tree.
func (ws *Workspace) primaryMainState(ctx context.Context, primary string) primaryState {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", primary, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	cur := ""
	if err == nil {
		cur = strings.TrimSpace(string(res.Stdout))
	}
	if cur != "main" {
		return primaryOnOtherBranch
	}
	if ws.isDirty(ctx, primary) {
		return primaryOnDirtyMain
	}
	return primaryOnCleanMain
}
```

> Add the `exec` import — change the import block to include
> `"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestPrimaryMainState`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/update_worktree.go modules/pn/internal/workspace/update_worktree_test.go
git commit -m "feat(pn): add primaryMainState probe and run-stamp for worktree update"
```

---

## Task 4: `updateViaWorktree` core

**Files:**

- Modify: `modules/pn/internal/workspace/update.go` (delete the Task 1 temporary stub)
- Modify: `modules/pn/internal/workspace/update_worktree.go` (add the core functions)

- [ ] **Step 1: Delete the temporary stub from Task 1**

Remove the `// TEMP stub …` `updateViaWorktree` function from `update.go`.

- [ ] **Step 2: Add the core functions to `update_worktree.go`**

Extend the import block of `update_worktree.go` to:

```go
import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)
```

Append:

```go
// repoOutcome records one repo's worktree-update result for the run summary.
type repoOutcome struct {
	name       string
	status     string // "ok" | "failed" | "deferred"
	failedStep string
	worktree   string // left-behind worktree path when status != ok
	branch     string // left-behind branch when status != ok
	rev        string // rev to record in revs.json (ok or pushed-but-deferred)
	note       string // recovery hint / human note
}

// updateViaWorktree runs the worktree-isolated update over all repos in
// topological order. See ADR 0009 and the design spec for the per-repo
// algorithm; this is the outer loop (terminal guard, UL_LIB_DIR resolve,
// rev-lock rewrite, eventlog, summary) and updateRepoViaWorktree is the body.
func (ws *Workspace) updateViaWorktree(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if _, err := ws.requireTerminal(ctx, opts.Terminal); err != nil {
		return err
	}
	// Each consumer update-locks.sh clobbers WORKSPACE_ROOT to SCRIPT_DIR/.., so
	// the only safe relock path is a resolved UL_LIB_DIR (ADR 0009 B1). Resolve
	// once; empty is fatal — do not silently take the slow store fallback.
	// Resolve UL_LIB_DIR once: explicit option → pre-set env (lets CI/smoke inject
	// without nix) → nix resolver. Each consumer update-locks.sh clobbers
	// WORKSPACE_ROOT to SCRIPT_DIR/.., so a non-empty UL_LIB_DIR is the only safe
	// relock path in a worktree (ADR 0009 B1); empty is fatal.
	ulLibDir := opts.ULLibDir
	if ulLibDir == "" {
		ulLibDir = os.Getenv("UL_LIB_DIR")
	}
	if ulLibDir == "" {
		ulLibDir = ws.ResolveULLibDir(ctx)
	}
	if ulLibDir == "" {
		return fmt.Errorf("update: could not resolve UL_LIB_DIR (set UL_LIB_DIR or fix determine-ul-lib-dir); refusing to relock in a worktree without it (use --in-place to update on main)")
	}

	runTS := updateRunStampFn()
	branch := "pn-update/" + runTS
	names := ws.topoAlpha(ctx)

	_ = opts.Log.Emit("info", "run_start", "workspace update (worktree) started", map[string]any{
		"terminal": opts.Terminal, "projects": len(names), "branch": branch,
	})

	// Seed from the existing rev-lock so untouched repos keep their entries.
	revs := make(map[string]LockedRepo, len(names))
	if ws.revLock != nil {
		for k, v := range ws.revLock.Repos {
			revs[k] = v
		}
	}

	outcomes := make([]repoOutcome, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		oc := ws.updateRepoViaWorktree(ctx, out, name, branch, runTS, ulLibDir)
		if oc.rev != "" {
			revs[name] = LockedRepo{URL: displayURL(ws.config.Repos[name]), Rev: oc.rev}
		}
		level, outcome := "info", "ok"
		if oc.status != "ok" {
			level, outcome = "error", oc.status
		}
		_ = opts.Log.Emit(level, "project_result", "project "+oc.status, map[string]any{
			"name": oc.name, "outcome": outcome, "failed_step": oc.failedStep,
		})
		outcomes = append(outcomes, oc)
	}

	if err := WriteRevLock(filepath.Join(ws.root, RevLockFileName), &RevLock{Repos: revs}); err != nil {
		return fmt.Errorf("write rev lock: %w", err)
	}

	printUpdateSummary(out, outcomes)

	var failed []string
	for _, oc := range outcomes {
		if oc.status != "ok" {
			failed = append(failed, oc.name)
		}
	}
	if len(failed) > 0 {
		_ = opts.Log.Emit("error", "run_end", "workspace update finished with failures",
			map[string]any{"status": "failed", "failed": len(failed), "failed_projects": failed})
		return fmt.Errorf("update failed/deferred in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	_ = opts.Log.Emit("info", "run_end", "workspace update finished", map[string]any{"status": "ok", "failed": 0})
	return nil
}

// updateRepoViaWorktree runs the 8-step per-repo flow. It never returns an
// error: every failure is captured in the returned repoOutcome and the worktree
// + branch are left in place (leave-on-failure). Only a fully successful
// integration removes them.
func (ws *Workspace) updateRepoViaWorktree(ctx context.Context, out io.Writer, name, branch, runTS, ulLibDir string) repoOutcome {
	primary := filepath.Join(ws.root, name)
	wt := filepath.Join(ws.WorktreesDir(), updateWorktreesSubdir, name+"-"+runTS)
	oc := repoOutcome{name: name, worktree: wt, branch: branch}

	fmt.Fprintf(out, "  --== update %s (worktree) ==--  \n", name)

	git := func(args ...string) error {
		_, err := ws.runner.Run(ctx, "git", append([]string{"-C"}, args...), exec.RunOptions{Stdout: out, Stderr: out})
		return err
	}
	fail := func(step, note string) repoOutcome {
		oc.status, oc.failedStep, oc.note = "failed", step, note
		fmt.Fprintf(out, "  ✗ %s: failed at %s — worktree left at %s (branch %s)\n", name, step, wt, branch)
		return oc
	}

	// Step 1: create worktree + branch off local main.
	if err := git(primary, "worktree", "add", "-b", branch, wt, "main"); err != nil {
		oc.status, oc.failedStep, oc.worktree, oc.branch = "failed", "worktree-add", "", ""
		fmt.Fprintf(out, "  ✗ %s: worktree add failed (stale leftover? run `pn workspace worktree prune`): %v\n", name, err)
		return oc
	}

	// Step 2: sync branch to remote main.
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("fetch-origin", "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main", "rebase conflict aborted")
	}

	// Step 3: run the existing update-locks in the worktree.
	if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{
		Dir: wt, Env: ws.ulSubprocessEnv(ulLibDir), Stdout: out, Stderr: out,
	}); err != nil {
		return fail("update-locks", "")
	}

	// Step 4: rebase onto local main FIRST (catch unpushed local commits).
	if err := git(wt, "rebase", "main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-local-main", "rebase conflict aborted")
	}

	// Step 5: re-fetch + rebase onto origin/main (catch remote drift).
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("refetch-origin", "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main-2", "rebase conflict aborted")
	}

	// Capture the integrated tip (the rev downstream consumers relock against).
	rev, err := captureHead(ctx, ws.runner, wt)
	if err != nil {
		return fail("capture-rev", "")
	}

	// Step 6: publish — push branch to remote main from the worktree.
	if err := git(wt, "push", "origin", "HEAD:main"); err != nil {
		return fail("push", "remote main may have advanced; resolve manually and re-run")
	}
	// Remote main is now at rev. Record it even if step 7 defers, so revs.json
	// matches what downstream repos will relock against (ADR 0009 N1).
	oc.rev = rev

	// Step 7: advance local primary main (smart).
	switch ws.primaryMainState(ctx, primary) {
	case primaryOnCleanMain:
		if err := git(primary, "merge", "--ff-only", branch); err != nil {
			oc.status, oc.failedStep = "deferred", "ff-merge"
			oc.note = fmt.Sprintf("remote main advanced; reset local: git -C %s reset --hard origin/main", primary)
			fmt.Fprintf(out, "  ⚠ %s: ff-merge deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnOtherBranch:
		if err := git(primary, "fetch", ".", branch+":main"); err != nil {
			oc.status, oc.failedStep = "deferred", "ff-ref"
			oc.note = fmt.Sprintf("local main diverged; reset: git -C %s branch -f main origin/main", primary)
			fmt.Fprintf(out, "  ⚠ %s: main ff deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnDirtyMain:
		oc.status, oc.failedStep = "deferred", "integrate"
		oc.note = "primary on dirty main; commit/stash then ff main from the branch"
		fmt.Fprintf(out, "  ⚠ %s: integration deferred — primary on dirty main; worktree at %s (branch %s)\n", name, wt, branch)
		return oc
	}

	// Step 8: success — remove worktree, then branch.
	if err := git(primary, "worktree", "remove", wt); err != nil {
		oc.status, oc.note = "ok", "integrated, but worktree remove failed — run `pn workspace worktree prune`"
		fmt.Fprintf(out, "  ⚠ %s: integrated, but worktree remove failed\n", name)
		return oc
	}
	_ = git(primary, "branch", "-d", branch)
	oc.status, oc.worktree, oc.branch = "ok", "", ""
	fmt.Fprintf(out, "  ✓ %s: updated and integrated\n", name)
	return oc
}

// printUpdateSummary prints one line per repo: outcome and, for non-ok repos,
// the worktree/branch left behind and the recovery note.
func printUpdateSummary(out io.Writer, outcomes []repoOutcome) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== Update Summary ===")
	for _, oc := range outcomes {
		switch oc.status {
		case "ok":
			fmt.Fprintf(out, "  ✓ %s — ok\n", oc.name)
		default:
			fmt.Fprintf(out, "  ✗ %s — %s@%s; worktree %s (branch %s)\n", oc.name, oc.status, oc.failedStep, oc.worktree, oc.branch)
			if oc.note != "" {
				fmt.Fprintf(out, "      ↳ %s\n", oc.note)
			}
		}
	}
}
```

- [ ] **Step 3: Build**

Run: `cd modules/pn && go build ./...`
Expected: builds clean (no unused imports; `eventlog` is used via `opts.Log` typed param — if the compiler reports `eventlog` imported and not used, remove it from the import list, since `opts.Log` is typed in `update.go`. Keep the import only if you reference `eventlog.` directly; this file does not, so **remove `eventlog` from the import block**).

> Correction: the snippet's import block lists `eventlog` but the code never names `eventlog.` — delete that import line before building.

- [ ] **Step 4: Run the full workspace test package**

Run: `cd modules/pn && go test ./internal/workspace/`
Expected: PASS (existing tests still green; new unit tests come in Task 5).

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/update.go modules/pn/internal/workspace/update_worktree.go
git commit -m "feat(pn): implement updateViaWorktree per-repo isolated update flow"
```

---

## Task 5: unit tests for `updateViaWorktree`

**Files:**

- Test: `modules/pn/internal/workspace/update_worktree_test.go`

Each test pins `updateRunStampFn` to a constant and scripts the exact git sequence. A shared helper builds the common single-repo workspace.

- [ ] **Step 1: Add a fixture helper + the happy-path test**

Append to `modules/pn/internal/workspace/update_worktree_test.go`:

```go
import (
	"bytes"
	// (context, filepath, testing, exec already imported above; add bytes)
)

// wtUpdateFixture sets up a single-repo (terminal "foo") workspace and returns
// the root, the primary dir, the worktree dir for stamp "TEST", and the runner.
func wtUpdateFixture(t *testing.T) (root, foo, wt string, f *exec.FakeRunner) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	foo = filepath.Join(root, "foo")
	wt = filepath.Join(root, ".worktrees", updateWorktreesSubdir, "foo-TEST")
	f = exec.NewFakeRunner()
	return root, foo, wt, f
}

// scriptThroughPush scripts steps 1–6 for repo "foo" (worktree add → … → push).
func scriptThroughPush(f *exec.FakeRunner, foo, wt, branch string) {
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)            // step 2 fetch
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)      // step 2 rebase
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)                                // step 3
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)             // step 4
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)            // step 5 fetch
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)      // step 5 rebase
	f.AddResponse("git", []string{"-C", wt, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("dead00000000000000000000000000000000beef\n")}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"}, exec.Result{}, nil) // step 6
}

func TestUpdateViaWorktree_HappyPath_CleanMain(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	defer func() { updateRunStampFn = func() string { return "TEST" } }()
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	// step 7: on clean main → merge --ff-only
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	// step 8: cleanup
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/nix/store/x/lib/scripts"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// revs.json records the pushed rev.
	rl, _ := ReadRevLock(filepath.Join(root, RevLockFileName))
	if rl.Repos["foo"].Rev != "dead00000000000000000000000000000000beef" {
		t.Errorf("revs.json rev = %q, want pushed tip", rl.Repos["foo"].Rev)
	}
}
```

- [ ] **Step 2: Run it to verify it passes (impl already exists from Task 4)**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestUpdateViaWorktree_HappyPath_CleanMain -v`
Expected: PASS. (If FAIL with "no scripted response", the assertion message names the unscripted git call — align the script to the implementation's exact args.)

- [ ] **Step 3: Add the push-defer (divergence) test**

```go
func TestUpdateViaWorktree_PushSucceedsFfDefers(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	// step 7: on clean main but ff-only fails (local main diverged).
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"})
	if err == nil {
		t.Fatalf("expected non-nil error (deferred), got nil")
	}
	// pushed rev still recorded.
	rl, _ := ReadRevLock(filepath.Join(root, RevLockFileName))
	if rl.Repos["foo"].Rev != "dead00000000000000000000000000000000beef" {
		t.Errorf("revs.json must record pushed rev even on defer; got %q", rl.Repos["foo"].Rev)
	}
	// No worktree remove was attempted (left-on-failure).
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[2] == "worktree" && c.Args[1] == foo && contains(c.Args, "remove") {
			t.Fatalf("must not remove worktree on defer")
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Add the other-branch, dirty-main, rebase-abort, and empty-UL_LIB_DIR tests**

```go
func TestUpdateViaWorktree_OtherBranchRefFf(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	// step 7: primary on another branch → fetch . branch:main (ref-only ff).
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "fetch", ".", branch + ":main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestUpdateViaWorktree_DirtyMainDefers(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	// step 7: on main but dirty → defer, no merge/cleanup calls.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error")
	}
}

func TestUpdateViaWorktree_RebaseConflictAborts(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", wt, "rebase", "--abort"}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected failure")
	}
	// rebase --abort must have run.
	if !calledWith(f, "git", []string{"-C", wt, "rebase", "--abort"}) {
		t.Fatalf("expected rebase --abort after conflict")
	}
}

func TestUpdateViaWorktree_EmptyULLibDirIsFatal(t *testing.T) {
	t.Setenv("UL_LIB_DIR", "") // ensure the env source is empty for this test
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[workspace]\nterminal=\"foo\"\n[repos.foo]\nurl=\"github:o/foo\"\n")
	f := exec.NewFakeRunner() // no resolver response → ResolveULLibDir returns ""
	w, _ := Open(root, f)
	err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{}) // ULLibDir empty
	if err == nil || !strings.Contains(err.Error(), "UL_LIB_DIR") {
		t.Fatalf("expected UL_LIB_DIR hard error, got %v", err)
	}
}

func calledWith(f *exec.FakeRunner, name string, args []string) bool {
	for _, c := range f.Calls() {
		if c.Name == name && len(c.Args) == len(args) {
			ok := true
			for i := range args {
				if c.Args[i] != args[i] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}
```

> Note: `updateRunStampFn` is global; tests that set it run in the same package. Add `defer func(){ updateRunStampFn = func() string { return "TEST" } }()` only if you later add tests needing the real stamp; all tests here pin it to "TEST", so a stray real value never leaks. Ensure `strings` is imported in the test file.

- [ ] **Step 5: Run all the new tests**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestUpdateViaWorktree -v`
Expected: PASS for all five.

- [ ] **Step 6: Run the whole module test suite + vet**

Run: `cd modules/pn && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add modules/pn/internal/workspace/update_worktree_test.go
git commit -m "test(pn): unit-test updateViaWorktree integration branches and failure-leaves"
```

---

## Task 6: smoke tests + preserve in-place smokes

> **Smoke harness facts (verified in `smoke_test.go` / `smoke_env.go`):** smoke tests are behind `//go:build smoke` — **always pass `-tags smoke`**. Each scenario is a dir under `scenarios/<name>` with `setup.sh` + `command.txt` (+ optional `expected_exit.txt` / `expected_stdout.txt` / `expected_stderr.txt`). A new scenario also needs (a) a `TestSmoke_S33_…` func calling `runScenario(t, "<name>")`, and (b) a `case "<name>":` in the `runExtraAssertions` switch calling a new assertion helper. Subprocess env comes from `buildScrubbedEnv` (no `nix` required; `nixAvailable()` lets nix-only scenarios skip).
>
> **Why the default flip breaks `s20`/`s32`:** under the worktree flow, `update-locks.sh` runs with `Dir=<worktree>`, so its side effects (`s20` does `touch updated.txt`, untracked) land in the **worktree**, which is removed on success — and untracked files are never committed/pushed. So `assertS20UpdateMarkers` (which checks `updated.txt` in the **primary**) no longer holds. (The `order.log` write survives because it uses an absolute `${WORKSPACE_ROOT}` path, but the marker check still fails.) Pin `s20`/`s32` to `--in-place`; add `s33` that asserts on **git state**.
>
> **Why `s33` needs a `UL_LIB_DIR`:** `updateViaWorktree` hard-errors on an unresolved `UL_LIB_DIR` (Task 4). To keep the smoke nix-free, inject a dummy `UL_LIB_DIR` in `buildScrubbedEnv`; the fixture's `update-locks.sh` never sources it.

**Files:**

- Modify: `modules/pn/internal/workspace/smoke/scenarios/s20-happy-path-update/command.txt`
- Modify: `modules/pn/internal/workspace/smoke/scenarios/s32-update-events-jsonl/command.txt`
- Modify: `modules/pn/internal/workspace/smoke/smoke_env.go` (inject `UL_LIB_DIR`)
- Modify: `modules/pn/internal/workspace/smoke/smoke_test.go` (test func + assertion case + helper)
- Create: `modules/pn/internal/workspace/smoke/scenarios/s33-worktree-update/{setup.sh,command.txt}`

- [ ] **Step 1: Inject a dummy `UL_LIB_DIR` into the scrubbed smoke env**

In `smoke_env.go`, after `xdgState` is created, add a stub dir and an override entry:

```go
	ulLib := filepath.Join(tempHome, "ullib")
	if err := os.MkdirAll(ulLib, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create ullib: %v", err)
	}
```

and add to the `overrides` map:

```go
		"UL_LIB_DIR": ulLib,
```

(This makes `updateViaWorktree`'s env-source resolution succeed without `nix`. Harmless to the in-place scenarios — their fixtures ignore `UL_LIB_DIR`.)

- [ ] **Step 2: Pin the in-place smokes**

Edit `s20-happy-path-update/command.txt` — change the last line from `workspace update` to:

```
workspace update --in-place
```

Read `s32-update-events-jsonl/command.txt` (`cat modules/pn/internal/workspace/smoke/scenarios/s32-update-events-jsonl/command.txt`); if it invokes `workspace update`, append ` --in-place` the same way. (S32 asserts 2 `project_result` events; the in-place flow still emits those, so `--in-place` keeps it valid.)

- [ ] **Step 3: Confirm the in-place scenarios still pass**

Run: `cd modules/pn && go test -tags smoke ./internal/workspace/smoke/ -run 'S20|S32' -v`
Expected: PASS.

- [ ] **Step 4: Create `s33-worktree-update/setup.sh`**

```bash
#!/usr/bin/env bash
# S33: worktree-isolated update (the new default).
# One bare-remote repo (terminal). Its update-locks.sh makes a COMMITTABLE change
# so the branch advances and pushes. Assertions are on git state (remote main
# advanced, primary main fast-forwarded, no leftover .pn-update worktree) — not
# on marker files, which the worktree flow leaves in the (removed) worktree.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

BARE="$REMOTES_DIR/solo.git"
git init --bare -b main "$BARE"
WORK="$(mktemp -d)"
git clone "file://${BARE}" "$WORK"
git -C "$WORK" config user.email "smoke@test.invalid"
git -C "$WORK" config user.name "smoke"
cat >"$WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
echo "v0" >"$WORK/locked.txt"
cat >"$WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
n=$(cat locked.txt 2>/dev/null || echo v0)
echo "${n}x" >locked.txt
git add locked.txt
git commit -m "update-locks: bump locked.txt" >/dev/null
SH
chmod +x "$WORK/update-locks.sh"
git -C "$WORK" add flake.nix update-locks.sh locked.txt
git -C "$WORK" commit -m "init"
git -C "$WORK" push -u origin main
rm -rf "$WORK"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s33"
terminal = "solo"

[repos.solo]
url = "file://${BARE}"
TOML
```

- [ ] **Step 5: Create `s33-worktree-update/command.txt`**

```
# Bootstrap, then run the default (worktree-isolated) update.
workspace init
workspace clone
workspace lock
workspace update
```

- [ ] **Step 6: Add the test func and assertion to `smoke_test.go`**

Add the test function (near the other `TestSmoke_S3x` funcs):

```go
// TestSmoke_S33_WorktreeUpdate: single bare-remote repo; the default
// (worktree-isolated) update relocks in an ephemeral worktree, pushes the
// branch to remote main, fast-forwards the primary main, and removes the
// worktree. Asserts the relock commit reached both the primary and the remote
// and that no .pn-update worktree remains.
func TestSmoke_S33_WorktreeUpdate(t *testing.T) {
	runScenario(t, "s33-worktree-update")
}
```

Add a case to the `runExtraAssertions` switch:

```go
	case "s33-worktree-update":
		assertS33WorktreeUpdate(t, wsRoot)
```

Add the assertion helper (alongside the other `assertSNN…` funcs; `os`, `os/exec`, `path/filepath`, `strings` are already imported in `smoke_test.go`):

```go
// --- S33 extra: worktree update integrated to primary + remote; no leftover ---

func assertS33WorktreeUpdate(t *testing.T, wsRoot string) {
	t.Helper()
	primary := filepath.Join(wsRoot, "solo")
	bare := filepath.Join(wsRoot, "remotes", "solo.git")

	logOut, err := exec.Command("git", "-C", primary, "log", "--oneline", "-n", "5").Output()
	if err != nil {
		t.Fatalf("S33: git log primary: %v", err)
	}
	if !strings.Contains(string(logOut), "update-locks: bump locked.txt") {
		t.Errorf("S33: primary main missing relock commit; log:\n%s", logOut)
	}

	remoteHead, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33: git rev-parse remote: %v", err)
	}
	primHead, err := exec.Command("git", "-C", primary, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33: git rev-parse primary: %v", err)
	}
	if strings.TrimSpace(string(remoteHead)) != strings.TrimSpace(string(primHead)) {
		t.Errorf("S33: remote main %s != primary main %s", strings.TrimSpace(string(remoteHead)), strings.TrimSpace(string(primHead)))
	}

	if entries, err := os.ReadDir(filepath.Join(wsRoot, ".worktrees", ".pn-update")); err == nil && len(entries) > 0 {
		t.Errorf("S33: .pn-update worktree left behind: %v", entries)
	}
}
```

- [ ] **Step 7: Run the new scenario**

Run: `cd modules/pn && go test -tags smoke ./internal/workspace/smoke/ -run S33 -v`
Expected: PASS (no nix needed — the fixture `update-locks.sh` is a plain committing shell script).

- [ ] **Step 8: Run the full smoke suite**

Run: `cd modules/pn && go test -tags smoke ./internal/workspace/smoke/`
Expected: PASS.

- [ ] **Step 9 (optional but recommended): failure-path / asymmetric-defer smoke**

Add `s34-worktree-update-defer` modeled on `s33`, but `setup.sh` advances the bare remote's `main` by one extra commit _after_ cloning the primary and _before_ `workspace update`, while also leaving the primary `main` with an un-pushed local commit — forcing the step-7 ff to defer after the push. Assert: exit non-zero, summary contains `deferred`, the `.worktrees/.pn-update/solo-*` worktree **remains**, and the recovery hint (`reset --hard origin/main` / `branch -f main origin/main`) is printed. Wire it the same way (test func + `runExtraAssertions` case + helper). If the divergence is fiddly to construct deterministically in bash, leave a `// TODO(pg2-…): asymmetric-defer smoke` bead instead of a flaky scenario — the unit test `TestUpdateViaWorktree_PushSucceedsFfDefers` already covers the logic; this smoke is defense-in-depth.

- [ ] **Step 10: Commit**

```bash
git add modules/pn/internal/workspace/smoke/
git commit -m "test(pn): worktree-update smoke (s33) + pin in-place update smokes"
```

---

## Task 7: audit consumer `update-locks.sh` scripts

**Files (read-only audit; fix only if a problem is found):**

- `phillipg-nix-repo-base/update-locks.sh`
- `phillipgreenii-nix-overlay/update-locks.sh`
- `phillipgreenii-nix-agent-support/update-locks.sh`
- `phillipgreenii-nix-personal/update-locks.sh`
- `phillipgreenii-nix-support-apps/update-locks.sh`
- `phillipg-nix-ziprecruiter/update-locks.sh`

- [ ] **Step 1: Grep each script for `WORKSPACE_ROOT` usage beyond the resolver**

Run: `rg -n 'WORKSPACE_ROOT' ../*/update-locks.sh phillipg-nix-repo-base/update-locks.sh` (adjust paths to your checkout layout).
Expected/required: the only `WORKSPACE_ROOT` references are `WORKSPACE_ROOT="${SCRIPT_DIR}/.."`, `export WORKSPACE_ROOT`, and passing it to `determine-ul-lib-dir`. Any script that dereferences `${WORKSPACE_ROOT}/<sibling>` for a real path is a problem under the worktree flow (its `SCRIPT_DIR/..` is `.pn-update`, not the workspace root).

- [ ] **Step 2: Grep for sibling-relative reach-outs**

Run: `rg -n '\.\./(phillipg|phillipgreenii)|\$\{?WORKSPACE_ROOT\}?/' ../*/update-locks.sh`
Expected: no hits that resolve to a sibling repo path. (`determine-ul-lib-dir` resolution is fine — `UL_LIB_DIR` is injected.)

- [ ] **Step 3: Record the result**

If clean (expected per the 2026-06-24 review), add a one-line note to the spec's audit bullet stating the date verified. If a script reaches for a sibling via `WORKSPACE_ROOT`, fix it to use `SCRIPT_DIR`-relative or an injected path, in that repo, with its own commit, and note it here.

- [ ] **Step 4: Commit (only if a fix was needed)**

```bash
# in the affected repo:
git commit -am "fix: make update-locks.sh worktree-safe (no WORKSPACE_ROOT sibling deref)"
```

---

## Task 8: docs + agent rules + `--help`

**Files:**

- Modify: `phillipg-nix-repo-base/docs/worktrees.md`
- Modify: `phillipg-nix-repo-base/pn-workspace-rules/CLAUDE.md`
- Modify: `phillipg-nix-repo-base/pn-workspace-rules/USER_JOURNEYS.md`

- [ ] **Step 1: Add a third model section to `docs/worktrees.md`**

After the coordinated-set and single-override sections, add a section titled **"Per-repo ephemeral update worktrees (the `update` default)"** documenting: that `pn workspace update` now isolates each repo in `.worktrees/.pn-update/<repo>-<run-ts>` on branch `pn-update/<run-ts>`, integrates by fast-forward, and removes the worktree+branch on success; that `--in-place` restores the old flow; the leave-on-failure resume workflow; the asymmetric divergence recovery (`git -C <repo> reset --hard origin/main` / `branch -f main origin/main`, not a merge); and that concurrent runs are unsupported. Reference ADR 0009.

- [ ] **Step 2: Update `pn-workspace-rules/CLAUDE.md`**

In the workspace-rules CLAUDE.md, document the new default for `update`/`upgrade`, the `--in-place` escape, the resume workflow for a left-behind worktree/branch, and that the worktree flow (unlike `--in-place`) does not skip a dirty repo — only a dirty `main` checkout defers.

- [ ] **Step 3: Update `USER_JOURNEYS.md`**

Add/adjust the "update the workspace" journey to reflect the worktree default and the recovery steps.

- [ ] **Step 4: Verify the `--help` text reads correctly**

Run: `cd modules/pn && go run . workspace update --help` and `... workspace upgrade --help`
Expected: both show the `--in-place` flag with the wording from Task 1.

- [ ] **Step 5: treefmt + commit**

```bash
cd phillipg-nix-repo-base
nix fmt docs/worktrees.md pn-workspace-rules/CLAUDE.md pn-workspace-rules/USER_JOURNEYS.md 2>/dev/null || true
git add docs/worktrees.md pn-workspace-rules/CLAUDE.md pn-workspace-rules/USER_JOURNEYS.md
git commit -m "docs(pn): document worktree-isolated update default and recovery"
```

---

## Final verification

- [ ] **Step 1: Full build, vet, test (incl. smoke)**

Run: `cd modules/pn && go build ./... && go vet ./... && go test ./...`
Then the smoke package (build-tagged): `go vet -tags smoke ./internal/workspace/smoke/ && go test -tags smoke ./internal/workspace/smoke/`
Expected: all PASS. (Plain `go test ./...` does NOT run smoke — the `//go:build smoke` tag excludes it; run it explicitly.)

- [ ] **Step 2: Pre-commit / treefmt clean**

Run (repo root): `pre-commit run --all-files` (or rely on the commit hook).
Expected: PASS.

- [ ] **Step 3: Manual sanity (optional, on the real workspace)**

With the workspace clean and on `main`, run `pn workspace update` and confirm: a `.worktrees/.pn-update/<repo>-<ts>` appears during each repo's run, the primary `main` advances, the remote is pushed, and the worktree/branch are gone on success; the summary lists each repo `✓`. Then test `--in-place` still works.

---

## Notes for the executor

- **TDD order:** Tasks 3–5 are written test-first. Tasks 1–4 form one compiling unit (the Task 1 stub bridges the gap); do not push between Task 1 and Task 4 in a way that ships the stub as the real default. If using subagent-driven execution, treat Tasks 1+4 as a pair reviewed together, or keep the stub commit local until Task 4 lands.
- **FakeRunner is exact-match FIFO:** if a test reports `no scripted response for: git -C … …`, the message names the precise call the implementation made — align the script, do not loosen the implementation.
- **`eventlog` import:** `update_worktree.go` references `opts.Log` (typed in `update.go`) but never names `eventlog.` — keep that import out of `update_worktree.go`.
- **`UL_LIB_DIR` resolution order** in `updateViaWorktree`: `opts.ULLibDir` → `os.Getenv("UL_LIB_DIR")` → `ResolveULLibDir(ctx)` → hard error. The env source (new) is what lets the nix-free smoke run; it does not weaken the spec's "non-empty required" guarantee.
- **Smoke is build-tagged** (`//go:build smoke`): every smoke command needs `-tags smoke`, and plain `go build/vet/test ./...` skips the smoke package — vet/test it explicitly.
- **bd:** create one bead per Task (1–8) plus the final-verification gate, mirroring this plan, when execution starts.
