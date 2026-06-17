# gomod2nix Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the `mkGoApp`/`mkGoBinary` Go-package family from nixpkgs `buildGoModule` (+ the ADR-0007 local-replace overlay + `vendorHash`) to `gomod2nix`/`buildGoApplication`, eliminating the `vendorHash`-drift class and making first-party local-replace modules natively "live."

**Architecture:** Add `gomod2nix` as a flake input + overlay to each flake that builds Go. Rewrite `mkGoApp` in `phillipg-nix-repo-base/lib/go-builders.nix` to support a **gomod2nix engine** alongside the existing `buildGoModule` engine, selected per-package by passing a `gomod2nixToml`. Migrate packages one at a time (each gets a committed `gomod2nix.toml`, builds green in _and_ out of the pn workspace), then delete the old engine + the ADR-0007 overlay once every consumer is migrated. This dual-engine transition gives per-package, reversible feedback rather than a flag-day across four repos.

**Tech Stack:** Nix flakes, nix-darwin/home-manager, `gomod2nix` v1.7.0 (`buildGoApplication`), Go (modernc.org/sqlite, bubbletea, otel/grpc), `pn-workspace` multi-repo overrides.

**Authority:** Implements ADR `phillipg-nix-repo-base/docs/adr/0008-adopt-gomod2nix-for-go-packages.md` (supersedes 0007; retains 0006 versioning). Spike: bead `pg2-gjzz` (closed). Cross-repo follow-up (out of scope): `pg2-wtjz`.

---

## Scope, package table, and the two patterns

**In scope — the `mkGoApp`/`mkGoBinary` family (8 packages, 3 repos):**

| Package                         | Repo                     | Builder today | Pattern                        | `subPackages`                                                                        | Notes                                           |
| ------------------------------- | ------------------------ | ------------- | ------------------------------ | ------------------------------------------------------------------------------------ | ----------------------------------------------- |
| `pn`                            | repo-base (`modules/pn`) | mkGoBinary    | A                              | n/a — `mkGoBinary` has no `subPackages` arg; builds all pkgs, installs `cmd/pn` main | canary; man-page + completions; never `[ "." ]` |
| `ccpool`                        | agent-support            | mkGoApp       | **B** (`../claude-transcript`) | `[ "cmd/ccpool" ]`                                                                   | proven in spike                                 |
| `pa-monitor`                    | agent-support            | mkGoApp       | **B**                          | `[ "cmd/pa-monitor" ]`                                                               | heaviest deps (otel/grpc/sqlite)                |
| `pr-pool`                       | agent-support            | mkGoApp       | **B**                          | verify                                                                               | depends on pg-pr/ccpool at runtime              |
| `pg-pr`                         | agent-support            | mkGoApp       | A                              | verify                                                                               | no local replace                                |
| `claude-extended-tool-approver` | agent-support            | mkGoApp       | A                              | verify                                                                               | no local replace                                |
| `pa-monitor-decorator-gc`       | agent-support            | mkGoApp       | A                              | verify                                                                               | `vendorHash = null` (no deps) → near-empty toml |
| `activity-collector`            | support-apps             | mkGoApp       | A                              | verify                                                                               | no local replace                                |

**Out of scope (raw `buildGoModule`, no wrapper, no local replace):** `beads` (ziprecruiter), `statusBar` (personal), `goccc`, `gascity` (agent-support). Out of scope entirely: cross-repo `pg-pr-zr` (`pg2-wtjz`); `go.work`.

**Pattern A (single module at package root):** `src = lib.cleanSource ./.`, no `modRoot`, `gomod2nixToml = ./gomod2nix.toml`. `mkGoApp` sets `pwd = src`.

**Pattern B (local `replace => ../sibling`):** `src = lib.fileset.toSource { root = ./..; fileset = unions [ ./. ../sibling ]; }`, `modRoot = "<name>"`, `gomod2nixToml = ./gomod2nix.toml`. `mkGoApp` sets `pwd = src + "/<name>"` so the replace symlink + toml resolve in one store tree.

