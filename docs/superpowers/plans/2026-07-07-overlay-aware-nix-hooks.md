# Overlay-Aware Nix Hooks (uniform event hooks) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `pn workspace install-hooks` subcommand with **uniform event-based hooks**. Both `[[hooks]]` (workspace, run once at root) and `[[repos.<r>.hooks]]` (per-repo) are `{ when, run }` lists; a `{nix_run <attr>}` token in a per-repo `run` expands to `nix run <--override-input flags> <repo-flake-dir>#<attr>`, so pre-commit gates rebuild against _local_ siblings after clone/rebase/update.

**Architecture (Option A — uniform reshape):** `HookCommand{Pre,Post}` map → `RepoHook{When,Run}` list, on both `WorkspaceConfig.Hooks []RepoHook` and `RepoConfig.Hooks []RepoHook`. An **event** is `<phase>-<command>` (`post-rebase`, `pre-build`). When command `C` runs, workspace `[[hooks]]` whose `when` has `pre-C`/`post-C` run once at root; for each repo `C` _processes_ (repo-iterating + `upgrade` → all repos; `build`/`apply` → terminal), that repo's per-repo hooks whose `when` has `pre-C`/`post-C` run in the repo (`cwd=repo`, `{nix_run}` → that repo, overrides from `effectiveLock`). `{nix_run}` is valid only in per-repo hooks. Because this reshapes the config, **every referencing site is updated**: the ordered-TOML writers, `filterConfig`, and the `EnforceKeys` apply-time enforcer (ADR 0017, resemanticized to ensure-present). The subcommand, `RepoConfig.InstallHooks`, and `install_hooks.go` are removed; workforest fires `post-clone` on new worktrees.

**Tech Stack:** Go (stdlib + `exec.FakeRunner`), gomod2nix (`nix build .#pn`), pn workspace tooling, nix flakes.

## Global Constraints

- Module path: `github.com/phillipgreenii/nix-repo-base/modules/pn`.
- gomod2nix (ADR 0008); pure first-party edits → no `gomod2nix.toml` regen.
- **`FakeRunner` matches EXACT `(name,args)`**; reconstruct the deterministic `sh -c <string>`. `exec.Call{Name,Args,Opts.Dir,Opts.Stdout}`. Workspace tests: `writeFile(...)` + `Open(root,f)`.
- Hooks run `sh -c <string>` with `RunOptions{Dir}`. **pre-\* aborts; post-\* warns.**
- **cwd is load-bearing** — `install-pre-commit-hooks` installs into `$PWD`; every per-repo run uses `cwd == that repo`.
- **Overrides from `effectiveLock(ctx)`**, not raw `ws.lock`. `effectiveLock` returns `emptyLock()` on derive error, so `repoNixHookVars` MUST **warn** on non-nil error (else silent empty overrides = the regression).
- Overrides CLI-only (`--override-input <alias> git+file://<dir>` triples, `helpers.go:89`). Never write local paths into flake.nix/flake.lock.
- Flake dir lock-first via `resolveFlakePath`. `Dir("flake.nix")="."`, `Dir("nix/flake.nix")="nix"`.
- All work in the set `.workforests/pg2-5yq5-nix-hooks`; never canonical checkouts.
- Completion gate: `nix build .#pn` → `pn workspace flake-check` (Tier 2). `pn workspace build` (Tier 3) before apply.
- Bead: implements **pg2-5yq5**, supersedes **pg2-ic7x** (a2fc790, 728bdac). Orthogonal: **pg2-mbi5**.

### Referencing sites to update (the reason Task 3 is a batch)

Confirmed by review — all read the old `map[string]HookCommand` / `HookCommand`:

- `enforce_keys.go`: `EnforceKeys` `cfg.Hooks["apply"]` (`:80-83`) + `orderedConfig.Hooks` (`:118`). **Task 7** (semantic redesign + ADR 0017).
- `init.go:141-149`, `init.go:181-189`: two `orderedConfig` writers. **Task 3.**
- `workforest_subset.go:84-92` (`orderedConfig`) + `:16-20` (`filterConfig` `Hooks: cfg.Hooks`). **Task 3.**
- `config_test.go`: `sampleTOML` (`:8-28`, map-shape hooks, used by 5 tests) + `TestParseConfig_Hooks` (`:68`), `TestParseConfig_RejectsUnknownHookCommand` (`:86`), `TestKnownHookCommands` (`:228`), `TestParseConfig_InstallHooks*` (`:174-224`). **Task 3.**
- `cli/hooks_test.go:58,86,105,127` (4 tests, `[hooks.update]` map) + `fuzz_test.go:12` (seed). **Task 9.**
- ADR `0002` (schema) amend + ADR `0017` (enforce contract, semantic change) amend. **Tasks 7, 10.**

### Migration

