# pn:applied gates — Phase 1: `pn` producer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pn` the producer for `pn:applied` gates — add a workspace `id`, a definitive per-repo applied-state store (single source of truth, replacing the rebuild-skip cache), and a `pn workspace info --json` API that `pb` (Phase 2) consumes.

**Architecture:** `pn workspace apply` already records each repo's applied `HEAD` after a successful `darwin-rebuild` (`markApplied`). This phase (a) adds a committed `[workspace].id` slug, (b) moves that record to one authoritative JSON store under `$XDG_DATA_HOME/pn-workspace/applied/<sha256(checkout-path)>`, repointing both `markApplied` (write) and `needsRebuild` (read) at it, and (c) exposes it via a new `pn workspace info` subcommand built on the existing `Status`/`topoAlpha` path.

**Tech Stack:** Go (gomod2nix engine via `mkGoBinary`), cobra CLI, BurntSushi/toml. Source root: `phillipg-nix-repo-base/modules/pn`. Tests via `go test` + the flake check `pn-go-tests`.

## Global Constraints

- Go packages use the **gomod2nix engine**; commit `gomod2nix.toml` if deps change; **no `vendorHash`**, no `buildGoModule`. (repo-base ADR 0008.)
- **Per-source-digest versioning** — never thread a repo `gitHash` into a build. (ADR 0006.)
- Tests MUST be **isolated**; anything touching the filesystem uses `t.TempDir()` and `t.Setenv` for `XDG_*` dirs (pattern: `modules/pn/internal/workspace/updatecache_test.go`).
- The applied-state store is written **only on a successful apply** and **atomically** (write-temp-then-rename).
- `wsid` is a **human-readable slug** matching `^[a-z0-9][a-z0-9-]*$` (lowercase, digits, dashes).
- Completion gate: `nix flake check` (runs `pn-go-tests`) and `prek run --all-files` (or `pre-commit run --all-files`) MUST pass. Use `git branch --show-current` before committing; work on a feature branch, not `main`.
- Commit message subject style follows the repo (`feat(pn): …`). No `Refs:` line (this is a non-ZR repo).

---

### Task 1: `[workspace].id` slug field + validation

**Files:**

- Modify: `modules/pn/internal/workspace/config.go` (add `Id` to `WorkspaceSection`; validate in `ParseConfig`)
- Test: `modules/pn/internal/workspace/config_test.go`

**Interfaces:**

- Produces: `WorkspaceSection.Id string` (toml key `id`); `ParseConfig` rejects a malformed `id` with an error. Other tasks read `cfg.Workspace.Id`.

- [ ] **Step 1: Write the failing test**

```go
// in config_test.go
func TestParseConfig_WorkspaceID(t *testing.T) {
	cfg, err := ParseConfig([]byte("[workspace]\nid = \"my-ws-01\"\nterminal = \"r\"\n[repos.r]\nurl=\"u\"\n"))
	if err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
	if cfg.Workspace.Id != "my-ws-01" {
		t.Fatalf("id = %q, want my-ws-01", cfg.Workspace.Id)
	}
	if _, err := ParseConfig([]byte("[workspace]\nid = \"Bad_ID\"\nterminal=\"r\"\n[repos.r]\nurl=\"u\"\n")); err == nil {
		t.Fatal("malformed id (uppercase/underscore) should be rejected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestParseConfig_WorkspaceID -v`
Expected: FAIL (`cfg.Workspace.Id` undefined / no validation).

- [ ] **Step 3: Add the field + validation**

In `config.go`, add to `WorkspaceSection` (after `Name`):

```go
	// Id is a stable, committed, human-readable workspace identifier (slug).
	// It is the wsid used by pn:applied gates; machine-invariant.
	Id string `toml:"id,omitempty"`
```

Add a package var + a check inside `ParseConfig` (after unmarshal, alongside the existing validation):

```go
var workspaceIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// inside ParseConfig, after cfg is populated:
	if cfg.Workspace.Id != "" && !workspaceIDRe.MatchString(cfg.Workspace.Id) {
		return nil, fmt.Errorf("workspace.id %q must be a slug: lowercase letters, digits, dashes", cfg.Workspace.Id)
	}
```