---

## Phase 1 — Infrastructure

### Task 1: Add `gomod2nix` input + overlay to `phillipg-nix-repo-base`

> **repo-base is special.** Unlike agent-support/support-apps (which build an overlaid `pkgs` via
> `import nixpkgs { overlays = [...]; }` and pass it to `mkGoBuilders`), repo-base uses
> `pkgs = nixpkgs.legacyPackages.${system}` (flake.nix line ~51) — a raw set you **cannot** add
> overlays to. So `pkgs.buildGoApplication` is missing inside `mkGoApp` until we convert it. This
> is a code change, not an "add to a list" edit.

**Files:**

- Modify: `phillipg-nix-repo-base/flake.nix` (inputs block; `outputs` arg set; the `pkgs` binding)

- [ ] **Step 1: Add the input.** In `flake.nix` `inputs`, add (follow only `nixpkgs`; gomod2nix
      exposes no `flake-utils` input to follow, so do NOT add a `flake-utils.follows` line):

```nix
gomod2nix = {
  url = "github:nix-community/gomod2nix";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

- [ ] **Step 2: Make `gomod2nix` available to `outputs`.** Add `gomod2nix` to the `outputs = { self, nixpkgs, ... }:` argument set (the flake destructures named inputs).

- [ ] **Step 3: Convert `pkgs` to an overlaid import.** Replace `pkgs = nixpkgs.legacyPackages.${system};` (line ~51) with:

```nix
pkgs = import nixpkgs {
  inherit system;
  overlays = [ gomod2nix.overlays.default ];
};
```

This `pkgs` is the one passed to `modules/pn` and `lib/go-builders.nix`, so the overlay now reaches `mkGoApp`.

- [ ] **Step 4: Verify the overlay reaches the wrapper.**

Run: `cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base && nix eval --raw .#packages.aarch64-darwin.pn.pname 2>&1 | tail -3`
Expected: prints `pn` (eval succeeds — confirms `pkgs` still builds `pn` after the conversion; `buildGoApplication` is now present though unused until Task 3). If it errors with anything about overlays/`import nixpkgs`, the conversion is malformed.

- [ ] **Step 5: Commit.**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git add flake.nix flake.lock
git commit -m "feat(go): add gomod2nix input + overlaid pkgs (ADR 0008)"
```

### Task 2: Add the gomod2nix engine to `mkGoApp` (dual-engine)

**Files:**

- Modify: `phillipg-nix-repo-base/lib/go-builders.nix` (the `mkGoApp` function)

- [ ] **Step 1: Replace the `mkGoApp` body with a dual-engine version.** Keep the existing `buildGoModule` branch (incl. `localReplaceModules` overlay) as the default; add a `gomod2nix` branch taken when `gomod2nixToml != null`:

```nix
  mkGoApp =
    {
      pname,
      src,
      vendorHash ? null,
      versionPath ? "main.version",
      baseVersion ? "0.0.0",
      ldflags ? [ ],
      localReplaceModules ? [ ],
      # gomod2nix engine (ADR 0008): when set, build with buildGoApplication and
      # IGNORE vendorHash/localReplaceModules. `gomod2nixToml` is the committed
      # path (conventionally ./gomod2nix.toml beside go.mod).
      gomod2nixToml ? null,
      modRoot ? null,
      ...
    }@args:
    let
      version = "${baseVersion}-${(import ./version.nix).mkSrcDigest src}";
    in
    if gomod2nixToml != null then
      # ---- gomod2nix engine ----
      let
        pwd = if modRoot != null then src + "/" + modRoot else src;
        # NOTE: `modRoot` is intentionally NOT stripped — it stays in `forwarded`
        # so buildGoApplication uses it as the build working dir (verified in the
        # spike). `pwd` carries module/replace resolution; `modRoot` carries cwd.
        forwarded = builtins.removeAttrs args [
          "versionPath" "baseVersion" "ldflags" "version"
          "vendorHash" "localReplaceModules" "gomod2nixToml"
        ];
      in
      pkgs.buildGoApplication (
        forwarded
        // {
          inherit version pwd;
          go = pkgs.go; # pin to our nixpkgs Go, not gomod2nix's
          modules = pwd + "/gomod2nix.toml";
          ldflags = ldflags ++ [ "-X ${versionPath}=${version}" ];
        }
      )
    else
      # ---- buildGoModule engine (existing; remove in Phase 4) ----
      <UNCHANGED existing body: the buildGoModule call with the
       localReplaceModules strip+overlay and overrideModAttrs FOD-name pin>;