- `[hooks.<cmd>] {pre,post}` map → `[[hooks]] {when,run}` list. Live hook migrates: `[hooks.apply] post=['pb gate check']` → `[[hooks]] when=['post-apply'] run=['pb gate check']`.
- `RepoConfig.InstallHooks` removed (none in workspace). `sync-hooks` alias removed.
- go-toml/v2 is lenient (no DisallowUnknownFields) and supports `[[…]]` array-of-tables into `[]RepoHook`.
- **HARD atomic cutover (verified, go-toml/v2 v2.3.1).** An old `[hooks.apply]` _table_ does NOT lenient-skip into `Hooks []RepoHook` — it errors `toml: cannot store a table in a slice`; symmetrically, old pn errors on `[[hooks]]`. So the deployed pn/enforce binary and the committed `pn-workspace.toml` hook shape MUST switch **together**. Critically, `pn-workspace-toml-enforce` runs in `home.activation` during the switching generation — old binary + migrated toml, or new binary + un-migrated toml, both **fail activation**. This is the same class as the original `[hooks.clone]`-vs-old-pn deadlock. Land+apply the new pn and migrate the canonical toml as one coordinated step; keep the migrated shape only in the set until then.

---

## Design reference

**Event:** `<phase>-<command>`; `splitEvent` parses it.

```go
var hookableCommands = map[string]struct{}{
    "clone":{}, "rebase":{}, "update":{}, "status":{}, "flake-check":{}, "format":{},
    "push":{}, "pre-commit-check":{}, "build":{}, "apply":{}, "upgrade":{}, "lock":{}, "init":{}, "tree":{},
}
var repoIteratingCommands = map[string]struct{}{
    "clone":{}, "rebase":{}, "update":{}, "status":{}, "flake-check":{}, "format":{}, "push":{}, "pre-commit-check":{},
}
```

**Hook:** `type RepoHook struct { When []string \`toml:"when"\`; Run []string \`toml:"run"\` }`— on`WorkspaceConfig.Hooks []RepoHook \`toml:"hooks,omitempty"\``and`RepoConfig.Hooks []RepoHook \`toml:"hooks,omitempty"\``.

**`{nix_run <attr>}`** → `nix run --override-input <a> 'git+file://<dir>' … '<absFlakeDir>#<attr>'`. Token regex `` `\{nix_run\s+([A-Za-z0-9._-]+)\}` ``; ≤1/entry; valid only in per-repo hooks.

**Firing:** workspace `[[hooks]]` with `pre-C`/`post-C` → root once. Per-repo hooks with `pre-C`/`post-C` → in each repo `C` processes.

**processedReposFor(ctx, cmd):** `repoIteratingCommands` or `upgrade` → `topoAlpha(ctx)` (note: `upgrade`'s inner update/apply don't re-enter `runWithHooks`, so use `post-upgrade` to resync on upgrade); `build`/`apply` → `[terminal]` (empty if unset); else `nil`.

**Build ordering (STUB strategy — reviewer-confirmed).** Reshaping `config.go`'s struct breaks its _dependent_ compile sites. Some are mechanical and fixed in the same batch: the `init.go`/`workforest_subset.go` writers and **`enforce_keys.go` (Task 7 MUST land inside this batch** — it shares `orderedConfig`/`cfg.Hooks`). Two others cannot be _fully_ fixed until later tasks: `workforest.go` (`:126`,`:306` call the removed `InstallHooksInDir`) needs Task 8, and `cli/hooks.go` `runWithHooks` (`:14` map-indexes `.Hooks[name]`, `:18/:22` use `.Pre`/`.Post`) needs Task 9 — and both those fixes call `RunEventHooks`/`ProcessedReposFor`, which are **Task 4** deliverables. To keep every commit green without pulling everything into one giant task, **Task 3 STUBS the two late-fixable sites**: `workforest.go` install calls → temporary no-op; `runWithHooks` → `return fn()`; and it `t.Skip`s the 3 `workforest_install_hooks_test.go` + 4 `cli/hooks_test.go` cases (with a `TODO(pg2-5yq5)` naming the un-stubbing task). The atomic green unit is then **Task 3 alone** (struct + writers + enforce + stubs + subcommand/`install_hooks.go` removal → green `go build`/`go vet`/`go test`). Tasks 4/5/6 are additive. **Task 8** replaces the workforest stub and unskips its tests; **Task 9** replaces the `runWithHooks` stub and unskips the cli tests + updates the `fuzz_test.go` seed (which lives in `internal/workspace`, not cli — its seed is a string literal so it never blocks the build; update for cleanliness). Tasks 1/2 are additive and land green _before_ Task 3.

---

## Task 1: `{nix_run <attr>}` template expansion (pure)

**Files:** `workspace/template.go`; `workspace/template_test.go`. **Produces:** `nixHookVars`, `expandNixRunTokens`.

- [ ] **Step 1: Failing test** — (same as prior revisions)

