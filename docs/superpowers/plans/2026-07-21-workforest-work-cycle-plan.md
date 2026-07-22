# Workforest Work-Cycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Each task subagent MUST be given the full v5 design spec (the DESIGN field of bead `pg2-xs5cj`) plus this plan; the design carries rationale this plan does not restate.

> **Revision note:** v2 — folds an independent adversarial critique. Fixes: guarded package overlay in agent-support (C1); non-vacuous `set -e` bats harness (H1); `PN_WORKSPACE_ROOT`-clearing in `pnwf resolve` (H2); reworked live-run/land/relock/apply sequence (H3, M4); robust `canonicalRoot()` (M1); enable-default eval test (M2); mock↔real `pn` smoke (M3); fresh-worktree pre-commit install (M5); full `PN_WORKSPACE_ROOT` reconciliation surface (M6); test-file naming (L2).

**Goal:** Ship the reusable workforest work-cycle — four composable stage-skills (`fork/validate/land/cleanup-workforest`), the `/pn-workspace-sync` command, a nix-built bash helper `pnwf`, and a field extension to `pn workspace info --json` — so cross-repo workspace operations run in an isolated coordinated workforest and land deliberately.

**Architecture:** A Pipeline of four prose stage-skills with an inline WORK gap; all deterministic logic factored into the `pnwf` helper (thin `pn`+`git` orchestrator); command consumers are Facades; `land-workforest` is a Client of the existing `integrate-branch` Strategy dispatcher. This is a **two-repo change**: `phillipg-nix-repo-base` (plugin skills/command, `pn info --json` field, the `pnwf` package) and `phillipgreenii-nix-agent-support` (the `pnwf` home module, because its `enable`-default machinery lives only there).

**Tech Stack:** Go (the `pn` CLI, `modules/pn`), Bash via the `mkBashBuilders` framework (`mkBashScript` + `mkBashLibrary`, bats + shellcheck), Nix flakes (packages, overlays, checks, home-manager modules), Claude Code plugin skills/commands (markdown prose).

## Global Constraints

Every task's requirements implicitly include this section. Values copied verbatim from the v5 design + verified exploration + the critique.