```

Leave the existing `buildGoModule` branch exactly as-is (it is still the default until Phase 4).

- [ ] **Step 1b: Thread the new params through `mkGoBinary`.** `mkGoBinary` is a **closed arg set** (no `...@args`) and calls `mkGoApp` with an explicit attr list — it does NOT forward unknown args, so `pn` (its only in-scope consumer) cannot reach the gomod2nix engine until this is added. Add `gomod2nixToml ? null,` and `modRoot ? null,` to the `mkGoBinary` signature, and add `inherit gomod2nixToml modRoot;` to its inner `mkGoApp { ... }` call.

- [ ] **Step 2: Format.**

Run: `cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base && nix fmt -- lib/go-builders.nix`
Expected: no diff errors.

- [ ] **Step 3: Confirm no existing consumer changed behavior.** No consumer passes `gomod2nixToml` yet, so every package still takes the `buildGoModule` branch.

Run: `nix build .#pn --no-link 2>&1 | tail -3` (from repo-base)
Expected: builds exactly as before (cached or rebuilt green).

- [ ] **Step 4: Commit.**

```bash
git add lib/go-builders.nix
git commit -m "feat(go): add gomod2nix engine to mkGoApp behind gomod2nixToml (ADR 0008)"
```

- [ ] **Step 5: `nix flake check` repo-base.**

Run: `cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base && nix flake check 2>&1 | tail -15`
Expected: passes (the dual-engine wrapper is backward-compatible).

---

## Per-package migration procedure (referenced by Tasks 3–10)

For each package below, run this exact procedure (values from the scope table). `<dir>` is the package directory, `<flake>` its flake root, `<pkg>` its attr name.

1. **Ensure the package's flake has the gomod2nix input+overlay** (Task 11 does this for agent-support and support-apps; repo-base done in Task 1). If not yet done for that flake, do Task 11 first.
2. **Generate the lockfile** (host `go` + network):
   ```bash
   cd <dir>
   go mod tidy
   nix run github:nix-community/gomod2nix -- generate
   ```
   Produces `<dir>/gomod2nix.toml`.
3. **Edit `<dir>/default.nix`** to the gomod2nix engine. Remove **only** `vendorHash` (and, Pattern B
   only, `localReplaceModules`); **preserve every other override** — `versionPath` (e.g. `pg-pr`/`cetа`
   set `main.Version`), `subPackages`, `postInstall`, `nativeBuildInputs`, `makeWrapper` wraps, etc.
   - Pattern A: keep `src = lib.cleanSource ./.;`; add `gomod2nixToml = ./gomod2nix.toml;`; delete `vendorHash`.
   - Pattern B: keep the rooted `src` fileset (`./.` already pulls in `gomod2nix.toml`); add `gomod2nixToml = ./gomod2nix.toml;`; keep `modRoot`; delete `vendorHash` **and** `localReplaceModules`.
4. **Build in isolation:**
   ```bash
   cd <flake>
   nix build .#<pkg> --no-link -L 2>&1 | tail -20
   ```
   Expected: exit 0; for Pattern B the log shows the sibling import path (e.g. `github.com/phillipgreenii/claude-transcript`) compiling.