```go
func TestExpandNixRunTokens_ExpandsWithOverridesAndQuoting(t *testing.T) {
	v := nixHookVars{NixExe: "nix", OverrideArgs: []string{"--override-input", "base", "git+file:///w/repo-base"}, FlakeDir: "/w/consumer"}
	got, attrs, err := expandNixRunTokens("{nix_run install-pre-commit-hooks}", v)
	if err != nil { t.Fatal(err) }
	if got != "nix run --override-input base 'git+file:///w/repo-base' '/w/consumer#install-pre-commit-hooks'" { t.Fatalf("got %q", got) }
	if len(attrs) != 1 || attrs[0] != "install-pre-commit-hooks" { t.Fatalf("attrs %v", attrs) }
}
func TestExpandNixRunTokens_PreservesSurroundingText(t *testing.T) {
	got, _, err := expandNixRunTokens("echo x && {nix_run y} && echo ${HOME}", nixHookVars{NixExe: "nix", FlakeDir: "/w/c"})
	if err != nil { t.Fatal(err) }
	if got != "echo x && nix run '/w/c#y' && echo ${HOME}" { t.Fatalf("got %q", got) }
}
func TestExpandNixRunTokens_NoTokenVerbatim(t *testing.T) {
	got, attrs, err := expandNixRunTokens("ls -la", nixHookVars{}); if err != nil || attrs != nil || got != "ls -la" { t.Fatalf("%q %v %v", got, attrs, err) }
}
func TestExpandNixRunTokens_MultipleTokensError(t *testing.T) {
	if _, _, err := expandNixRunTokens("{nix_run a} {nix_run b}", nixHookVars{NixExe: "nix", FlakeDir: "/w/c"}); err == nil { t.Fatal("want error") }
}
```

- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** — append to `template.go`:

```go
var nixRunTokenRe = regexp.MustCompile(`\{nix_run\s+([A-Za-z0-9._-]+)\}`)
type nixHookVars struct{ NixExe string; OverrideArgs []string; FlakeDir string }
func expandNixRunTokens(raw string, v nixHookVars) (string, []string, error) {
	locs := nixRunTokenRe.FindAllStringSubmatchIndex(raw, -1)
	if len(locs) == 0 { return raw, nil, nil }
	if len(locs) > 1 { return "", nil, fmt.Errorf("hook %q references more than one {nix_run …} token; v1 supports one", raw) }
	attr := raw[locs[0][2]:locs[0][3]]
	var b strings.Builder; b.WriteString(v.NixExe); b.WriteString(" run")
	for i := 0; i < len(v.OverrideArgs); i++ {
		if v.OverrideArgs[i] == "--override-input" && i+2 < len(v.OverrideArgs) {
			b.WriteString(" --override-input "); b.WriteString(v.OverrideArgs[i+1]); b.WriteString(" '"); b.WriteString(v.OverrideArgs[i+2]); b.WriteString("'"); i += 2; continue
		}
		b.WriteString(" "); b.WriteString(v.OverrideArgs[i])
	}
	fmt.Fprintf(&b, " '%s#%s'", v.FlakeDir, attr)
	return raw[:locs[0][0]] + b.String() + raw[locs[0][1]:], []string{attr}, nil
}
```

- [ ] **Step 4: Verify pass.** **Step 5: Commit** — `feat(pn): {nix_run <attr>} hook template expansion (pg2-5yq5)`

---

## Task 2: Lock-parameterized overrides + per-repo hook vars (warn on lock error)

**Files:** `workspace/helpers.go`; `workspace/nix_hooks.go`; `workspace/nix_hooks_test.go`. **Produces:** `overrideInputArgsForLock`, `repoNixHookVars`, `repoNixRunString`.

- [ ] **Step 1: Lock-parameterize** (mechanical; `ws.lock` at `helpers.go:56,66` → `lk`; `ws.root` at `:82` stays):

```go
func (ws *Workspace) overrideInputArgsFor(consumer string, opts overrideOpts) []string { return ws.overrideInputArgsForLock(ws.lock, consumer, opts) }
func (ws *Workspace) overrideInputArgsForLock(lk *Lock, consumer string, opts overrideOpts) []string {
	if ws == nil || lk == nil { return []string{} }
	// … existing body iterating lk.Edges …
}
```

`go build ./modules/pn/...` — callers (`nix.go:49`, `build.go:39`, `flake_check.go:40`, `apply.go:44`) unaffected.

- [ ] **Step 2: Failing test** (lock type is `LockRepoEntry`, per `lock.go:33`):