- **`set -euo pipefail` guard (MUST — review-critical):** the builders inject `set -euo pipefail`. Every exit-code-as-boolean git probe MUST be guarded — capture rc via `rc=0; <cmd> || rc=$?` (never bare), then `case $rc in …`. Applies to `git merge-base --is-ancestor` (0 ancestor / 1 not-ancestor / 128 absent-ref) and `git rev-parse --verify --quiet` (0 present / non-0 absent). bats MUST prove non-abort **inside a `set -e` shell**: use `run bash -euo pipefail -c "source '$LIB_PATH'; <probe> …"` and assert `status -eq 0` (mirroring `lib/bash-builders-tests/sample-cmd-with-lib/tests/…:20`). Merely sourcing the lib into bats' own non-`-e` shell proves nothing (H1).
- **`pnwf` is a thin layer over `pn` + `git`:** reads `pn workspace info --json`, the set's own `pn-workspace.lock.json`, `pn workspace discover` TSV, and shells `git`. MUST NOT parse human-only `tree`/`status`; MUST NOT re-derive the workspace graph.
- **`pnwf resolve` discovery MUST clear an inherited `PN_WORKSPACE_ROOT` (H2 — crux):** run its `pn workspace info --json` as `env -u PN_WORKSPACE_ROOT pn workspace info --json` so the cwd upward-walk governs (a stale exported value from a prior tool call otherwise redirects `pn` to canonical, silently defeating every stage's location guard). `resolve` reads the NEW fields (`workforests_dir`, `in_workforest`, `canonical_root`) via `jq` and prints the explicit `PN_WORKSPACE_ROOT=<setdir>` value the stages must use — never a bash re-parse of `pn-workspace.toml`, never a hardcoded `.workforests`.
- **`integrate-branch-support` is invoked BARE** (correction to the design, verified): **no `--json` flag**; emits one JSON object unconditionally. Pipe to `jq`. Fields: `.primary_branch` (string), `.strategy` (string|null), `.canonical.branch` (string), `.canonical.dirty` (bool), `.remote`, `.open_pr`, `.mr_bead`. `pnwf` reads `primary_branch`/`strategy` from this tool for parity with the land loop.
- **`land-plan` worktree presence** via `[ -e <setdir>/<member> ]` — NOT `git worktree list`.
- **`cleanup` landed-test** via `git merge-base --is-ancestor <branch> <primary>` — NEVER `git branch -d` as the test.
- **Subset-aware enumeration (MUST):** `repos`/`stage`/`land-plan` enumerate members from the **set's own** `pn-workspace.lock.json` topo `order`, worktree paths `<setdir>/<member>`.
- **No per-repo subagent fan-out (MUST NOT).**
- **Bash source-file rules:** NO shebang, NO `set` flags, MUST start with `# shellcheck shell=bash`. Libraries non-executable, functions-only. NEVER write/test a `--version` handler. NEVER pass `excludeShellChecks`; inline `# shellcheck disable=SCxxxx  # reason`.
- **Version stamping:** digest-based, repo-rev-independent — do NOT thread a repo gitHash.
- **`pnwf` package location:** built in repo-base `modules/pnwf`, exposed as a `packages.<system>` output (mirror `determine-ul-lib-dir` at repo-base `flake.nix:153`). repo-base publishes for **x86_64-linux + aarch64-darwin only** (`flake.nix:63-65`).
- **agent-support package wiring (C1 — MUST be guarded):** agent-support builds **4 systems** (`flake.nix:205-210`). Add `pnwf` to agent-support's `overlay` sourced from `inputs.phillipgreenii-nix-base.packages.<system>.pnwf` with the **same `or {}` / `? pnwf` guard** the marketplace-drv threading uses (`flake.nix:1648-1656`), so `pkgs.pnwf` exists on the 2 published systems and is a graceful no-op on the other 2. Then the home module uses `package = lib.mkPackageOption pkgs "pnwf" { }` (a true mirror of the precedent) and needs no `inputs` arg. An unguarded `inputs…packages.${system}.pnwf` throws "attribute 'pnwf' missing" on `aarch64-linux`/`x86_64-darwin` and breaks `nix flake check`.
- **`pnwf` home module `enable` default:** `config.phillipgreenii.programs.claude-code.enable && pluginEnabled "pn-workspace-rules"` — mirror `agent-support/home/programs/integrate-branch-support/default.nix`. No per-machine `enable`. Ship the enable-default `evalModules` test (M2).
- **Fresh-worktree pre-commit (M5):** `.pre-commit-config.yaml` is a gitignored store symlink absent in fresh worktrees. Before the first commit in each worktree, install it — `ln -s "$(readlink <canonical-repo>/.pre-commit-config.yaml)" .pre-commit-config.yaml` (per the `worktree-precommit-config-missing` learning) — do NOT use `PREK_ALLOW_NO_CONFIG=1`.
- **Skill prose:** RFC 2119 language, mermaid diagrams, a `RUN FROM:` banner opening each stage SKILL, disambiguating `description` (`land-workforest` = whole coordinated set; single branch/repo = `integrate-branch` directly).
- **Tracking:** `bd` child beads, not markdown/TaskCreate.
- **Work location:** a full-workspace coordinated workforest on `wf/pg2-xs5cj-workforest-work-cycle` (all 6 repos, so the Tier-3 `pn workspace build` gate runs inside the set). NOT on any canonical `main` (R-4).

## Completion Gate

- repo-base Go/bash changes → **Tier 2** (`pn workspace flake-check`) minimum.
- agent-support home module touches the assembled system → **Tier 3** (`pn workspace build`), then `pn workspace doctor`.
- Per-task local gates: `bats tests/`, `go test ./...`, `nix build .#<pkg>` / `nix flake check`.
- Explicit `pn workspace pre-commit-check` (all-files) in T13 (acceptance criterion).

---

## File Structure

### `phillipg-nix-repo-base`

```text
modules/pn/internal/workspace/info.go             # MODIFY: +3 JSON fields, +canonicalRoot() helper
modules/pn/internal/workspace/info_test.go         # MODIFY: set/non-set + multi-segment/absolute cases
modules/pnwf/                                       # NEW (mirror modules/ul + fixture sample-cmd-with-lib)
├── scripts.nix
├── lib/{default.nix, pnwf-lib.bash, tests/test-pnwf-lib.bats}
└── pnwf/{default.nix, pnwf.sh, pnwf.md, completions/pnwf.bash, completions/_pnwf, tests/test-pnwf.bats}
flake.nix                                           # MODIFY: import pnwf scripts.nix; packages.pnwf; checks
pn-workspace-rules/commands/pn-workspace-sync.md    # NEW
pn-workspace-rules/skills/{fork,validate,land,cleanup}-workforest/SKILL.md   # NEW x4
pn-workspace-rules/skills/pn-workspace-rules/SKILL.md                        # MODIFY
docs/superpowers/plans/2026-07-21-workforest-work-cycle-plan.md              # NEW: this plan
```

### `phillipgreenii-nix-agent-support`

```text
home/programs/pnwf/default.nix   # NEW (mirror home/programs/integrate-branch-support/default.nix)
home/default.nix                  # MODIFY: add ./programs/pnwf to imports
flake.nix                         # MODIFY: guarded pnwf overlay entry (C1) + enable-default evalModules test (M2)
```

---

## Task 1: `pn workspace info --json` field extension (repo-base, Go)

**Files:** Modify `modules/pn/internal/workspace/info.go`; Test `modules/pn/internal/workspace/info_test.go`.

**Interfaces — Produces:** three JSON fields on `WorkspaceInfo`: `workforests_dir` (string), `in_workforest` (bool), `canonical_root` (string). Consumed by `pnwf resolve` (T3).

- [ ] **Step 1 — Failing tests.** Add: `TestInfo_WorkforestFields_Canonical` (root with `pn-workspace.toml` → `InWorkforest==false`, `CanonicalRoot==root`, `WorkforestsDir==".workforests"`); `TestInfo_WorkforestFields_InSet` (mirror `update_worktree_test.go:762-779`; set at `<base>/.workforests/feature-x` → `InWorkforest==true`, `CanonicalRoot==base`); `TestInfo_WorkforestFields_MultiSegmentAndAbsolute` (assert the documented behavior chosen in Step 3 for a multi-segment relative and an absolute `workforests_dir` — M1). Use `writeFile`, `Open`, `exec.NewFakeRunner()`.
- [ ] **Step 2 — Verify fail.** `cd modules/pn && go test ./internal/workspace/ -run TestInfo_WorkforestFields -v` → FAIL.
- [ ] **Step 3 — Implement.** Extend the struct (fields grouped after `Terminal`, before `Repos`; field order is cosmetic only — L1):

```go
type WorkspaceInfo struct {
	Wsid           string     `json:"wsid"`
	Root           string     `json:"root"`
	Terminal       string     `json:"terminal"`
	WorkforestsDir string     `json:"workforests_dir"`
	InWorkforest   bool       `json:"in_workforest"`
	CanonicalRoot  string     `json:"canonical_root"`
	Repos          []RepoInfo `json:"repos"`
}
```

Add a helper robust to the `WorkforestsDir()` value shapes (M1) — single/multi-segment **relative** stripped correctly; **absolute** `workforests_dir` returns `""` (canonical is not derivable; documented precondition), consistent with `inWorkforest()` still being true:

```go
// canonicalRoot returns the canonical workspace root. When rooted inside a set
// (<canonical>/<workforests_dir>/<branch>), strip <branch> then the (possibly
// multi-segment, relative) <workforests_dir>. If workforests_dir is absolute,
// the set lives outside any canonical tree, so canonical root is undefined ("").
func (ws *Workspace) canonicalRoot() string {
	if !ws.inWorkforest() {
		return ws.root
	}
	wf := ws.config.WorkforestsDirName()
	if filepath.IsAbs(wf) {
		return ""
	}
	afterBranch := filepath.Dir(ws.root)                 // strip <branch>
	trimmed := strings.TrimSuffix(afterBranch, string(filepath.Separator)+filepath.Clean(wf))
	if trimmed == afterBranch { // suffix didn't match (unexpected layout) — fall back to one level up
		return filepath.Dir(afterBranch)
	}
	return trimmed
}
```

Populate in `Info()`: `WorkforestsDir: ws.config.WorkforestsDirName()`, `InWorkforest: ws.inWorkforest()`, `CanonicalRoot: ws.canonicalRoot()`. Add `strings` to imports.

- [ ] **Step 4 — Verify pass.** `go test ./internal/workspace/ -run TestInfo -v` → PASS.
- [ ] **Step 5 — Gate.** `go test ./...` in `modules/pn` → PASS; `nix build .#pn` → builds.
- [ ] **Step 6 — Commit.** `feat(pn): add workforests_dir/in_workforest/canonical_root to info --json` (no `Refs:` — non-ZR repo).

---

## Task 2: `pnwf-lib` primitives (repo-base, bash — review-critical core)

**Files:** Create `modules/pnwf/lib/{default.nix, pnwf-lib.bash, tests/test-pnwf-lib.bats}`, `modules/pnwf/scripts.nix` (skeleton).

**Interfaces — Produces** guarded bash functions: `pnwf_branch_exists`, `pnwf_is_ancestor_of_primary` (prints `landed|not-landed|absent`), `pnwf_worktree_present`, `pnwf_working_tree_dirty`, `pnwf_ahead_of_primary`, `pnwf_canonical_on_primary_and_clean`, `pnwf_resolve_primary_branch` (bare `integrate-branch-support | jq -r .primary_branch`), `pnwf_strategy`, `pnwf_topo_order` (`jq -r '.order[]'` from set lock). Full contracts as in v1.

- [ ] **Step 1 — Failing library tests** in `test-pnwf-lib.bats` (correct name — L2). Set up with a **real** temp git repo (`command git`) + a mock `integrate-branch-support` on PATH. **Non-abort cases MUST use the `set -e` harness (H1):**

```bash
@test "pnwf_is_ancestor_of_primary: absent ref (exit 128) does not abort" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' does-not-exist main"
  [ "$status" -eq 0 ]
  [ "$output" = "absent" ]
}
@test "pnwf_branch_exists: missing branch (non-zero) does not abort caller" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_branch_exists '$REPO' nope; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}
```

Plus: `landed`/`not-landed` classification; `pnwf_resolve_primary_branch` parity (mock emitting `{"primary_branch":"trunk"}` → `trunk`; default → `main`); `pnwf_worktree_present`; `pnwf_topo_order` from a fixture lock.

- [ ] **Step 2 — Verify fail.** `cd modules/pnwf/lib && bats tests/` → FAIL.
- [ ] **Step 3 — Implement `pnwf-lib.bash`** (`# shellcheck shell=bash`, functions-only). Guard pattern:

```bash
# shellcheck shell=bash
# Prints: landed | not-landed | absent  (never aborts under set -e)
pnwf_is_ancestor_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0
  git -C "$repo_dir" merge-base --is-ancestor "$branch" "$primary" || rc=$?
  case "$rc" in
    0) echo "landed" ;; 1) echo "not-landed" ;; 128) echo "absent" ;;
    *) echo "error" >&2; return "$rc" ;;
  esac
}
pnwf_branch_exists() {
  local repo_dir="$1" branch="$2" rc=0
  git -C "$repo_dir" rev-parse --verify --quiet "refs/heads/$branch" >/dev/null || rc=$?
  [ "$rc" -eq 0 ]
}
```

Implement remaining primitives with the same guarded shape.

- [ ] **Step 4 — Verify pass.** `bats tests/` → PASS.
- [ ] **Step 5 — `lib/default.nix`** (`mkBashLibrary { name="pnwf-lib"; src=./.; description="…"; testDeps=[pkgs.git pkgs.jq]; }`) + a minimal `scripts.nix` exposing the lib `.check`; wire the check into repo-base `flake.nix` (mirror `flake.nix:599`). `nix build .#checks.<system>.test-pnwf-lib` → green.
- [ ] **Step 6 — Commit.** `feat(pnwf): add pnwf-lib guarded git/pn primitives with set -e bats coverage`.

---

## Task 3: `pnwf` command skeleton + `resolve`/`repos`/`stage` (repo-base, bash)

**Files:** Create `modules/pnwf/pnwf/{default.nix, pnwf.sh, pnwf.md, completions/pnwf.bash, completions/_pnwf, tests/test-pnwf.bats}`; **edit** (not re-add — L3) `modules/pnwf/scripts.nix` + repo-base `flake.nix`.

**Interfaces:** Consumes `pnwf-lib` (T2), the new `info --json` fields (T1). Produces `pnwf resolve` (JSON: `canonical_root`, `in_workforest`, `set_dir`, `pn_workspace_root`), exits non-zero on guard violation; `pnwf repos [--set]`; `pnwf stage [--set]`.

- [ ] **Step 1 — Failing CLI tests** in `test-pnwf.bats` (run assembled `SCRIPT_UNDER_TEST`; mock `pn`). Assert: `resolve` on canned canonical info → `in_workforest=false`; on canned set info → `in_workforest=true` + correct `pn_workspace_root`; **`resolve` returns SET info when cwd is in the set even with `PN_WORKSPACE_ROOT` exported to canonical (H2)** — verify the mock `pn` is invoked with `PN_WORKSPACE_ROOT` unset; `--help` exits 0; unknown subcommand → non-zero + stderr; `repos --set` reads a fixture set lock in topo order (subset lock with 2 repos → only 2). **Add a key-parity assertion (M3): the canned mock JSON keys equal the `WorkspaceInfo` json tags** (guard against mock drift). Do NOT test `--version`.
- [ ] **Step 2 — Verify fail.** `cd modules/pnwf/pnwf && bats tests/` → FAIL.
- [ ] **Step 3 — Implement `pnwf.sh`** (`# shellcheck shell=bash`): `show_help()`; `while/case` arg parser; dispatch (`resolve|repos|stage|fork-preflight|land-plan|cleanup|status|sync-fetch`); `die()`. `resolve` runs `env -u PN_WORKSPACE_ROOT pn workspace info --json | jq …` (H2) and prints the explicit `pn_workspace_root=<setdir>`. Push logic-heavy bodies into `pnwf-lib.bash`.
- [ ] **Step 4 — Verify pass.** `bats tests/` (both) → PASS.
- [ ] **Step 5 — Artifacts + wiring.** `pnwf.md`, both completions (`mapfile -t COMPREPLY`), `default.nix` (`mkBashScript { name="pnwf"; src=./.; description="…"; public=true; libraries=[pnwf-lib]; runtimeDeps=[pkgs.git pkgs.jq]; testDeps=[pkgs.git pkgs.jq]; }`). Finish `scripts.nix` (mirror `modules/ul/scripts.nix`). In repo-base `flake.nix`: import `modules/pnwf/scripts.nix` (mirror `:116-119`), `pnwf = pnwfScripts.pnwf.script;` (mirror `:153`), merge `pnwfScripts.checks` (mirror `:599`).
- [ ] **Step 6 — Nix gate.** `nix build .#pnwf` → builds; `pnwf --help` works; `nix flake check` → green.
- [ ] **Step 7 — Commit.** `feat(pnwf): add pnwf CLI with resolve/repos/stage`.

---

## Task 4: `pnwf` `fork-preflight`/`land-plan`/`cleanup`/`status` (repo-base, bash)

**Files:** Modify `pnwf-lib.bash`, `pnwf.sh`, both `tests/*.bats`, `pnwf.md`, both completions.

**Interfaces — Produces:** `pnwf fork-preflight <branch> [--repos …]` (`proceed|resume|stop` + reason); `pnwf land-plan <branch>` (topo repos still needing landing; `[ -e ]` presence; subset-aware); `pnwf cleanup <branch> [--force-dirty-worktree-removal] [--force-unlanded-branch-removal]`; `pnwf status <branch>` (per-repo table).

- [ ] **Step 1 — Failing tests** (both bats files). Review-critical, all via the `run bash -euo pipefail -c` CLI harness against the built binary:
  - `pnwf cleanup` on a set: repo A landed (exit 0), B not-landed (exit 1), C absent (exit 128) → processes all, removes A, keeps B+C, **exit 0 (no abort on 1/128)**, report names force flags for B.
  - `pnwf land-plan` skips absent worktrees (`[ -e ]` false), includes a present PR worktree, **no abort** on an absent member branch (128).
  - subset set: members from the set's own lock (excluded repo absent from plan, distinct from removed).
  - `fork-preflight`: canonical off-primary → `stop`; existing set dir/branch → `resume`; clean + no set → `proceed`.
- [ ] **Step 2 — Verify fail** → **Step 3 — Implement** (guarded probes; `cleanup` best-effort loop: per-repo `git worktree remove`/`git branch -d` primary, `pn workspace workforest remove` only when no worktree kept, never abort on one repo) → **Step 4 — Verify pass** (incl. non-abort).
- [ ] **Step 5 — Update `pnwf.md` + completions; `nix build .#pnwf` + `nix flake check` green.**
- [ ] **Step 6 — Commit** `feat(pnwf): add fork-preflight/land-plan/cleanup/status`.

---

## Task 5: `pnwf sync-fetch` (repo-base, bash)

**Files:** Modify `pnwf-lib.bash`, `pnwf.sh`, tests, `pnwf.md`, completions.

**Interfaces — Produces:** `pnwf sync-fetch [--set]` — per-repo `git fetch origin` then guarded rebase onto remote primary; stop-and-report on first conflict (repo+path), agent-owned recovery.

- [ ] **Step 1 — Failing tests** (mock `git`: clean rebase all repos; conflicting rebase stops on first, prints repo+path, non-zero; idempotent re-run) → **2 fail → 3 implement (guarded) → 4 pass.**
- [ ] **Step 5 — `pnwf.md` + completions; nix gates green.**
- [ ] **Step 6 — Commit** `feat(pnwf): add sync-fetch WORK-recipe helper`.

---

## Task 6: `pnwf` home module + guarded flake wiring + enable-default test (agent-support, nix)

**Files:** Create `home/programs/pnwf/default.nix`; Modify `home/default.nix`, `flake.nix`.

**Interfaces:** Consumes `inputs.phillipgreenii-nix-base.packages.<system>.pnwf` (via the overlay). Produces `pnwf` on PATH iff `claude-code.enable && pluginEnabled "pn-workspace-rules"`, + a tldr custom page.

- [ ] **Step 1 — Guarded overlay entry (C1).** In agent-support `flake.nix` `overlay`, add `pnwf` from `inputs.phillipgreenii-nix-base.packages.${final.stdenv.hostPlatform.system} or {}` with the same `? pnwf` / `or {}` guard as the marketplace-drv block (`:1648-1656`), so `pkgs.pnwf` exists on the 2 published systems and is absent-but-graceful on the other 2.
- [ ] **Step 2 — Author `home/programs/pnwf/default.nix`** as a faithful mirror of `integrate-branch-support/default.nix`: local `mcfg`/`activeMarketplaces`/`pluginEnabled` let-bindings; `enable.default = claudeEnable && pluginEnabled "pn-workspace-rules"` + matching `defaultText`; `package = lib.mkPackageOption pkgs "pnwf" { }` (now a true mirror, no `inputs` arg — M2/C1); `config = lib.mkIf cfg.enable { home.packages = [ cfg.package ]; programs.tldr.customPages.pnwf = lib.mkIf config.programs.tldr.enable { platform = "common"; source = "${cfg.package}/share/tldr/pages.common/pnwf.md"; }; }`.
- [ ] **Step 3 — Register** in `home/default.nix` imports (mirror the `./programs/integrate-branch-support` entry).
- [ ] **Step 4 — Enable-default evalModules test (M2).** Mirror `test-integrate-branch-support-enable-default` (`flake.nix:662-754`), 4 scenarios: default-on (claude on + plugin enabled), overridden-off, claude-off, explicit-on. It reads only `.enable` (never forces `.package`, so `specialArgs = { inherit pkgs lib; }` suffices). Wire into `checks`.
- [ ] **Step 5 — Gate.** From the workforest: `pn workspace flake-check` → green **on all declared systems** (verifies C1 guard). Confirm `enable` resolves true on this host.
- [ ] **Step 6 — Commit** (agent-support worktree): `feat(home): ship pnwf on PATH when pn-workspace-rules plugin enabled`.

---

## Tasks 7–10: The four stage-skills (repo-base plugin, prose)

Each subagent MUST receive design §3.1–3.2, §4.1–4.5 verbatim. Common (Global Constraints): `RUN FROM:` banner; RFC 2119; mermaid where clarifying; disambiguating `description`; any non-zero `pnwf` exit ⇒ halt-and-report; deterministic logic lives in `pnwf`, judgment in the skill.

- [ ] **Task 7 — `fork-workforest/SKILL.md`** (RUN FROM: canonical root). `pnwf fork-preflight`; `stop`→halt-and-report (R-3/R-8); `resume`→agent+user decide resume-vs-discard; `proceed`→`pn workspace workforest add <branch> [--repos]` + `cd`. Commit.
- [ ] **Task 8 — `validate-workforest/SKILL.md`** (RUN FROM: inside the set). On success the _workspace_ is valid; use `pnwf` facts to inform the existing Completion-Gate tier (reference, don't restate); default Tier-3 `pn workspace build` where "touches the assembled system" isn't script-decidable; MUST NOT fail on dirty (warns). Commit.
- [ ] **Task 9 — `land-workforest/SKILL.md`** (RUN FROM: inside the set). Thin orchestrator over the `integrate-branch` skill; `pnwf land-plan` → topo repos; per repo invoke `integrate-branch` from its worktree, mapping `landed`→continue, `pr-opened`/`pr-updated`→stop-and-report before any consumer, `stopped:<reason>`→stop-and-report, "nothing to land"→continue; resume skips absent worktrees, PR repos re-run idempotently; on stop `pnwf status` + reason→next-action map; `validate` SHOULD precede. **Description: whole coordinated set (topo order); single branch/repo → `integrate-branch` directly.** No fan-out. Commit.
- [ ] **Task 10 — `cleanup-workforest/SKILL.md`** (RUN FROM: canonical root; `cd` to canonical first). Wraps `pnwf cleanup`; explains merge-base landed-test (0/1/128), never `git branch -d` as the test; best-effort; preserves PR + un-landed branches; documents both force flags; agent may act on what the tool refuses. Commit.

---

## Task 11: `/pn-workspace-sync` command (repo-base plugin, prose Facade)

**Files:** Create `pn-workspace-rules/commands/pn-workspace-sync.md`.

- [ ] **Step 1 — Author the Facade:** fork `pn-workspace-sync` → WORK `pnwf sync-fetch` (+ conflict resolution) → validate → land → cleanup → POST (from canonical): `pn workspace update --siblings-only`, `pn workspace push`. Opening summary MUST state plainly that on success it **pushes every repo to `origin/main`** (authorized by invocation — no second gate). Reference the stage skills by name.
- [ ] **Step 2 — Commit** `feat(pn-workspace-rules): add /pn-workspace-sync command`.

---

## Task 12: `pn-workspace-rules` SKILL refactor (repo-base plugin, prose)

**Files:** Modify `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`.

- [ ] **Step 1 — Edits (MUST):** (a) point the "Landing a set onto `main`" recipe (`~:374-406`) at `land-workforest`, dropping its inline set-wide rebase (state the change); (b) **reconcile EVERY `PN_WORKSPACE_ROOT` `unset`-preferred occurrence** to the explicit `PN_WORKSPACE_ROOT=<setdir> pn workspace …` form — at minimum `:313`, `:342-343`, `:385`, and the whole section `:431-438` (M6 — enumerate all, don't stop at three); (c) single-source the shared spine (stages point at it; no duplication).
- [ ] **Step 2 — Verify** `nix build .#phillipg-nix-repo-base-marketplace` builds; SKILL.md well-formed. Commit `refactor(pn-workspace-rules): land-set→land-workforest, explicit PN_WORKSPACE_ROOT, single-source spine`.

---

## Task 13: Cross-repo validation, landing, relock, and the supervised live run

> Ordering here is deliberate: **land the code first, then relock, then (user) apply, then the supervised live run** — because `/pn-workspace-sync` and `pnwf` are only invocable once built + reinstalled + on the applied system, and the live run pushes to `origin/main` (H3, M4).

- [ ] **Step 1 — Fresh-worktree pre-commit (M5):** install the hook config in each edited worktree (repo-base + agent-support) before relying on commits; run `pn workspace pre-commit-check` (all-files) → green.
- [ ] **Step 2 — Tier-2 gate:** `pn workspace flake-check` from the workforest → green **on all declared systems** (catches C1 + consumer-side breakage).
- [ ] **Step 3 — Tier-3 gate:** `pn workspace build` inside the set → full host system builds; verify the closure contains `pnwf`. Then `pn workspace doctor` → consistent.
- [ ] **Step 4 — Land the code** via the `integrate-branch` skill per repo, topo order (**repo-base before agent-support**, stop-on-blocked). Per-repo strategy is resolved by `integrate-branch` (most are `ff-merge-to-main`, local, no push).
- [ ] **Step 5 — Producer→consumer relock (M4):** after repo-base lands, relock agent-support's repo-base input (`pn workspace update --siblings-only`) so agent-support's `flake.lock` points at the repo-base rev carrying `pnwf`. NOTE: `--siblings-only` **pushes** (a sibling must be pushed before consumers relock) — so this step, and the applied delivery of `pnwf`, require pushing repo-base to `origin` (surface this to the user; pushing is a separate, user-authorized step). Remove the set from canonical; re-run Tier-3 build on canonical `main` as a post-land recheck.
- [ ] **Step 6 — Reinstall + apply (USER-ONLY):** the plugin (four skills + command) and `pnwf` reach the live session only after the marketplace is rebuilt/reinstalled and the home-manager system is applied. `pn workspace apply` is **user-only** — coordinate; I do not run it.
- [ ] **Step 7 — Supervised live run (acceptance criterion — USER-INVOLVED, scoped):** BEFORE running, (a) confirm each repo's `integrate-branch` strategy via `pnwf` / `integrate-branch-support`; if the terminal (or any repo) resolves to `pull-request`, either scope the run with `--repos` to the `ff-merge-to-main` repos or accept the documented PR stop (design §12); (b) state exactly which repos will be pushed to `origin/main`; (c) with the user, run `/pn-workspace-sync` once end-to-end and confirm fork→sync-fetch→validate→land→cleanup→POST succeed. **This step pushes to `origin/main`; it requires explicit user go-ahead at run time.**
- [ ] **Step 8 — Close beads:** `bd close` the child beads + `pg2-xs5cj`; `bd remember` durable learnings (the `integrate-branch-support`-has-no-`--json` correction; the two-repo guarded-overlay wiring; the `pnwf resolve` `PN_WORKSPACE_ROOT`-clearing crux).

---

## Self-Review (writing-plans checklist)

**Spec + acceptance coverage:** four stage skills (T7–10); `/pn-workspace-sync` (T11); `pnwf` + bats incl. **non-vacuous** exit-1/128 non-abort (T2 H1) + subset enumeration (T3/T4) + primary-branch parity (T2); `info --json` + Go test incl. edge cases (T1 M1); `pnwf` installs iff plugin enabled **with an evalModules test** (T6 M2); `validate` guarantees validity (T8); `cleanup` merge-base test + preserves PR/un-landed + force flags (T10); SKILL refactor across **all** `PN_WORKSPACE_ROOT` sites (T12 M6); pre-commit all-files gate (T13 M5); supervised live run properly gated (T13 H3). All map.

**Placeholder scan:** the one remaining runtime decision — whether the supervised live run is scoped via `--repos` or accepts a PR stop — is explicitly deferred to run-time user input (T13 Step 7), not hand-waved.

**Type consistency:** `pnwf` subcommand names stable T3–5/T7–12; JSON field names (`workforests_dir`/`in_workforest`/`canonical_root`; `primary_branch`/`strategy`/`canonical.{branch,dirty}`) consistent T1/T2/T9.

**Critique fixes folded:** C1, H1, H2, H3, M1–M6, L2, L3 all addressed above.