Add `"regexp"` to imports if absent.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestParseConfig_WorkspaceID -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/config.go modules/pn/internal/workspace/config_test.go
git commit -m "feat(pn): add validated [workspace].id slug"
```

---

### Task 2: Applied-state store (read/write API, atomic, per-checkout-path keyed)

**Files:**

- Create: `modules/pn/internal/workspace/appliedstate.go`
- Test: `modules/pn/internal/workspace/appliedstate_test.go`

**Interfaces:**

- Produces:
  - `type AppliedState struct { AppliedRef string `json:"applied_ref"`; Dirty bool `json:"dirty"`; AppliedAt string `json:"applied_at"` }`
  - `func appliedStateDir() string` → `$XDG_DATA_HOME/pn-workspace/applied` (fallback `~/.local/share`).
  - `func appliedStateFile(repoDir string) string` → `<dir>/<sha256(filepath.Clean(repoDir))>`.
  - `func writeAppliedState(repoDir string, st AppliedState) error` — atomic temp+rename.
  - `func readAppliedState(repoDir string) (AppliedState, bool, error)` — `ok=false` if no file.

- [ ] **Step 1: Write the failing test**

Create `appliedstate_test.go` in `package workspace`. `hexFilenameRe` is the package-level var already defined in `updatecache.go` (it matches 64-char lowercase hex). Imports: `os`, `testing`.

```go
func TestAppliedState_RoundTripAtomicPerPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a, b := "/ws/repoA", "/ws/repoB"
	if appliedStateFile(a) == appliedStateFile(b) {
		t.Fatal("distinct paths must map to distinct files")
	}
	if _, ok, _ := readAppliedState(a); ok {
		t.Fatal("expected no state before write")
	}
	st := AppliedState{AppliedRef: "deadbeef", Dirty: false, AppliedAt: "2026-06-26T00:00:00Z"}
	if err := writeAppliedState(a, st); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := readAppliedState(a)
	if err != nil || !ok || got != st {
		t.Fatalf("round-trip: got %+v ok=%v err=%v", got, ok, err)
	}
	// no leftover temp files in the dir
	ents, _ := os.ReadDir(appliedStateDir())
	for _, e := range ents {
		if !hexFilenameRe.MatchString(e.Name()) {
			t.Fatalf("unexpected non-hex (temp?) file left behind: %s", e.Name())
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestAppliedState_RoundTripAtomicPerPath -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement the store**

Create `appliedstate.go`:

```go
package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type AppliedState struct {
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
	AppliedAt  string `json:"applied_at"`
}

func appliedStateDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pn-workspace", "applied")
}

func appliedStateFile(repoDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoDir)))
	return filepath.Join(appliedStateDir(), fmt.Sprintf("%x", sum))
}

func writeAppliedState(repoDir string, st AppliedState) error {
	if err := os.MkdirAll(appliedStateDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	final := appliedStateFile(repoDir)
	tmp, err := os.CreateTemp(appliedStateDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}

func readAppliedState(repoDir string) (AppliedState, bool, error) {
	var st AppliedState
	data, err := os.ReadFile(appliedStateFile(repoDir))
	if os.IsNotExist(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}
```

Note: `hexFilenameRe` already exists in `updatecache.go`; the temp file uses a `.tmp-` prefix so it won't match.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestAppliedState_RoundTripAtomicPerPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/appliedstate.go modules/pn/internal/workspace/appliedstate_test.go
git commit -m "feat(pn): add atomic per-repo applied-state store under XDG_DATA_HOME/pn-workspace"
```

---

### Task 3: Repoint `markApplied` (write) and `needsRebuild` (read) at the new store

**Files:**

- Modify: `modules/pn/internal/workspace/updatecache.go` (`markApplied`, `needsRebuild`; retire `stateDir`/`appliedHashDir`/`appliedHashFile`/`cleanStaleHashCacheEntries`)
- Modify/extend: `modules/pn/internal/workspace/updatecache_test.go` (delete obsolete store tests; add the two new ones)
- Fix (callers/tests broken by the new `markApplied` git-status call + retired store): `modules/pn/internal/workspace/apply_test.go`, `modules/pn/internal/workspace/upgrade_test.go`

**Interfaces:**

- Consumes: `writeAppliedState`/`readAppliedState`/`AppliedState` (Task 2). `markApplied`/`needsRebuild` are methods on `*Workspace` (field `runner exec.Runner`, set directly in white-box tests as `&Workspace{runner: f}`).
- Produces: after a successful apply, each repo has an `AppliedState{AppliedRef, Dirty, AppliedAt}`; the rebuild-skip check reads `AppliedRef` from it. The legacy `zn-self-upgrade` files are no longer written or read.

- [ ] **Step 1: Write the failing test** (rebuild-skip reads the new store)

Add this to `updatecache_test.go`. The package is `package workspace` (white-box: the `Workspace.runner` field is unexported and set directly, exactly as the existing `TestNeedsRebuild_*` tests do — see `&Workspace{runner: f}` at `updatecache_test.go:55`). `FakeRunner.AddResponse(name, args, exec.Result, err)` scripts each `(name,args)` pair (consumed FIFO). `needsRebuild` issues `git -C <dir> status --porcelain` then `git -C <dir> rev-parse HEAD`; both must be scripted, matching `TestNeedsRebuild_CleanUnchangedSkips`.

```go
func TestNeedsRebuild_ReadsNewStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const dir = "/repo" // a fake repo dir; the runner is faked, so it need not exist
	f := exec.NewFakeRunner()
	// clean working tree; HEAD == the applied_ref we seed below
	f.AddResponse("git", []string{"-C", dir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	// seed the new store so HEAD matches -> should SKIP
	if err := writeAppliedState(dir, AppliedState{AppliedRef: "abc123"}); err != nil {
		t.Fatal(err)
	}
	rebuild, err := w.needsRebuild(context.Background(), []string{dir}, false, &bytes.Buffer{})
	if err != nil || rebuild {
		t.Fatalf("clean + matching applied_ref should skip rebuild; rebuild=%v err=%v", rebuild, err)
	}
}
```

Imports needed in `updatecache_test.go` for the new tests: `bytes`, `context`, `os`, `path/filepath`, `testing`, and `exec` (`.../internal/exec`) — all already imported by the existing file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestNeedsRebuild_ReadsNewStore -v`
Expected: FAIL (`needsRebuild` still reads `appliedHashFile`).

- [ ] **Step 3: Repoint the read in `needsRebuild`**

In `needsRebuild`, replace the `os.ReadFile(appliedHashFile(dir))` comparison with:

```go
		st, ok, err := readAppliedState(dir)
		if err != nil {
			return false, fmt.Errorf("read applied-state for %s: %w", dir, err)
		}
		if !ok || head != st.AppliedRef {
			return true, nil
		}
```

- [ ] **Step 4: Repoint the write in `markApplied`** and retire the legacy store

Replace the entire body of `markApplied` (currently `updatecache.go:107-125`) with the version below. Note the new body does **no** `os.MkdirAll` and no `cleanStaleHashCacheEntries` call — `writeAppliedState` (Task 2) does its own `MkdirAll` and atomic write:

```go
// markApplied records each repo's current HEAD (and dirty flag) into the
// authoritative applied-state store. Written only after a successful apply.
func (ws *Workspace) markApplied(ctx context.Context, repoDirs []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, dir := range repoDirs {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git rev-parse in %s: %w", dir, err)
		}
		head := strings.TrimSpace(string(res.Stdout))
		st, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "status", "--porcelain"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("git status in %s: %w", dir, err)
		}
		dirty := strings.TrimSpace(string(st.Stdout)) != ""
		if err := writeAppliedState(dir, AppliedState{AppliedRef: head, Dirty: dirty, AppliedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
```

`time` is already imported in `updatecache.go`. (Inlining the `git status` + RFC3339 stamp keeps the diff small; no separate `repoDirty`/`nowISO` helpers are needed.)

Then **retire the legacy `zn-self-upgrade` store**. In `updatecache.go`, delete these now-unused symbols: `stateDir`, `appliedHashDir`, `appliedHashFile`, and `cleanStaleHashCacheEntries`. Keep `hexFilenameRe` (the Task 2 test references it; update its doc comment to drop the `appliedHashFile` mention). After the repoint+deletions, `updatecache.go` no longer references `os`, `crypto/sha256`, or `path/filepath` (the legacy funcs were their only users; `needsRebuild` now calls `readAppliedState` and `markApplied` calls `writeAppliedState`). The remaining required imports are exactly: `context`, `fmt`, `io`, `regexp`, `strings`, `time`, and `.../internal/exec`. **Remove `os`, `crypto/sha256`, and `path/filepath`** — Go fails to compile on unused imports. (Run `cd modules/pn && go build ./internal/workspace/` to confirm the import set.)

Also **delete the obsolete tests** in `updatecache_test.go` that exercise the retired store (otherwise the package will not compile): `TestNeedsRebuild_CleanUnchangedSkips`, `TestMarkApplied_WritesHead`, `TestAppliedHashFile_FullPathKey`, `TestAppliedHashFile_SamePathSameKey`, `TestCleanStaleHashCacheEntries_RemovesBasenameKeyed`, `TestCleanStaleHashCacheEntries_Idempotent`, `TestCleanStaleHashCacheEntries_EmptyDir`, `TestCleanStaleHashCacheEntries_MissingDir`, `TestMarkApplied_SetVsPrimaryNoCollision`, and the `hashRepoDir` helper (only those tests use it). Keep `TestNeedsRebuild_Force`, `TestNeedsRebuild_DirtyTree`, and `TestCheckNixDaemon_ErrorPath` (they don't touch the store). `TestNeedsRebuild_DirtyTree` sets `XDG_STATE_HOME`; that is now harmless but may be left as-is. After deleting, prune any imports that become unused in the test file (`crypto/sha256` and the `hashRepoDir` body's `filepath`/`fmt` usage).

- [ ] **Step 4b: Fix `apply_test.go`** — `Apply` calls `markApplied` on its success path, and the new `markApplied` issues a `git status --porcelain` per repo _in addition to_ `git rev-parse HEAD`. The existing apply tests script only the rev-parse, and one references the retired store directly, so they break. Make these edits in `internal/workspace/apply_test.go`:
  - `TestApply_RunsApplyCommandWithOverrides`: after the two `rev-parse HEAD` responses (for `depDir` and `leafDir`), add a clean `status --porcelain` response for each:
    ```go
    f.AddResponse("git", []string{"-C", depDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
    f.AddResponse("git", []string{"-C", leafDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
    ```
  - `applyTestRunner` helper: after its `rev-parse HEAD` response for `leafDir`, add:
    ```go
    f.AddResponse("git", []string{"-C", leafDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
    ```
    (This covers `TestApply_RestartsFsmonitorWhenGitVersionChanges` and `TestApply_NoFsmonitorRestartWhenGitUnchanged`, which both build on it.)
  - `TestApply_NoFsmonitorRestartOnSkippedRebuild`: this test pre-seeds the legacy store and uses `XDG_STATE_HOME`. Repoint it at the new store: replace `t.Setenv("XDG_STATE_HOME", t.TempDir())` with `t.Setenv("XDG_DATA_HOME", t.TempDir())`, and replace the `os.MkdirAll(appliedHashDir(), …)` + `os.WriteFile(appliedHashFile(leafDir), []byte("abc\n"), …)` block with a single `writeAppliedState` call (which does its own MkdirAll):
    ```go
    if err := writeAppliedState(leafDir, AppliedState{AppliedRef: "abc"}); err != nil {
        t.Fatalf("seed applied state: %v", err)
    }
    ```
    Leave its scripted `status --porcelain` + `rev-parse HEAD` for `leafDir` as-is (the skip path runs `needsRebuild`, not `markApplied`). After this edit `os` may become unused in the file — if so, drop it from the imports.
  - Other apply tests (`TestApply_ErrorsWhenApplyCommandMissing`, `TestApply_ShowNixCommandsOnly`, `TestAllRepoDirs_*`) never reach `markApplied`'s success path, so they need no change; but run the whole file to confirm.

- [ ] **Step 4c: Fix `upgrade_test.go`** — `TestUpgrade_RunsUpdateThenApply` drives `Update`→`Apply`→`markApplied` and breaks the same way (and references the legacy store dir). In `internal/workspace/upgrade_test.go`:
  - The `markApplied` phase now also issues `git status --porcelain` for `dep` and `leaf`. The test already scripts a _dirty_ `status --porcelain` for `dep` (consumed FIFO by `needsRebuild`), so add fresh `status --porcelain` responses for **both** repos for the `markApplied` phase, right after the two `markApplied` `rev-parse HEAD` responses:
    ```go
    f.AddResponse("git", []string{"-C", dep, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
    f.AddResponse("git", []string{"-C", leaf, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
    ```
  - Remove the now-vestigial legacy-store seeding (the `hashDir := filepath.Join(stateDir, "zn-self-upgrade", …)` + `os.MkdirAll(hashDir, …)` block); `writeAppliedState` self-creates its dir. `t.Setenv("XDG_STATE_HOME", …)` may stay (the update phase's event log still uses it) — only delete the `zn-self-upgrade` MkdirAll. If `os` becomes unused after the deletion, drop it from the imports.

- [ ] **Step 5: Add the fail-closed test** (record-write-fails after success)

The new `markApplied` issues `git rev-parse HEAD` and `git status --porcelain` for each dir _before_ calling `writeAppliedState`, so both must be scripted or the test fails on an unscripted-call error instead of the store-write error. The write fails because `appliedStateDir()` resolves under `XDG_DATA_HOME`, which is pointed at a regular file, so `writeAppliedState`'s internal `os.MkdirAll` errors.

```go
func TestMarkApplied_WriteFailIsReturned(t *testing.T) {
	// Point XDG_DATA_HOME at a regular file (not a dir) so writeAppliedState's
	// MkdirAll under it fails.
	bad := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", bad)
	const dir = "/repo"
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	f.AddResponse("git", []string{"-C", dir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	w := &Workspace{runner: f}
	if err := w.markApplied(context.Background(), []string{dir}); err == nil {
		t.Fatal("markApplied must return the store-write error (fail-closed)")
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd modules/pn && go test ./internal/workspace/ -run 'TestNeedsRebuild_ReadsNewStore|TestMarkApplied_WriteFailIsReturned' -v`
Expected: PASS. Then run the full package `cd modules/pn && go test ./internal/workspace/` to confirm the obsolete tests deleted in Step 4 are gone and nothing else regressed (e.g. `apply_test.go` / smoke that exercises `markApplied`). If the build fails on an unused import or a still-referenced retired symbol, finish the deletions from Step 4.

- [ ] **Step 7: Commit**

```bash
git add modules/pn/internal/workspace/updatecache.go \
        modules/pn/internal/workspace/updatecache_test.go \
        modules/pn/internal/workspace/apply_test.go \
        modules/pn/internal/workspace/upgrade_test.go
git commit -m "feat(pn): single applied-state store; markApplied writes + needsRebuild reads it (retire zn-self-upgrade)"
```

---

### Task 4: `pn workspace info [--json]` subcommand

**Files:**

- Create: `modules/pn/internal/workspace/info.go` (the `Info` method + struct)
- Modify: `modules/pn/internal/cli/workspace.go` (register `workspaceInfoCmd`)
- Test: `modules/pn/internal/workspace/info_test.go`

**Interfaces:**

- Consumes: `cfg.Workspace.Id`, `topoAlpha`/config iteration (the no-nix-eval path `Status` uses), `readAppliedState` (Task 2).
- Produces: `func (ws *Workspace) Info(ctx context.Context) (WorkspaceInfo, error)` where
  `WorkspaceInfo{ Wsid string `json:"wsid"`; Root string `json:"root"`; Terminal string `json:"terminal"`; Repos []RepoInfo `json:"repos"` }`,
  `RepoInfo{ Name string `json:"name"`; Path string `json:"path"`; AppliedRef string `json:"applied_ref"`; Dirty bool `json:"dirty"` }`.
  CLI: `pn workspace info [--json]` prints the struct (JSON when `--json`). **This is the contract Phase 2's `pb` parses.**

- [ ] **Step 1: Write the failing test**

There is **no** `newTestWorkspace`/`repoPath` helper in the `workspace` package — those live in the `cli` package's test files and are not exported. Inside `package workspace` tests, construct a workspace exactly as `status_test.go` does: write `pn-workspace.toml` into `t.TempDir()` with the package helper `writeFile(t, path, body)` (defined in this package's test files), then `Open(root, runner)`. The repo's on-disk path is `filepath.Join(root, "<repokey>")`, which is what `Info` derives via `ws.root` (accessible in-package). Add this to `info_test.go`:

```go
func TestInfo_JoinsConfigAndAppliedState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "ws1"
terminal = "r"

[repos.r]
url = "github:owner/r"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repoPath := filepath.Join(root, "r")
	if err := writeAppliedState(repoPath, AppliedState{AppliedRef: "abc123", Dirty: false}); err != nil {
		t.Fatal(err)
	}
	info, err := w.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Wsid != "ws1" || info.Root == "" {
		t.Fatalf("wsid/root: %+v", info)
	}
	if info.Terminal != "r" {
		t.Fatalf("terminal: %+v", info)
	}
	if len(info.Repos) != 1 || info.Repos[0].Path != repoPath || info.Repos[0].AppliedRef != "abc123" {
		t.Fatalf("repos: %+v", info.Repos)
	}
}
```

Imports for `info_test.go`: `context`, `path/filepath`, `testing`, and `.../internal/exec`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestInfo_JoinsConfigAndAppliedState -v`
Expected: FAIL (`Info` undefined).

- [ ] **Step 3: Implement `Info`**

Create `info.go` in `package workspace`. Iterate repos via `ws.topoAlpha(ctx)` — the **same path `Status` uses** (NOT `Discover`, which forces a per-repo `nix eval` fan-out). For repos with no `flake.nix` on disk (the test case, and the post-apply hook case once the disk lock matches config), `topoAlpha` returns without invoking `nix` (Tier-1 lock match or the alphabetical Tier-3 fallback). For each repo name build a `RepoInfo` with `Path = filepath.Join(ws.root, name)` and `AppliedRef`/`Dirty` from `readAppliedState(path)` (zero-valued when the second return `ok` is false). Read identity from the in-package fields `ws.root` and `ws.config.Workspace` (both accessible since `Info` is in `package workspace`):

```go
package workspace

import (
	"context"
	"path/filepath"
)

// WorkspaceInfo is the stable JSON contract emitted by `pn workspace info`.
type WorkspaceInfo struct {
	Wsid     string     `json:"wsid"`
	Root     string     `json:"root"`
	Terminal string     `json:"terminal"`
	Repos    []RepoInfo `json:"repos"`
}

// RepoInfo is one repo's identity + applied state.
type RepoInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
}

// Info joins the configured repos with their per-repo applied-state records.
// It uses the topoAlpha (no-nix-eval) iteration order, never Discover.
func (ws *Workspace) Info(ctx context.Context) (WorkspaceInfo, error) {
	info := WorkspaceInfo{
		Wsid:     ws.config.Workspace.Id,
		Root:     ws.root,
		Terminal: ws.config.Workspace.Terminal,
	}
	for _, name := range ws.topoAlpha(ctx) {
		path := filepath.Join(ws.root, name)
		ri := RepoInfo{Name: name, Path: path}
		if st, ok, err := readAppliedState(path); err != nil {
			return WorkspaceInfo{}, err
		} else if ok {
			ri.AppliedRef = st.AppliedRef
			ri.Dirty = st.Dirty
		}
		info.Repos = append(info.Repos, ri)
	}
	return info, nil
}
```

- [ ] **Step 4: Register the CLI command**

The package is `internal/cli`; the workspace handle is obtained via `openWorkspace()` (the package-level var that tests stub), not a captured `ws`. Mirror `workspaceDiscoverCmd` (no `runWithHooks` — `info` is not a hook command and must not be added to `knownHookCommands`). In `cli/workspace.go`:

(1) Register it inside `addWorkspaceCmd`, next to the other `ws.AddCommand(...)` lines (e.g. after the `discover` line):

```go
	ws.AddCommand(workspaceInfoCmd(&terminalFlag))
```

(2) Add the command factory (the `terminal` param is accepted for flag-uniformity with the other verbs even though `Info` does not consume it yet):

```go
func workspaceInfoCmd(_ *string) *cobra.Command {
	var infoJSON bool
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show the workspace identity and per-repo applied state",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			info, err := w.Info(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if infoJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintf(out, "wsid:     %s\n", info.Wsid)
			fmt.Fprintf(out, "root:     %s\n", info.Root)
			fmt.Fprintf(out, "terminal: %s\n", info.Terminal)
			for _, r := range info.Repos {
				applied := r.AppliedRef
				if applied == "" {
					applied = "(none)"
				}
				dirty := ""
				if r.Dirty {
					dirty = " (dirty)"
				}
				fmt.Fprintf(out, "  %s\t%s\t%s%s\n", r.Name, r.Path, applied, dirty)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&infoJSON, "json", false, "JSON output")
	return cmd
}
```

Add `"encoding/json"` to the `cli/workspace.go` imports (`fmt`, `cobra`, `exec`, `workspace`, `os`, `path/filepath`, `context`, `eventlog` are already imported). `cmd.Context()` is valid (cobra sets it; other subcommands use `context.Background()` but `Context()` is fine and inherits cancellation).

- [ ] **Step 5: Add the "no nix eval" assertion test**

`Info` reads only applied-state files + config, and iterates via `topoAlpha`. With repos declared in TOML but **no `flake.nix` on disk** (and no lock), `topoAlpha` falls back to the alphabetical order without invoking `nix` (verified: `gatherInputURLs` skips any repo whose flake is absent). So with a `FakeRunner` that scripts nothing, `Info` must complete with **zero** `nix` calls. Add to `info_test.go`:

```go
func TestInfo_NoNixEval(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
id = "ws1"
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Info(context.Background()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			t.Fatalf("info must not invoke nix eval; saw %v", c.Args)
		}
	}
}
```

- [ ] **Step 6: Run tests + manual smoke**

Run (from `modules/pn`): `cd modules/pn && go test ./internal/workspace/ -run 'TestInfo_' -v` → PASS. Also run the CLI package: `cd modules/pn && go test ./internal/cli/ -run 'TestWorkspaceInfo' -v` if you added a cli-level smoke (optional — the existing `cli` tests will at least confirm the command registers without breaking the `--terminal` persistence tests).
Manual (optional, requires nix + a real workspace; from the repo root so `./result` resolves): `nix build .#pn` then run the built binary against the real workspace, e.g.
`PN_WORKSPACE_ROOT=/Users/phillipg/phillipg_mbp "$PWD/result/bin/pn" workspace info --json | jq .` → shows `{wsid,root,terminal,repos:[…]}`. (`wsid` is `""` until the downstream `pn-workspace.toml` sets `[workspace].id` — Phase-4 wiring; the field is still present.)

- [ ] **Step 7: Commit**

```bash
git add modules/pn/internal/workspace/info.go modules/pn/internal/workspace/info_test.go modules/pn/internal/cli/workspace.go
git commit -m "feat(pn): add 'pn workspace info [--json]' applied-state API"
```

---

### Task 5: `wsid` duplicate detection (machine-local registry, MUST-fail)

**Files:**

- Create: `modules/pn/internal/workspace/wsidregistry.go`
- Modify: `modules/pn/internal/workspace/apply.go` (call the registry check at apply start)
- Test: `modules/pn/internal/workspace/wsidregistry_test.go`

**Interfaces:**

- Produces: `func checkWsidUnique(wsid, root string) error` — records `wsid → root` under `$XDG_DATA_HOME/pn-workspace/wsids/<wsid>` (atomic); returns an error if the stored root differs from `root` (a different workspace claims the same wsid on this machine).

- [ ] **Step 1: Write the failing test**

```go
func TestWsidRegistry_DuplicateFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := checkWsidUnique("ws1", "/ws/a"); err != nil { t.Fatalf("first claim: %v", err) }
	if err := checkWsidUnique("ws1", "/ws/a"); err != nil { t.Fatalf("same root re-claim must pass: %v", err) }
	if err := checkWsidUnique("ws1", "/ws/b"); err == nil {
		t.Fatal("a different root claiming the same wsid MUST fail")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modules/pn && go test ./internal/workspace/ -run TestWsidRegistry_DuplicateFails -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement the registry** — create `wsidregistry.go` in `package workspace`. Store one file per wsid under `$XDG_DATA_HOME/pn-workspace/wsids/<wsid>` containing the cleaned root path. Read-compare first (idempotent re-claim from the same root passes); on a different stored path, fail. Write atomically (temp+rename), consistent with Task 2's store. The `<wsid>` is already slug-validated by `ParseConfig` (Task 1), so it is filesystem-safe as a filename.

```go
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

func wsidRegistryDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pn-workspace", "wsids")
}