```go
// workspace/nix_hooks_test.go
package workspace
import ("context"; "os"; "path/filepath"; "strings"; "testing"; "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec")
func mustMkdir(t *testing.T, d string) { t.Helper(); if err := os.MkdirAll(d, 0o755); err != nil { t.Fatal(err) } }
func TestRepoNixRunString_InjectsConsumerOverrides(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.repo-base]\nurl=\"github:o/repo-base\"\n[repos.consumer]\nurl=\"github:o/consumer\"\n")
	for _, r := range []string{"repo-base", "consumer"} { mustMkdir(t, filepath.Join(root, r)); writeFile(t, filepath.Join(root, r, "flake.nix"), "{}") }
	lk := &Lock{Repos: map[string]LockRepoEntry{"repo-base": {FlakePath: "flake.nix", RemoteURL: "github:o/repo-base"}, "consumer": {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"}}, Edges: []LockEdge{{Consumer: "consumer", Alias: "base", Target: "repo-base"}}}
	if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil { t.Fatal(err) }
	w, err := Open(root, exec.NewFakeRunner()); if err != nil { t.Fatal(err) }
	got := w.repoNixRunString(context.Background(), "consumer", "install-pre-commit-hooks")
	if !strings.Contains(got, "--override-input base 'git+file://"+filepath.Join(root, "repo-base")+"'") { t.Errorf("no override: %q", got) }
	if !strings.HasSuffix(got, "'"+filepath.Join(root, "consumer")+"#install-pre-commit-hooks'") { t.Errorf("bad suffix: %q", got) }
}
```

- [ ] **Step 3: Verify fail. Step 4: Implement** — `workspace/nix_hooks.go`:

```go
package workspace
import ("context"; "fmt"; "os"; "path/filepath")
func (ws *Workspace) repoNixHookVars(ctx context.Context, key string) nixHookVars {
	lk, _, err := ws.effectiveLock(ctx)
	if err != nil { fmt.Fprintf(os.Stderr, "warning: hook overrides for %s: effective lock unavailable (%v); gate may build against locked inputs\n", key, err) }
	return nixHookVars{NixExe: "nix", OverrideArgs: ws.overrideInputArgsForLock(lk, key, overrideOpts{}), FlakeDir: filepath.Join(ws.root, key, filepath.Dir(ws.resolveFlakePath(key)))}
}
func (ws *Workspace) repoNixRunString(ctx context.Context, key, attr string) string { s, _, _ := expandNixRunTokens("{nix_run "+attr+"}", ws.repoNixHookVars(ctx, key)); return s }
```

- [ ] **Step 5: Verify pass. Step 6: Commit** — `feat(pn): effective-lock overrides (warn on error) + repoNixHookVars (pg2-5yq5)`

---

## Task 3: Reshape config to `{when,run}` lists + update ALL writers + delete subcommand (one green build)

**Files:** `workspace/config.go`, `workspace/init.go`, `workspace/workforest_subset.go`, `workspace/enforce_keys.go` (Task 7 — land here), `workspace/config_test.go`; **stub** `workspace/workforest.go` + `t.Skip` `workspace/workforest_install_hooks_test.go`; **stub** `cli/hooks.go` + `t.Skip` `cli/hooks_test.go`; delete `workspace/install_hooks.go`, `workspace/install_hooks_test.go`; `cli/workspace.go`, `cli/workspace_test.go`.

**This is the atomic reshape — it must leave `go build`/`go vet`/`go test ./modules/pn/...` green.** Every reader of the old `map[string]HookCommand` shape is handled here: the writers + `enforce_keys.go` are rewritten; `workforest.go` and `cli/hooks.go` are STUBBED (their real fixes need Task 4/8/9) and their tests skipped.

- [ ] **Step 1: Rewrite `sampleTOML` + failing config tests** — In `config_test.go`: convert `sampleTOML` hooks from `[hooks.update]`/`[hooks.build]` maps to `[[hooks]] when=[...] run=[...]`; rewrite `TestParseConfig_Hooks` to assert `[]RepoHook`; rename `TestParseConfig_RejectsUnknownHookCommand` → `..._RejectsUnknownEvent` (assert Task 5 behavior — may be skipped until Task 5, mark `t.Skip` with a TODO or fold its assertion into Task 5); delete `TestKnownHookCommands` and the `TestParseConfig_InstallHooks*` trio. Add:

```go
func TestConfig_ParsesEventHookLists(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[[hooks]]\nwhen=[\"post-apply\"]\nrun=[\"pb gate check\"]\n[repos.foo]\nurl=\"github:o/foo\"\n[[repos.foo.hooks]]\nwhen=[\"post-clone\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n")
	w, err := Open(root, exec.NewFakeRunner()); if err != nil { t.Fatal(err) }
	if len(w.Config().Hooks) != 1 || w.Config().Hooks[0].When[0] != "post-apply" || w.Config().Hooks[0].Run[0] != "pb gate check" { t.Fatalf("ws %+v", w.Config().Hooks) }
	if fh := w.Config().Repos["foo"].Hooks; len(fh) != 1 || fh[0].Run[0] != "{nix_run install-pre-commit-hooks}" { t.Fatalf("repo %+v", fh) }
}
```

- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Reshape struct** — in `config.go`: replace `type HookCommand struct{Pre,Post []string}` with `type RepoHook struct { When []string \`toml:"when"\`; Run []string \`toml:"run"\` }`; `WorkspaceConfig.Hooks`→`[]RepoHook \`toml:"hooks,omitempty"\``; add `RepoConfig.Hooks []RepoHook \`toml:"hooks,omitempty"\``; delete `RepoConfig.InstallHooks`, `knownHookCommands`, `IsKnownHookCommand`, and the ParseConfig `knownHookCommands`validation block (replaced in Task 5). If`ParseConfig`initialized`Hooks` as a non-nil map, drop that.
- [ ] **Step 4: Update the 3 ordered-TOML writers + enforce (Task 7)** — `init.go:144`, `init.go:184`, `workforest_subset.go:87`: change `Hooks map[string]HookCommand` → `Hooks []RepoHook` in each `orderedConfig` (assignment unchanged). `workforest_subset.go:16-20` `filterConfig` `Hooks: cfg.Hooks` unchanged (slice-header copy is benign — never mutated). **Do Task 7 here**: rewrite `enforce_keys.go`'s `orderedConfig.Hooks` → `[]RepoHook` and the `cfg.Hooks["apply"]` block (`:79-85`) → ensure-present (Task 7 code) — it shares this struct and must compile in the batch.
- [ ] **Step 4b: Stub the two late-fixable sites (keeps the batch green).** `workforest.go` (`:126-127`, `:306-307`): replace the `InstallHooks` loop + `InstallHooksInDir(...)` with a no-op `// TODO(pg2-5yq5): rewired in Task 8`; add `t.Skip("rewired in Task 8 (pg2-5yq5)")` to the 3 `workforest_install_hooks_test.go` cases. `cli/hooks.go`: `runWithHooks` body → `return fn()` `// TODO(pg2-5yq5): event dispatch in Task 9`; add `t.Skip("rewired in Task 9 (pg2-5yq5)")` to the 4 `cli/hooks_test.go` cases.
- [ ] **Step 5: Delete subcommand + dead code** — `git rm workspace/install_hooks.go workspace/install_hooks_test.go`; delete the `install-hooks` cobra command + `AddCommand` in `cli/workspace.go` (~236–260) + its cases in `cli/workspace_test.go` (~:130, ~:1061); fix the stale `IsKnownHookCommand` comment at `cli/workspace.go:566`. `rg 'InstallHooks|install-hooks|installHooksRunArgs|InstallHooksInDir|sync-hooks|HookCommand|knownHookCommands|IsKnownHookCommand' modules/pn` → every hit resolved or stubbed (Step 4b covers `workforest.go`/`cli/hooks.go`).
- [ ] **Step 6:** (Task 7 enforce landed in Step 4; Task 5 validation is a separate additive commit after this batch.)
- [ ] **Step 7: Green build** — `go build ./modules/pn/... && go vet ./modules/pn/...` → clean. Run `go test ./modules/pn/internal/workspace/ -run 'TestConfig_ParsesEventHookLists|TestParseConfig_Hooks'` → PASS.
- [ ] **Step 8: Commit** — `refactor(pn): hooks as {when,run} event lists; update writers; drop install-hooks (pg2-5yq5)`

---

## Task 4: Event dispatch — `RunEventHooks` (workspace + per-repo)

**Files:** `workspace/nix_hooks.go`; `workspace/nix_hooks_test.go`. **Produces:** `eventName`, `processedReposFor`, `ProcessedReposFor` (exported), `RunEventHooks(ctx, phase, cmd, processed, out)`.

- [ ] **Step 1: Failing test**

```go
func TestRunEventHooks_RepoScopedFiresForProcessedRepoOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n[repos.b]\nurl=\"github:o/b\"\n")
	for _, r := range []string{"a", "b"} { mustMkdir(t, filepath.Join(root, r)); writeFile(t, filepath.Join(root, r, "flake.nix"), "{}") }
	lk := &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}, "b": {FlakePath: "flake.nix", RemoteURL: "github:o/b"}}}
	if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil { t.Fatal(err) }
	wantCmd := "nix run '" + filepath.Join(root, "a") + "#install-pre-commit-hooks'"
	f := exec.NewFakeRunner(); f.AddResponse("sh", []string{"-c", wantCmd}, exec.Result{}, nil)
	w, err := Open(root, f); if err != nil { t.Fatal(err) }
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "rebase", []string{"a", "b"}, &bytes.Buffer{}); err != nil { t.Fatal(err) }
	var sh []exec.Call
	for _, c := range f.Calls() { if c.Name == "sh" { sh = append(sh, c) } }
	if len(sh) != 1 || sh[0].Opts.Dir != filepath.Join(root, "a") { t.Fatalf("calls %+v", sh) }
}
```

- [ ] **Step 2: Verify fail. Step 3: Implement** (imports `io`, `slices`, `strings`, `exec`):