5. **Build as the pn workspace builds it** (local overrides, as `pn-workspace-apply` does). The
   repo-base input is named `phillipgreenii-nix-base` in **both** agent-support and support-apps
   (verified), so this flag is correct for both. Caution: nix **silently ignores** an
   `--override-input` for an unknown input name — if it ever no-ops you'd be testing the _pinned_
   repo-base, not local, so confirm the input name if you adapt this to another repo.
   ```bash
   nix build .#<pkg> --no-link \
     --override-input phillipgreenii-nix-base /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base \
     2>&1 | tail -5
   ```
   Expected: exit 0 (proves it works with the local repo-base that carries the new `mkGoApp`).
6. **Commit** the toml + default.nix together:
   ```bash
   git add <dir>/gomod2nix.toml <dir>/default.nix go.sum 2>/dev/null
   git commit -m "feat(<pkg>): build with gomod2nix engine (ADR 0008)"
   ```

> Rough edge to watch (Pattern B): if step 4 fails with `cannot find package .../claude-transcript in .../vendor/...`, `pwd` did not resolve into the rooted tree — confirm `modRoot` is set and `src` roots at `packages/`.

---

## Phase 2 — Canary

### Task 3: Migrate `pn` (repo-base, Pattern A — simplest, no replace)

**Files:**

- Create: `phillipg-nix-repo-base/modules/pn/gomod2nix.toml`
- Modify: `phillipg-nix-repo-base/modules/pn/default.nix` (the `mkGoBinary` call)

- [ ] **Step 1:** Run the per-package procedure with `<dir>=modules/pn`, `<flake>=phillipg-nix-repo-base`, `<pkg>=pn`, Pattern A. `pn` uses `mkGoBinary` (already extended in Task 2 Step 1b): add `gomod2nixToml = ./gomod2nix.toml;` to the `mkGoBinary` args and delete its `vendorHash` **if present**. Do NOT add `subPackages` (mkGoBinary has no such arg; buildGoApplication builds all packages and installs the `cmd/pn` main).