// checkWsidUnique records wsid -> root in the machine-local registry and fails
// if a different root already claims this wsid (a same-machine duplicate). A
// re-claim from the same root is a no-op success. Cross-machine uniqueness is
// the operator's responsibility (the slug is human-chosen).
func checkWsidUnique(wsid, root string) error {
	root = filepath.Clean(root)
	file := filepath.Join(wsidRegistryDir(), wsid)
	if data, err := os.ReadFile(file); err == nil {
		stored := string(data)
		if stored != root {
			return fmt.Errorf("wsid %q already used by workspace %q (must be unique per machine)", wsid, stored)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(wsidRegistryDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(wsidRegistryDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.WriteString(root); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, file)
}
```

- [ ] **Step 4: Wire into apply** — in `Apply` (`apply.go`), insert the check right after `requireTerminal` succeeds (currently `apply.go:30-33`, before `terminalDir`/overrides/build). Use the in-package fields directly:

```go
	if id := ws.config.Workspace.Id; id != "" {
		if err := checkWsidUnique(id, ws.root); err != nil {
			return err
		}
	}
```

`apply.go` already imports `fmt`; no new imports are needed there. Note: this runs on the `ShowNixCommandsOnly` (dry-run) path too if placed before that early-return — to keep dry-run side-effect-free, place the block **after** the `if opts.ShowNixCommandsOnly { … return nil }` guard (`apply.go:57-60`) but before `checkNixDaemon`.

- [ ] **Step 5: Run tests** — `cd modules/pn && go test ./internal/workspace/ -run TestWsidRegistry -v` → PASS; full package green.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/workspace/wsidregistry.go modules/pn/internal/workspace/apply.go modules/pn/internal/workspace/wsidregistry_test.go
git commit -m "feat(pn): fail apply on duplicate workspace id (machine-local registry)"
```

---

### Task 6: ADRs

**Files:**

- Create: `docs/adr/0012-pn-applied-state-store-and-info-api.md` (0012 is the next free number — current max is 0011)
- Modify: `docs/adr/0002-pn-workspace-toml-schema.md` (amend the `[workspace]` schema to include `id`) + `docs/adr/index.md`

Use the ADR template/format already in this repo's ADRs (header block `# ADR-NNNN: …` / `**Date:** … **Status:** … **Deciders:** …`, then `## Context` / `## Decision` / `## Consequences` / `## Alternatives Considered`). Match the heading style of `0011-source-digest-in-derivation-version.md`.

- [ ] **Step 1:** Write ADR 0012 (Status: Accepted) — Context: gates need a definitive "what's applied" record; the rebuild-skip cache is the wrong store to overload. Decision: single per-repo applied-state store under `$XDG_DATA_HOME/pn-workspace/applied/<sha256(checkout-path)>` (JSON `{applied_ref, dirty, applied_at}`), written on success, read by both `needsRebuild` and `pn workspace info`; legacy `$XDG_STATE_HOME/zn-self-upgrade/apply/applied-hash` retired. Plus the `pn workspace info --json` schema (`{wsid, root, terminal, repos:[{name, path, applied_ref, dirty}]}`) as a stable consumed API, and the machine-local `wsids/` registry (Task 5). Consequences + Alternatives (two parallel stores; reuse cache as-is). Cross-ref: "See also: phillipgreenii-nix-agent-support docs/adr/0018-… (the `pb` tool + `pn:applied` gate contract)".
- [ ] **Step 2:** Amend `docs/adr/0002-pn-workspace-toml-schema.md` to add `id` to the documented `[workspace]` schema (slug `^[a-z0-9][a-z0-9-]*$`, machine-invariant, the wsid used by `pn:applied` gates). Update its `## Decision` bullet that currently reads "[workspace] section: name, description" to include `id`; bump its status note to "Accepted (amended by 0012)" if following the repo's amendment convention (see how 0006 records "amended by 0011").
- [ ] **Step 3:** Update `docs/adr/index.md` — add a table row `| [0012](0012-pn-applied-state-store-and-info-api.md) | … | Accepted |` (the index is a 3-column markdown table: ADR link, Title, Status) and reflect the 0002 amendment note in its Status cell.
- [ ] **Step 4: Commit**

```bash
git add docs/adr/
git commit -m "docs(adr): 0012 applied-state store + info API; amend 0002 for [workspace].id"
```

---

### Phase-1 completion gate

- [ ] `cd modules/pn && go test ./...` green.
- [ ] From repo root: `nix flake check` (runs `pn-go-tests`) and `prek run --all-files` pass.
- [ ] `pn workspace info --json` emits `{wsid, root, terminal, repos:[{name,path,applied_ref,dirty}]}` on the real workspace.
- [ ] Confirm on a feature branch (not `main`); open PR / hand to the finishing-a-development-branch flow.

---

## Phases 2 & 3 — roadmap (to be expanded into their own plans)

**Phase 2 — `pb` tool (`phillipgreenii-nix-agent-support`)** — depends on Phase 1's `pn workspace info --json`:

- T1 Scaffolding: `packages/pb/` (`cmd/pb`, `mkGoApp`, committed `gomod2nix.toml`, `wrapProgram --prefix PATH [pn bd git]`), `home/programs/pb/` option module, `flake.nix` overlay + `packages` re-export + per-package gofmt/golangci-lint pre-commit hooks. Template: `packages/pr-pool`.
- T2 Duration parser: `ms`..`d` (custom `d`=24h on top of `time.ParseDuration`), reject `<1ms`/`0`/negative/bare-number. Unit tests for accept + reject cases.
- T3 `pb gate create`: `--commit` defaults to `HEAD`; validate repo via `pn workspace info`; `git patch-id --stable`; create gate co-located in the bead's DB; write `metadata.applied_baseline`; create the bead **deferred** (fleet-race test); `--json` schema.
- T4 `pb gate check`: walk-up `.beads` discovery **bounded at workspace root** + dedupe by Dolt identity; `BD_JSON_ENVELOPE=1` + parse envelope; `--limit 0`; baseline-ancestry (`merge-base --is-ancestor`, else `--last-n` default 100); bounded `git log -p | git patch-id --stable` match; resolve in the gate's DB; dirty lenient vs `--strict`; **`--dry-run`** (no mutation on resolve _and_ stale); stale-handler (`convert-to-human` default / `close`, default `--stale-after=3d`); best-effort + non-zero report; `--json` schema.
- T5 Contract tests (build-tagged `//go:build contract`): bd gate surface (incl. `BD_JSON_ENVELOPE=1` envelope, cross-DB block doesn't hold, `metadata` round-trip), `git patch-id` (rebase-stable, within-context MISS + squash LOSS deterministic, binary works, `--stable`≠`--verbatim`), `pn workspace info --json` schema.
- T6 ADR 0018 (`pb` + the `pn:applied` contract: `await_id` grammar, dedupe key, co-location, baseline).

**Phase 3 — plugin + smoke + wiring:**

- T1 `pb` Claude plugin/skill (agent-support marketplace): teaches commit → create bead **deferred** → `pb gate create` (HEAD default) → un-defer; recommend single-commit gating.
- T2 Reusable smoke harness: extend `modules/pn/.../smoke` (workspace + git, `file://` bare remotes) as the `pn` layer; a `pb` layer adds an isolated bd/Dolt (pattern: `packages/pg-pr/pkg/beads/mergerequest_test.go`); assert the happy-path user story + ms-precision stale.
- T3 Downstream wiring (machine flake, out of repo scope here): nix-render `pn-workspace.toml` with `[workspace].id` (committed value) + `[hooks.apply].post = pb gate check`.

---

## Plan review notes (2026-06-26)

Reviewed against the real codebase at `phillipg-nix-repo-base/modules/pn` and **end-to-end verified**: I implemented Tasks 1–5's code blocks in a throwaway copy, ran `go build ./...`, `go vet`, the targeted `-run` set, and `go test ./...` (all green — the 7 new tests pass exactly as written), then reverted. The smoke package (`-tags smoke`) also still compiles. Corrections made to the plan:

**Symbol/signature accuracy (confirmed correct, no change needed):** `ParseConfig` + `WorkspaceSection` (field `Name` exists; `Id` slot is new). `markApplied`/`needsRebuild`/`appliedHashFile`/`hexFilenameRe` all exist in `updatecache.go`. `Workspace` fields are unexported `root`, `config`, `runner` (white-box tests set `&Workspace{runner: f}`). `exec.NewFakeRunner()` takes no args; `AddResponse(name, args, exec.Result, err)`; `FakeRunner.Calls()` exists for the no-nix assertion. `topoAlpha` and `Status` iterate repos without `nix eval` **when no repo flake is on disk** (verified by probe) — `gatherInputURLs` skips repos whose `flake.nix` is absent, so the `TestInfo_NoNixEval` assertion holds in the test setup.

**Soft notes replaced with concrete code:**

- Task 3 Step 1/5: replaced "match existing helper usage" with the exact `FakeRunner` scripting (clean `status --porcelain` + `rev-parse HEAD`) and `&Workspace{runner: f}` construction, plus the import list.
- Task 3 Step 4: gave the full `markApplied` body, the exact symbols to delete (`stateDir`/`appliedHashDir`/`appliedHashFile`/`cleanStaleHashCacheEntries`), and the **exact final import set** for `updatecache.go` (remove `os`, `crypto/sha256`, `path/filepath`).
- Task 4: there is **no** `newTestWorkspace`/`repoPath`/`newTestWorkspaceWithRunner` in the `workspace` package (those live in `internal/cli` test files) — rewrote both `Info` tests to use `writeFile(...)` + `Open(root, runner)` and an in-package `filepath.Join(root, key)`. Gave the full `Info` implementation and a correct CLI factory (`workspaceInfoCmd(_ *string)` using `openWorkspace()`, registered via `ws.AddCommand(...)`, `encoding/json` import) mirroring `workspaceDiscoverCmd`; replaced the undefined `printInfoHuman` with an inline human printer.
- Task 5: gave the full `checkWsidUnique` + `wsidRegistryDir` implementation and the exact insertion point in `Apply` (after the `ShowNixCommandsOnly` guard, before `checkNixDaemon`, gated on `id != ""`).

**Coverage gaps fixed (these would have broken `go test ./...`):**

- The new `markApplied` issues an extra `git status --porcelain` per repo, and the retired store removes `appliedHashDir`/`appliedHashFile`. Added **Step 4b** (fix `apply_test.go`: add status responses in `TestApply_RunsApplyCommandWithOverrides` + `applyTestRunner`; repoint `TestApply_NoFsmonitorRestartOnSkippedRebuild` to `writeAppliedState` + `XDG_DATA_HOME`; drop now-unused `os` import) and **Step 4c** (fix `upgrade_test.go`: add the two markApplied-phase status responses; remove the legacy `zn-self-upgrade` MkdirAll; drop the now-unused `os` import). Verified: both files needed the `os` import removed.
- Step 4 also enumerates the obsolete `updatecache_test.go` tests to delete (they reference retired symbols and won't compile otherwise) and the `hashRepoDir` helper.

**Path/command fixes:** Task 4 Step 6 manual smoke used `nix build .#pn && cd …/phillipg_mbp && ./result/bin/pn` — `./result` would not resolve after `cd`-ing away from the repo root; replaced with `"$PWD/result/bin/pn"` run from repo root + `PN_WORKSPACE_ROOT`. Task 6 file glob `0002-*.md` replaced with the concrete `docs/adr/0002-pn-workspace-toml-schema.md` and added the index table-row format; confirmed `0012` is the next free ADR number.

**Could not verify:** `nix flake check` / `prek run --all-files` (the Phase-1 completion gate) were not run here — they require the nix toolchain; I exercised the equivalent `go build`/`go vet`/`go test ./...` directly. No `golangci-lint`/`gofmt`/`go vet` Go hooks exist in this repo's pre-commit config, so leaving a retired-but-referenced function would compile — but the plan deletes them outright, which is cleaner. The ADR bodies (Task 6) are prose and were not drafted.

**Residual risk (low):** In production, if the on-disk lock does **not** match config and repos _do_ have flakes on disk, `Info`'s `topoAlpha` Tier-2 path (`effectiveLock` → `gatherInputURLs`) _can_ invoke `nix eval`. The design spec accepts building on `topoAlpha`, and the post-apply common case has a matching lock (Tier-1, no eval); but if a future change must guarantee `Info` is nix-free unconditionally, iterate via `orderedRepoNames(ws.config.Repos)` instead of `topoAlpha` (ordering is irrelevant for the JSON repo list). Noted for the implementer, not blocking.