```go
func eventName(phase HookPhase, cmd string) string { if phase == HookPhasePre { return "pre-" + cmd }; return "post-" + cmd }
func (ws *Workspace) processedReposFor(ctx context.Context, cmd string) []string {
	if _, ok := repoIteratingCommands[cmd]; ok { return ws.topoAlpha(ctx) }
	switch cmd {
	case "upgrade": return ws.topoAlpha(ctx)
	case "build", "apply": if t, err := ws.config.TerminalRepo(); err == nil { return []string{t} }
	}
	return nil
}
func (ws *Workspace) ProcessedReposFor(ctx context.Context, cmd string) []string { return ws.processedReposFor(ctx, cmd) }

func (ws *Workspace) RunEventHooks(ctx context.Context, phase HookPhase, cmd string, processed []string, out io.Writer) error {
	ev := eventName(phase, cmd)
	// Workspace-scoped: once at root (no {nix_run}; enforced in Task 5).
	for _, h := range ws.config.Hooks {
		if slices.Contains(h.When, ev) {
			if err := RunHooks(ctx, ws.runner, h.Run, ws.root, phase); err != nil { return err }
		}
	}
	// Per-repo: in each processed repo.
	for _, key := range processed {
		hooks := ws.config.Repos[key].Hooks
		if len(hooks) == 0 { continue }
		vars := ws.repoNixHookVars(ctx, key) // once per repo
		dir := filepath.Join(ws.root, key)
		for _, h := range hooks {
			if !slices.Contains(h.When, ev) { continue }
			for _, raw := range h.Run {
				cmdStr, _, err := expandNixRunTokens(raw, vars)
				if err == nil {
					var resolved string
					if resolved, err = rewriteFirstToken(cmdStr, dir); err == nil {
						var res exec.Result
						if res, err = ws.runner.Run(ctx, "sh", []string{"-c", resolved}, exec.RunOptions{Dir: dir, Stdout: out, Stderr: out}); err != nil && phase == HookPhasePost { _, _ = os.Stderr.Write(res.Stderr) }
					}
				}
				if err != nil {
					if phase == HookPhasePre { return fmt.Errorf("pre-hook %q in %s: %w", raw, key, err) }
					fmt.Fprintf(os.Stderr, "warning: post-hook %q in %s: %v\n", raw, key, err)
				}
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Verify pass. Step 5: Commit** — `feat(pn): RunEventHooks (workspace + per-repo event dispatch) (pg2-5yq5)`

---

## Task 5: Validation — events, {nix_run} placement, single-token

**Files:** `workspace/config.go` (ParseConfig validation, `:229`), `workspace/nix_hooks.go` (`splitEvent`); `workspace/config_test.go`.

- [ ] **Step 1: Failing tests** — `TestConfig_RejectsUnknownEvent`, `TestConfig_RejectsNixRunInWorkspaceHook`, `TestConfig_RejectsMultipleTokens`, `TestConfig_AllowsRepoNixRun` (per prior revision; workspace hook fixture uses `[[hooks]] when=['post-rebase'] run=['{nix_run x}']` → expect `nix_run` error).
- [ ] **Step 2: Verify fail. Step 3: Implement** — `splitEvent` in `nix_hooks.go`:

```go
func splitEvent(ev string) (HookPhase, string, bool) {
	if s, ok := strings.CutPrefix(ev, "pre-"); ok { if _, k := hookableCommands[s]; k { return HookPhasePre, s, true } }
	if s, ok := strings.CutPrefix(ev, "post-"); ok { if _, k := hookableCommands[s]; k { return HookPhasePost, s, true } }
	return 0, "", false
}
```

In `ParseConfig`: a helper `validateHook(h RepoHook, repoScoped bool) error` — every `when` event `splitEvent`-ok (else error naming it); each `run` entry: `>1` token → error; `{nix_run}` present && `!repoScoped` → error "valid only in per-repo hooks". Call for each `c.Hooks` (false) and each `c.Repos[*].Hooks` (true).

- [ ] **Step 4: Verify pass. Step 5: Commit** — `feat(pn): validate event hooks (events, {nix_run} placement, single-token) (pg2-5yq5)`

---

## Task 6: `doctor`/`lock` verify `{nix_run <attr>}` outputs + never-fire

**Files:** `doctor.go` (findings `SevWarning`, `registerChecks` `:225`), `derive_lock.go`; tests.

- [ ] **Step 1: Failing test — never-fire (pure, terminal-guarded)** — repo `other` with only `post-build`, terminal `term` ⇒ warn "never fires". Skip the finding when `workspace.terminal == ""`.
- [ ] **Step 2: Verify fail. Step 3: Implement** — (a) never-fire: for each repo hook, if every event's command is repo-iterating→fires; `build`/`apply`/`upgrade`→fires only if repo==terminal (skip check if terminal unset); else→never; if none fire → `SevWarning`. (b) output existence: probe `nix eval <repoDir>#<attr> --apply "_: true"` (swallow error ⇒ absent, per `edges.go` convention), warn if absent; route same probe into `deriveLock`/`lock`. Advisory only.
- [ ] **Step 4: Verify pass. Step 5: Commit** — `feat(pn): doctor/lock verify {nix_run} outputs + never-fire hooks (pg2-5yq5)`