- [ ] **Step 2: Verify the man page + completions still generate** (mkGoBinary's `postInstall`):

Run: `nix build .#pn --no-link -L 2>&1 | grep -iE "help2man|completion|WARN" | tail`
Expected: no `help2man failed` / completion-generation errors (postInstall passes through buildGoApplication unchanged).

- [ ] **Step 3: Verify the checkPhase (tests) still runs + passes.** `buildGoApplication` runs `doCheck` by default, same as `buildGoModule` does today, and `mkGoBinary` passes `testDeps` as `nativeCheckInputs` — so pn's `cmd/pn` integration test runs in-sandbox exactly as it does now.

Run: `nix build .#pn --no-link -L 2>&1 | grep -iE "RUN|--- FAIL|FAIL\b|ok  |PASS" | tail`
Expected: no `FAIL`. (If pn's integration test cannot run hermetically under buildGoApplication where it did under buildGoModule, that is a regression to fix here — not to paper over with `doCheck=false`.)

- [ ] **Step 4: Smoke-test the binary.**

Run: `nix build .#pn && ./result/bin/pn --help >/dev/null && echo OK`
Expected: `OK`.

- [ ] **Step 5: Commit** (per-package procedure step 6).

---

## Phase 3 — Local-replace packages (Pattern B)

### Task 4: Migrate `ccpool` (agent-support — proven in spike)

**Files:**

- Create: `phillipgreenii-nix-agent-support/packages/ccpool/gomod2nix.toml`
- Modify: `phillipgreenii-nix-agent-support/packages/ccpool/default.nix`

- [ ] **Step 1:** Do Task 11 (agent-support input+overlay) first if not done.
- [ ] **Step 2:** Run the per-package procedure, `<dir>=packages/ccpool`, `<flake>=phillipgreenii-nix-agent-support`, `<pkg>=ccpool`, **Pattern B**. The resulting `default.nix` matches ADR 0008 §"Case B": keep the `lib.fileset.toSource { root = ./..; fileset = unions [ ./. ../claude-transcript ]; }` src and `modRoot = "ccpool"`; add `gomod2nixToml = ./gomod2nix.toml;`; delete `vendorHash` and `localReplaceModules`.
- [ ] **Step 3:** Confirm step-4 build log contains `github.com/phillipgreenii/claude-transcript`.
- [ ] **Step 4:** Run ccpool's tests via the build (gomod2nix `doCheck` defaults on):

Run: `nix build .#ccpool --no-link -L 2>&1 | grep -iE "ok |FAIL|--- FAIL|PASS" | tail`
Expected: no `FAIL`.

- [ ] **Step 5: Commit.**

### Task 5: Migrate `pa-monitor` (agent-support, Pattern B — heaviest deps)

**Files:**

- Create: `phillipgreenii-nix-agent-support/packages/pa-monitor/gomod2nix.toml`
- Modify: `phillipgreenii-nix-agent-support/packages/pa-monitor/default.nix`

- [ ] **Step 1:** Per-package procedure, `<dir>=packages/pa-monitor`, `<pkg>=pa-monitor`, Pattern B (sibling `../claude-transcript`, `modRoot = "pa-monitor"`, `subPackages = [ "cmd/pa-monitor" ]`). Delete `vendorHash` + `localReplaceModules`; add `gomod2nixToml = ./gomod2nix.toml;`. Keep the existing `postInstall` (ccusage/gh/tmux/cmux wrap) and `makeWrapper` — they pass through.
- [ ] **Step 2:** Build + confirm `claude-transcript` compiles (step 4 of procedure) and the wrapped PATH binaries are still wrapped:

Run: `nix build .#pa-monitor && head -5 result/bin/pa-monitor | grep -q export && echo WRAPPED`
Expected: `WRAPPED` (wrapper preserved).

- [ ] **Step 3: Commit.**

### Task 6: Migrate `pr-pool` (agent-support, Pattern B)

**Files:**

- Create: `phillipgreenii-nix-agent-support/packages/pr-pool/gomod2nix.toml`
- Modify: `phillipgreenii-nix-agent-support/packages/pr-pool/default.nix`

- [ ] **Step 1:** First confirm `subPackages` and the cmd path: `ls packages/pr-pool/cmd`.
- [ ] **Step 2:** Per-package procedure, Pattern B (`modRoot = "pr-pool"`, sibling `../claude-transcript`). Delete `vendorHash` + `localReplaceModules`; add `gomod2nixToml`.
- [ ] **Step 3:** Build in + out of workspace (procedure steps 4–5). Confirm `claude-transcript` compiles.
- [ ] **Step 4: Commit.**

---

## Phase 4 — Remaining Pattern-A packages

### Task 7: Migrate `pg-pr` (agent-support, Pattern A)

**Files:** Create `packages/pg-pr/gomod2nix.toml`; Modify `packages/pg-pr/default.nix`.

- [ ] **Step 1:** Confirm it has no local replace: `grep -E "replace .*=> \.\." packages/pg-pr/go.mod` → expect empty.
- [ ] **Step 2:** Per-package procedure, Pattern A (`src = lib.cleanSource ./.` if not already; no `modRoot`; `subPackages` per current file).
- [ ] **Step 3: Commit.**

### Task 8: Migrate `claude-extended-tool-approver` (agent-support, Pattern A)

**Files:** Create `packages/claude-extended-tool-approver/gomod2nix.toml`; Modify its `default.nix`.

- [ ] **Step 1:** Per-package procedure, Pattern A.
- [ ] **Step 2: Commit.**

### Task 9: Migrate `pa-monitor-decorator-gc` (agent-support, Pattern A — no-dep)

**Files:** Create `packages/pa-monitor-decorator-gc/gomod2nix.toml`; Modify its `default.nix`.

- [ ] **Step 1:** Per-package procedure, Pattern A. NOTE: this package has `vendorHash = null` (no external deps); `gomod2nix generate` writes a near-empty toml — commit it anyway.
- [ ] **Step 2:** Confirm build is green; delete `vendorHash = null`.
- [ ] **Step 3: Commit.**

### Task 10: Migrate `activity-collector` (support-apps, Pattern A)

**Files:** Create `phillipgreenii-nix-support-apps/packages/activity-collector/gomod2nix.toml`; Modify its `default.nix`.

- [ ] **Step 1:** Do Task 11 (support-apps input+overlay) first if not done.
- [ ] **Step 2:** Per-package procedure, `<flake>=phillipgreenii-nix-support-apps`, Pattern A.
- [ ] **Step 3: Commit.**

### Task 11: Add `gomod2nix` input + overlay to agent-support and support-apps

**Files:**

- Modify: `phillipgreenii-nix-agent-support/flake.nix`
- Modify: `phillipgreenii-nix-support-apps/flake.nix`

- [ ] **Step 1:** In each flake, add the `gomod2nix` input (same stanza as Task 1 Step 1, with `inputs.nixpkgs.follows = "nixpkgs"`).
- [ ] **Step 2:** Apply `inputs.gomod2nix.overlays.default` to the `pkgs` overlays list that is passed into `mkGoBuilders` (so `pkgs.buildGoApplication` exists inside `mkGoApp`).
- [ ] **Step 3:** Verify per repo: `nix eval .#packages.aarch64-darwin.<anyGoPkg>.pname` resolves (overlay didn't break eval).
- [ ] **Step 4: Commit** `flake.nix` + `flake.lock` in each repo: `feat(go): add gomod2nix input + overlay (ADR 0008)`.

> Run Task 11 **before** the first package in each repo (it gates Tasks 4–10). It's listed here for locality; sequence it first within each repo.

---

## Phase 5 — Dependency-update tooling

### Task 12: Replace `vendorHash`/`nix-update` refresh with `gomod2nix generate`

**Files:**

- Modify: each repo's `update-locks.sh` and any per-package `update-deps.sh` that runs `nix-update` for a now-migrated Go package.

- [ ] **Step 1: Find the Go-dep refresh logic.**

Run: `grep -rn "nix-update\|vendorHash\|update-deps" phillipgreenii-nix-agent-support phillipgreenii-nix-support-apps phillipg-nix-repo-base --include="*.sh" | grep -v /nix/store`
Expected: a list of the scripts to edit.

- [ ] **Step 2:** For each migrated Go package, replace its `nix-update -F --no-src --version=skip <pkg>` step with:

```bash
( cd "<dir>" && go mod tidy && nix run github:nix-community/gomod2nix -- generate )
```

and drop the `git core.fsmonitor` suspend hack that `nix-update` required (per support-apps ADR 0035) where it was only there for `nix-update`.

- [ ] **Step 3:** Dry-run one repo's `update-locks.sh` and confirm it regenerates a toml and the package still builds.

Run (agent-support): `./update-locks.sh 2>&1 | tail -20 && nix build .#ccpool --no-link 2>&1 | tail -3`
Expected: toml regenerated (likely no diff if deps unchanged), build green.

- [ ] **Step 4: Commit** each script change.

---

## Phase 6 — Remove the old engine + document the pattern

### Task 13: Delete the `buildGoModule` branch + ADR-0007 overlay from `mkGoApp`

**Pre-req:** every in-scope package now passes `gomod2nixToml` and builds green (Tasks 3–10 done).

**Files:**

- Modify: `phillipg-nix-repo-base/lib/go-builders.nix`

- [ ] **Step 1: Confirm no consumer still uses the old engine.**

Run: `grep -rn "vendorHash\|localReplaceModules" phillipgreenii-nix-agent-support phillipgreenii-nix-support-apps phillipg-nix-repo-base --include="*.nix" | grep -v /nix/store`
Expected: only matches inside `go-builders.nix` itself (no consumer matches). If a consumer matches, migrate it first.

- [ ] **Step 2:** In `mkGoApp`, delete the entire `else` (buildGoModule) branch, the `localReplaceModules`/`stripLocalFromModules`/`overlayLocalModules` logic, the `vendorHash` param, and the `overrideModAttrs` FOD-name pin. Make `gomod2nixToml` required (drop the `? null` and the `if gomod2nixToml != null`). Keep `version`, `pwd`/`modRoot`, `go` pin, `ldflags`.

- [ ] **Step 3:** Update the `mkGoApp` doc-comment to describe the gomod2nix engine + Pattern A/B (mirror ADR 0008 §"The pattern"). This comment is the in-code canonical reference.

- [ ] **Step 4: Build every Go package once more** (in + out of workspace) to confirm the simplified wrapper:

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
for p in ccpool pa-monitor pr-pool pg-pr claude-extended-tool-approver pa-monitor-decorator-gc; do
  nix build .#$p --no-link --override-input phillipgreenii-nix-base /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base 2>&1 | tail -2; echo "[$p done]"
done
```

Expected: every `[<p> done]` with no error above it.

- [ ] **Step 5:** `nix fmt` + `nix flake check` repo-base, agent-support, support-apps. Expected: all pass.

- [ ] **Step 6: Commit:** `refactor(go): drop buildGoModule engine + ADR-0007 overlay; gomod2nix only (ADR 0008)`.

### Task 14: Document the pattern for future agents

**Files:**

- Modify: `phillipg-nix-repo-base/CLAUDE.md` (Versioning/Go section)
- Modify: `phillipgreenii-nix-agent-support/CLAUDE.md` (the "Versioning of Custom Packages" + "AI Agent Package Sourcing" area)
- Verify: ADR 0008 is the single source of truth (already written)

- [ ] **Step 1:** In `phillipg-nix-repo-base/CLAUDE.md`, add a short "Go packages" subsection: "All Go apps use `mkGoApp`/`mkGoBinary` on the gomod2nix engine. Commit a `gomod2nix.toml` beside `go.mod`; bump deps with `go mod tidy && nix run github:nix-community/gomod2nix -- generate`. Pattern A (root module) vs Pattern B (local `replace`): see ADR 0008. Never reintroduce `vendorHash`/`buildGoModule` for these." Link `docs/adr/0008-adopt-gomod2nix-for-go-packages.md`.

- [ ] **Step 2:** In `phillipgreenii-nix-agent-support/CLAUDE.md`, replace any `vendorHash`/`update-deps` Go guidance with the gomod2nix pointer to ADR 0008 (cross-repo reference form).

- [ ] **Step 3:** Grep for stale guidance:

Run: `grep -rn "vendorHash\|buildGoModule\|localReplaceModules\|nix-update" */CLAUDE.md 2>/dev/null`
Expected: no stale Go-build instructions remain for the migrated family (out-of-scope `buildGoModule` packages may still be referenced — leave those).

- [ ] **Step 4: Commit:** `docs: document gomod2nix Go pattern for agents (ADR 0008)`.

### Task 15: Mark ADR-0035 (support-apps) superseded for the mkGoApp family

**Files:**

- Modify: `phillipgreenii-nix-support-apps/docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md` (status note) + that repo's adr `index.md`

- [ ] **Step 1:** Add to ADR 0035 a "Superseded for the `mkGoApp`/`mkGoBinary` family by phillipg-nix-repo-base ADR 0008" note (it still governs any raw-`buildGoModule` packages). Update the support-apps ADR index accordingly.
- [ ] **Step 2: Commit.**

---

## Related beads

- `pg2-gjzz` (CLOSED) — the spike that produced ADR 0007/0008.
- `pg2-wtjz` (OPEN, P3) — cross-repo `pg-pr-zr` nix build; **not** addressed here, but its eventual fix is "expose pg-pr source + apply Pattern B cross-repo." Update it to reference gomod2nix once this lands.
- support-apps ADR-0035 beads (CLOSED): `pg2-sz8f`, `pg2-eg1c`, `pg2-b9pb`, `pg2-o0jd` — the prior `nix-update`/`vendorHash` decisions this migration moves past for the wrapper family.
- **Create on approval:** one tracking bead/epic for this migration with Tasks 1–15 as children (do in execution, not planning).

---

## Rough edges (carry into execution)

1. **`pwd` rooting (Pattern B):** the `../sibling` symlink only resolves if `src` roots at `packages/` and `mkGoApp` sets `pwd = src + "/" + modRoot`. A dangling symlink → `cannot find package .../vendor/...`. Verified shape in the spike.
2. **`gomod2nix.toml` must be git-tracked** for flake builds (incl. pn-workspace `git+file` overrides) to see it. A new package fails until `git add`. This is the same untracked-file trap that caused earlier issues.
3. **Go toolchain:** pin `go = pkgs.go` in `mkGoApp` so the fleet uses our nixpkgs Go, not gomod2nix's pin (which was `go1.25.0` in the spike). Our modules declare `go 1.25.0`, satisfied by either; pinning keeps it consistent.
4. **Cross-repo replace unsupported** (`pg-pr-zr`, gomod2nix #101) — out of scope (`pg2-wtjz`).
5. **`go.work` unsupported** (gomod2nix #98) — do not introduce.
6. **`gomod2nix generate` needs host `go` + network** (downloads the module graph to hash it). Run it on a machine with both; the resulting toml is then fully offline-cacheable.
7. **Maintenance:** gomod2nix is low-velocity (bus factor ~3, incl. Mic92). Pin the input; bump deliberately.

## Rollback

Per-package and reversible until Task 13: revert the package's `default.nix` to the `buildGoModule` branch (restore `vendorHash`/`localReplaceModules`) and delete its `gomod2nix.toml`. The dual-engine wrapper means a single package can sit on either engine. After Task 13 (old engine deleted), rollback = `git revert` the Task-13 commit.

## Verification matrix (run before declaring done)

| Check                                       | Command                                                              | Expected                  |
| ------------------------------------------- | -------------------------------------------------------------------- | ------------------------- |
| Each pkg builds standalone                  | `nix build .#<pkg> --no-link`                                        | exit 0                    |
| Each pkg builds under workspace overrides   | `nix build .#<pkg> --override-input phillipgreenii-nix-base <local>` | exit 0                    |
| Pattern-B pkgs compile the sibling          | build log contains `claude-transcript`                               | present                   |
| Live first-party edit needs no toml regen   | edit `claude-transcript`, rebuild a Pattern-B pkg                    | green, toml unchanged     |
| No stale `vendorHash`/`localReplaceModules` | `grep -rn ... --include="*.nix"`                                     | only in (deleted) history |
| Flakes pass                                 | `nix flake check` in all 3 repos                                     | pass                      |
| Full apply                                  | `pn workspace apply`                                                 | succeeds                  |

## Self-review notes

- **Spec coverage:** ADR 0008 §Decision items 1–4 map to Tasks 1–2 (rewrite), 3–11 (migrate family incl. input/overlay), 12 (tooling), 13 (remove old engine), 14 (docs); cross-repo (item 4) explicitly deferred to `pg2-wtjz`. Pattern A/B from ADR map to the per-package procedure.
- **Atomicity:** the breaking-signature concern from ADR 0008 is handled by the dual-engine transition (old engine stays until Task 13), so no flag-day; Task 13 Step 1 gates removal on zero remaining consumers.
- **Placeholders:** the only `<...>` are the deliberately-unchanged existing `buildGoModule` body (Task 2 Step 1) and per-package variable substitutions defined in the scope table — not TODOs.