---

## Task 7: Redesign `EnforceKeys` for the list shape (ADR 0017)

**Files:** `workspace/enforce_keys.go`, `workspace/enforce_keys_test.go`, `cmd/pn-workspace-toml-enforce/main_test.go`, `docs/adr/0017-*.md`. **(Land inside Task 3's batch — see Task 3 note.)**

- [ ] **Step 1: Update tests** — In `enforce_keys_test.go` (~9–10 functions embed `[hooks.apply]` / assert `.Post` — incl. the `realWorldTOML` const feeding 3, plus inline fixtures at `:131`, `:158`, `:262`) + `main_test.go` (fixtures `liveTOML`/`withCustom` across 4 tests): change fixtures/assertions from `[hooks.apply] post=[…]` to `[[hooks]] when=['post-apply'] run=[…]`; assert **ensure-present** semantics (a `post-apply` `[[hooks]]` entry whose `run` contains the enforced command; absent ⇒ appended; present ⇒ idempotent no-op; extra `run` commands NOT clobbered). (Reviewer confirmed no existing test requires exact-replace, so the byte-exact no-op cases still pass.)
- [ ] **Step 2: Verify fail.**
- [ ] **Step 3: Implement** — replace the map block (`enforce_keys.go:79-85`) and `orderedConfig.Hooks` (`:118`):

```go
// orderedConfig.Hooks:
Hooks []RepoHook `toml:"hooks,omitempty"`
// enforcement (ensure-present, idempotent, append-if-absent):
found := false
for i := range cfg.Hooks {
	if slices.Contains(cfg.Hooks[i].When, "post-apply") {
		if !slices.Contains(cfg.Hooks[i].Run, applyPost) {
			cfg.Hooks[i].Run = append(cfg.Hooks[i].Run, applyPost); changed = true
		}
		found = true; break
	}
}
if !found { cfg.Hooks = append(cfg.Hooks, RepoHook{When: []string{"post-apply"}, Run: []string{applyPost}}); changed = true }
```

The `EnforceKeys(path, id, applyPost, buildCommand, applyCommand)` signature and the binary's flags are **unchanged**, so the nix `home.activation` wiring needs no edit.

- [ ] **Step 4: Amend ADR 0017** — document the shape change and the ensure-present (was enforce-exact) semantics; note it no longer removes user-added `post-apply` `run` entries.
- [ ] **Step 5: Verify pass** — `go test ./modules/pn/... -run 'EnforceKeys|Enforce'` → PASS.
- [ ] **Step 6: Commit** — `refactor(pn): EnforceKeys enforces [[hooks]] post-apply (ensure-present); amend ADR 0017 (pg2-5yq5)`

---

## Task 8: Workforest fires `post-clone` on new worktrees (set-rooted)

**Files:** `workspace/workforest.go`, `workspace/workforest_install_hooks_test.go`. (Both install sites `:127`/`:307` use canonical `w` before `writeSetMembership`; move after, open set-rooted Workspace, fire `post-clone`.)

- [ ] **Step 1: Update test** — per new-worktree repo with a `post-clone` hook, expect `sh -c` with `Opts.Dir==filepath.Join(setDir,repo)` ending `<setDir>/<repo>#install-pre-commit-hooks'`.
- [ ] **Step 2: Verify fail. Step 3: Add path** — after `writeSetMembership(...)`: `if setWs, err := Open(setDir, w.runner); err == nil { _ = setWs.RunEventHooks(ctx, HookPhasePost, "clone", names, out) } else { fmt.Fprintf(errOut, "warning: workforest hooks: open set: %v\n", err) }`. **Step 4: add-repo path (`:305-307`)** — after `rewriteSetMembership`, open set Workspace, `RunEventHooks(ctx, HookPhasePost, "clone", []string{repo}, out)`.
- [ ] **Step 5: Verify pass. Step 6: Commit** — `refactor(pn): workforest fires post-clone on new worktrees (set-rooted) (pg2-5yq5)`

---

## Task 9: Wire `runWithHooks` to events; update cli tests + fuzz seed

**Files:** `cli/hooks.go`, `cli/hooks_test.go`, `fuzz_test.go`.

- [ ] **Step 1: Rewrite `runWithHooks`**

```go
func runWithHooks(ctx context.Context, w *workspace.Workspace, name string, fn func() error) error {
	processed := w.ProcessedReposFor(ctx, name)
	if err := w.RunEventHooks(ctx, workspace.HookPhasePre, name, processed, os.Stdout); err != nil { return err }
	fnErr := fn()
	_ = w.RunEventHooks(ctx, workspace.HookPhasePost, name, processed, os.Stdout)
	return fnErr
}
```

- [ ] **Step 2: Update `cli/hooks_test.go` (4 tests)** — convert `[hooks.update]` map fixtures to `[[hooks]] when=['pre-update'/'post-update'] run=[…]`; assert dispatch via `RunEventHooks` (workspace-scoped entries run at root). Update `fuzz_test.go:12` seed to `[[hooks]]` form.
- [ ] **Step 3: Full suite** — `go test ./modules/pn/...` → PASS (pre-existing `terminal_flake`/pg2-2cdt failures excepted; confirm count).
- [ ] **Step 4: Commit** — `refactor(pn): route runWithHooks through RunEventHooks; update cli hook tests + fuzz seed (pg2-5yq5)`

---

## Task 10: ADR 0002 amend + adopt in set toml + integration verify

**Files:** `docs/adr/0002-*.md` (+ `docs/adr/index.md`); `.workforests/pg2-5yq5-nix-hooks/pn-workspace.toml`.

- [ ] **Step 1: ADR 0002** — document the `[[hooks]]`/`[[repos.<r>.hooks]]` `{when,run}` schema, event vocabulary, firing rule, `{nix_run}` semantics + effective-lock overrides, and the `install-hooks` removal. Commit.
- [ ] **Step 2: Adopt** —

```toml
[[hooks]]
when = ['post-apply']
run  = ['pb gate check']

[[repos.phillipg-nix-repo-base.hooks]]
when = ['post-clone', 'post-rebase', 'post-update', 'post-upgrade']
run  = ['{nix_run install-pre-commit-hooks}']
# … repeat under each of the other five repos …
```

- [ ] **Step 3: Build + validate** — `nix build .#pn && ./result/bin/pn workspace discover` (rc 0).
- [ ] **Step 4: End-to-end** — from set root (`unset PN_WORKSPACE_ROOT`): `pn workspace rebase main`; each repo's `.pre-commit-config.yaml` → treefmt.toml has `[formatter.gofumpt]`; a consumer's emitted command shows `--override-input`. `pn workspace apply` (USER) exercises `EnforceKeys` against the new shape — verify it's idempotent (no spurious rewrite).
- [ ] **Step 5: Gate** — `rm -f …/result; pn workspace flake-check; pn workspace doctor`. Canonical `pn-workspace.toml` untouched until landed + new pn applied.

---

## Self-Review

**Coverage:** uniform `{when,run}` (ws + per-repo) + all writers/tests → T3; enforce redesign + ADR 0017 → T7; `{nix_run}`+overrides (warn) → T1,2; dispatch+firing+upgrade → T4; validation → T5; doctor/lock+never-fire → T6; workforest → T8; runWithHooks+cli tests+fuzz → T9; ADR 0002+adopt+verify → T10. ✅

**Reviewer fixes folded:** C1 EnforceKeys/ADR-0017 → T7 (ensure-present); C2 writers → T3 (init ×2, workforest_subset, +enforce in T7); C3 `LockRepoEntry` → T2,4; C4 sampleTOML+config tests → T3; C5 cli/hooks_test+fuzz → T9; C6 upgrade → T4 (all-repos) + `post-upgrade` in T10; P1 warn on lock error → T2; P2 never-fire terminal-guard → T6; P4 rewriteFirstToken parity → T4; P5 vars once/repo → T4. cwd load-bearing, FakeRunner exact-match — Global + T4,8.

**Type consistency:** `RepoHook{When,Run}` (T3) → T4,5,7,8,9,10. `repoNixHookVars`/`repoNixRunString` (T2) → T4,8. `expandNixRunTokens`/`nixHookVars`/`nixRunTokenRe` (T1) → T2,4,5. `RunEventHooks(ctx,phase,cmd,processed,out)` (T4) → T8,9. `ProcessedReposFor` (T4) → T9. `overrideInputArgsForLock` (T2). `splitEvent`/`hookableCommands`/`repoIteratingCommands` (T4,5) → T6.

**Ordering caveat (critical, reviewer-fixed):** the reshape breaks `workforest.go` + `cli/hooks.go` at compile time, and their real fixes (Tasks 8/9) depend on Task 4 — so **Task 3 STUBS both** (+ `t.Skip`s their tests) to stay green, and folds in the enforce rewrite (Task 7). Sequence: **1, 2** (additive, green) → **3** (struct + writers + enforce + stubs + subcommand/`install_hooks.go` removal → green) → **4** → **5** → **6** → **8** (unstub workforest) → **9** (unstub `runWithHooks` + fuzz seed) → **10**. The header "Build ordering (STUB strategy)" note governs.

**Execution-time reconciliations:** exact `Lock`/`LockRepoEntry`/`LockEdge` fields (T2 — confirmed lock.go:33/44/52/152); doctor `Finding` shape + harness (T6); nix output-existence incantation (T6); exact `enforce_keys.go`/`main_test.go` fixtures (T7); exact subcommand/test lines (T3).
