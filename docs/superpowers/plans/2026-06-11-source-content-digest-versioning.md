# Per-source content-digest versioning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every custom artifact's version a function of its own source content (not the repo git rev), so committing/dirtying one thing no longer rebuilds the whole stamped tier.

**Architecture:** Add a shared `mkSrcDigest` helper to `lib/version.nix`; switch the bash and python builders off `gitHash` (repo rev) onto a per-source content digest (Go already does this — refactor it onto the shared helper). The repo-meta module keeps the repo rev. Remove `inherit gitHash;` threading from consumer flakes. Document the convention for future agents.

**Tech Stack:** Nix (flakes, `buildGoModule`, `buildPythonApplication`, `stdenv.mkDerivation`, `lib.runTests`), Bash, Python; spec at `docs/superpowers/specs/2026-06-11-unified-source-versioning-design.md`; decisions in ADR 0006.

**Scope:** Track A only. Track B (pn `nix fmt` removal + `pn workspace format`) and Track D (neovim from-source, via ziprecruiter ADR 0044's playbook) are separate plans. Track C (auto-optimise) is already landed.

**Invariants to preserve (from the spec):**

1. version changes iff the artifact's own source changes (committed or dirty);
2. an unchanged artifact is a cache hit even across an unrelated commit (HEAD move);
3. multi-path/transitive: a change in any included source bumps version AND triggers rebuild;
4. `--version` always prints `YY.MM.DD.SSSSS+<srcdigest8>`.

All repos are personal Nix repos: use simple branch names; no `Refs:` commit trailer.

---

### Task 0: Baseline measurement (re-measure on current `main`)

**Why:** the "84 derivations" figure predates ziprecruiter ADR 0044. Re-baseline so the end-state is falsifiable. No code changes.

**Files:** none (produces `/tmp/versioning-baseline.txt`).

- [ ] **Step 1: Capture the current clean-tree rebuild set**

Run (one line; override names per `pn-workspace.toml`):

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix build ".#darwinConfigurations.phillipg-mbp-02.system" \
  --override-input phillipgreenii-nix-base      "git+file:///Users/phillipg/phillipg_mbp/phillipg-nix-repo-base" \
  --override-input phillipgreenii-agent-support "git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support" \
  --override-input phillipgreenii-nix-overlay   "git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay" \
  --override-input phillipgreenii-personal      "git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-personal" \
  --override-input phillipgreenii-support-apps  "git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps" \
  --dry-run 2>/tmp/versioning-baseline.txt
grep -c '\.drv$' /tmp/versioning-baseline.txt
```

Expected: a non-zero count (the current stamped tier). Record it; this is the "before".

- [ ] **Step 2: Bucket the rebuild set**

Run:

```bash
grep '\.drv$' /tmp/versioning-baseline.txt | sed -E 's#.*/[a-z0-9]+-##; s#\.drv$##' | sort | uniq -c | sort -rn
```

Expected: a list dominated by bash scripts + man pages + the python packages + 5 `*-install-metadata`. Confirm every entry is bash/python/go/repo-meta; note any outlier for follow-up.

---

### Task 1: Add `mkSrcDigest` helper (TDD, pure unit tests)

**Files:**

- Modify: `lib/version.nix`
- Test: `lib/version-tests.nix`
- Branch: `git checkout -b source-content-digest-versioning` in `phillipg-nix-repo-base` (already exists from the design commits — reuse it).

- [ ] **Step 1: Write failing tests in `lib/version-tests.nix`**

Add a `let`-binding near the top (after `digest = ...`):

```nix
  inherit (version) mkSrcDigest;
  sha8 = s: builtins.substring 0 8 (builtins.hashString "sha256" s);
```

Add these cases to the returned attrset:

```nix
  # Single source: digest is first8(sha256) of the (stringified) source.
  testSrcDigestSingle = {
    expr = mkSrcDigest "src-a";
    expected = sha8 "src-a";
  };
  # A single path equals the singleton list of that path.
  testSrcDigestSingleEqualsSingleton = {
    expr = mkSrcDigest "src-a" == mkSrcDigest [ "src-a" ];
    expected = true;
  };
  # Multiple sources are joined with ":" before hashing.
  testSrcDigestListConcat = {
    expr = mkSrcDigest [ "a" "b" ];
    expected = sha8 "a:b";
  };
  # Order-sensitive (callers pass a stable, ordered list).
  testSrcDigestOrderSensitive = {
    expr = mkSrcDigest [ "a" "b" ] != mkSrcDigest [ "b" "a" ];
    expected = true;
  };
  # Content change changes the digest.
  testSrcDigestTracksContent = {
    expr = mkSrcDigest "a" != mkSrcDigest "b";
    expected = true;
  };
```

- [ ] **Step 2: Run the test, verify it fails**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix build ".#checks.aarch64-darwin.version-lib" --no-link 2>&1 | tail -20
```

Expected: FAIL — `attribute 'mkSrcDigest' missing` (helper not defined yet).

- [ ] **Step 3: Implement `mkSrcDigest` in `lib/version.nix`**

Inside the top `let`, add:

```nix
  mkSrcDigest =
    srcs:
    let
      list = if builtins.isList srcs then srcs else [ srcs ];
    in
    builtins.substring 0 8 (
      builtins.hashString "sha256" (builtins.concatStringsSep ":" (map (s: "${s}") list))
    );
```

And export it in the returned attrset (alongside `mkGitHash` / `mkVersion`):

```nix
  inherit mkSrcDigest;
```

- [ ] **Step 4: Run the test, verify it passes**

Run:

```bash
nix build ".#checks.aarch64-darwin.version-lib" --no-link 2>&1 | tail -5
```

Expected: PASS (builds successfully; `runTests` returns no failures).

- [ ] **Step 5: Commit**

```bash
git add lib/version.nix lib/version-tests.nix
git commit -m "feat(version): add mkSrcDigest (per-source content digest helper)"
```

---

### Task 2: Bash builder — remove `gitHash`, version from source digest (TDD)

**Files:**

- Modify: `lib/bash-builders.nix` (the `gitHash` binding line ~12; the `mkBashScript` build phase ~171-199)
- Test: `lib/bash-builders-version-tests.nix` (new) + wire a check in `flake.nix`

The core invariant test: the same script `src` at two different `self.rev` values must produce the **same** derivation path.

- [ ] **Step 1: Write the failing rev-independence check**

Create `lib/bash-builders-version-tests.nix`:

```nix
# Proves mkBashScript's derivation identity is independent of the repo git rev:
# same src + two different self.rev => identical drvPath. Wired into
# flake `checks.bash-version-rev-independent`.
{ pkgs }:
let
  lib = pkgs.lib;
  mk =
    rev:
    (import ./bash-builders.nix {
      inherit pkgs lib;
      self = {
        rev = rev;
        lastModifiedDate = "20260101000000";
        narHash = "sha256-AAA";
      };
    }).mkBashScript {
      name = "demo";
      src = ./fixtures/demo;
      description = "demo script for rev-independence test";
    };
  drvA = (mk "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").script.drvPath;
  drvB = (mk "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").script.drvPath;
in
pkgs.runCommand "bash-version-rev-independent" { inherit drvA drvB; } ''
  if [ "$drvA" != "$drvB" ]; then
    echo "FAIL: script drvPath depends on repo rev:"; echo "  $drvA"; echo "  $drvB"; exit 1
  fi
  echo "OK: script drvPath is rev-independent"; touch $out
''
```

Create the fixture script `lib/fixtures/demo/demo.sh`:

```bash
echo "hello from demo"
```

Wire the check in `flake.nix` (in the `checks` attrset for the system, mirroring `version-lib`):

```nix
bash-version-rev-independent = import ./lib/bash-builders-version-tests.nix { inherit pkgs; };
```

- [ ] **Step 2: Run it, verify it fails**

Run:

```bash
nix build ".#checks.aarch64-darwin.bash-version-rev-independent" --no-link 2>&1 | tail -20
```

Expected: FAIL — "script drvPath depends on repo rev" (because `GIT_HASH="${gitHash}"` is baked into the build phase and `gitHash` is derived from `self.rev`).

- [ ] **Step 3: Switch `mkBashScript` off `gitHash` onto the source digest**

In `lib/bash-builders.nix`, inside `mkBashScript`'s `let` (near `scriptBodyFile`), add:

```nix
      # Per-source content digest: this script's src plus each sourced
      # library's composed-lib store path (l.lib transitively embeds nested
      # libs). Repo-rev independent; see ADR 0006.
      srcDigest =
        (import ./version.nix).mkSrcDigest ([ src ] ++ map (l: l.lib) libraries);
```

In the `buildPhase`, replace:

```nix
          # Compute version: YY.MM.DD.SSSSS+gitHash
          GIT_HASH="${gitHash}"
          SECONDS_TODAY=$(( $(date -u +%s) % 86400 ))
          FULL_VERSION=$(printf "%s.%05d+%s" "$(date -u +%y.%m.%d)" "$SECONDS_TODAY" "$GIT_HASH")
```

with:

```nix
          # Compute version: YY.MM.DD.SSSSS+<srcDigest> (date is build-time and
          # not a derivation input; srcDigest is eval-time and content-driven).
          SRC_DIGEST="${srcDigest}"
          SECONDS_TODAY=$(( $(date -u +%s) % 86400 ))
          FULL_VERSION=$(printf "%s.%05d+%s" "$(date -u +%y.%m.%d)" "$SECONDS_TODAY" "$SRC_DIGEST")
```

Leave the top-level `gitHash = ...` binding (line ~12) in place — it is still exported by the factory for any other consumer — but it is no longer referenced by `mkBashScript`.

- [ ] **Step 4: Run the check, verify it passes**

Run:

```bash
nix build ".#checks.aarch64-darwin.bash-version-rev-independent" --no-link 2>&1 | tail -5
```

Expected: PASS — "OK: script drvPath is rev-independent".

- [ ] **Step 5: Sanity-check transitivity (a library edit must move the digest)**

Run an eval that compares the digest with vs without a library, confirming `l.lib` participates:

```bash
nix eval --impure --expr '
  let pkgs = import <nixpkgs> {};
      v = import ./lib/version.nix;
      lib0 = v.mkSrcDigest [ ./lib/fixtures/demo ];
      lib1 = v.mkSrcDigest [ ./lib/fixtures/demo (pkgs.writeText "x" "lib-content") ];
  in lib0 != lib1
'
```

Expected: `true` (adding a library input changes the digest).

- [ ] **Step 6: Commit**

```bash
git add lib/bash-builders.nix lib/bash-builders-version-tests.nix lib/fixtures/demo/demo.sh flake.nix
git commit -m "feat(bash-builders): version from source digest, not repo rev (ADR 0006)"
```

---

### Task 3: Python builder — `gitHash` → source digest

**Files:**

- Modify: `phillipgreenii-nix-agent-support/lib/python-package.nix`
- Modify: every call site passing `gitHash` to `mkPythonPackage` (found in Task 5)
- Branch: `git checkout -b source-content-digest-versioning` in `phillipgreenii-nix-agent-support`

- [ ] **Step 1: Replace the `gitHash` argument with a derived source digest**

In `lib/python-package.nix`, remove `gitHash,` from the argument set (line ~28). In the `let` block (after `python = pkgs.python3;`), add:

```nix
      # Per-source content digest (repo-rev independent; see nix-repo-base ADR 0006).
      # phillipgreenii-nix-base is available here via the flake's inputs.
      srcDigest = phillipgreenii-nix-base.lib.mkSrcDigest src;
```

> Note: `mkPythonPackage`'s factory must receive `phillipgreenii-nix-base` (or `mkSrcDigest`
> directly). If the factory is constructed as `import ./lib/python-package.nix { inherit pkgs lib; }`,
> extend its argument set to also take `mkSrcDigest` and pass it from the flake. Prefer passing
> `mkSrcDigest` to avoid coupling the helper to a specific input name.

Replace, in `preBuild` (line ~119):

```nix
        BUILD_VERSION=$(printf "%s.%05d+%s" "$(date +%y.%m.%d)" "$SECONDS_TODAY" "${gitHash}")
```

with:

```nix
        BUILD_VERSION=$(printf "%s.%05d+%s" "$(date +%y.%m.%d)" "$SECONDS_TODAY" "${srcDigest}")
```

- [ ] **Step 2: Update the factory signature and flake wiring**

Where the factory is imported (search `python-package.nix` in the agent-support flake), thread `mkSrcDigest`:

```nix
  pythonHelpers = import ./lib/python-package.nix {
    inherit pkgs lib;
    inherit (phillipgreenii-nix-base.lib) mkSrcDigest;
  };
```

and change `python-package.nix`'s arg set head to `{ pkgs, lib, mkSrcDigest }:`, dropping the per-call `gitHash`.

- [ ] **Step 3: Build one python package two ways to prove rev-independence**

Pick a consumer (e.g. `work-activity-tracker`) and verify the package `drvPath` no longer depends on the repo rev. With `gitHash` gone from the build inputs, this holds by construction; verify it builds:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps
nix build ".#packages.aarch64-darwin.work-activity-tracker" --no-link \
  --override-input phillipgreenii-nix-base "git+file:///Users/phillipg/phillipg_mbp/phillipg-nix-repo-base" 2>&1 | tail -5
nix run ".#work-activity-tracker" -- --version  # shows YY.MM.DD.SSSSS+<digest>
```

Expected: builds; `--version` shows the `+<digest>` form.

- [ ] **Step 4: Commit (agent-support)**

```bash
git add lib/python-package.nix flake.nix
git commit -m "feat(python-package): version from source digest, not repo rev (ADR 0006)"
```

---

### Task 4: Go builder — use shared helper; decide on build-time timestamp

**Files:**

- Modify: `lib/go-builders.nix` (line ~129)

- [ ] **Step 1: Refactor the inline digest onto `mkSrcDigest`**

In `lib/go-builders.nix`, replace:

```nix
      version = "${baseVersion}-${builtins.substring 0 8 (builtins.hashString "sha256" "${src}")}";
```

with:

```nix
      version = "${baseVersion}-${(import ./version.nix).mkSrcDigest src}";
```

This is behavior-preserving for a single `src` (identical digest) and gains multi-path support (`src` may now be a list).

- [ ] **Step 2: Verify Go packages still build with identical version**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix build ".#packages.aarch64-darwin.pn" --no-link 2>&1 | tail -5   # if exposed; else build a consumer
```

Expected: PASS, `--version` unchanged in shape (`<baseVersion>-<digest>`).

- [ ] **Step 3: (Decision) build-time timestamp for Go**

Per the spec, Go cross-language timestamp parity needs a `preBuild`-exported `buildDate` referenced by an ldflag AND a second `main.Date` var in each consumer's `main.go`. **Default: defer this.** Go `--version` already meets every invariant via the digest. If parity is desired, open a follow-up task; do not block this plan on consumer `main.go` edits.

- [ ] **Step 4: Commit**

```bash
git add lib/go-builders.nix
git commit -m "refactor(go-builders): use shared mkSrcDigest helper (ADR 0006)"
```

---

### Task 5: Consumer call-site cleanup (remove `inherit gitHash;`)

**Files (find exact sites first):**

- [ ] **Step 1: Find every `gitHash` thread into a builder**

```bash
cd /Users/phillipg/phillipg_mbp
rg -n 'inherit gitHash|gitHash =|gitHash;' --glob '*.nix' \
  phillipgreenii-nix-support-apps phillipgreenii-nix-agent-support \
  phillipg-nix-ziprecruiter phillipgreenii-nix-personal | grep -v '/\.git/'
```

Expected hits include `support-apps/flake.nix` (`work-activity-tracker`, `pd-schedule-manager`, `jsonl-log-parser` `{ inherit gitHash; }`) and the agent-support python call sites.

- [ ] **Step 2: Remove `{ inherit gitHash; }` from each `callPackage`/`mkPythonPackage` call**

For each package that no longer needs it (bash/python/go now self-derive), drop the `gitHash` argument. Keep the flake-level `gitHash = mkGitHash (...)` binding **only** if still used by the repo-meta module; otherwise remove it too. Do NOT touch `mkInstallMetadata`/`mkVersion` (repo-meta keeps the rev).

- [ ] **Step 3: `nix flake check` each touched repo**

```bash
for r in phillipgreenii-nix-support-apps phillipgreenii-nix-agent-support; do
  (cd "$r" && nix flake check \
    --override-input phillipgreenii-nix-base "git+file:///Users/phillipg/phillipg_mbp/phillipg-nix-repo-base" 2>&1 | tail -5)
done
```

Expected: PASS.

- [ ] **Step 4: Commit each repo**

```bash
git add flake.nix && git commit -m "refactor: drop gitHash threading; builders self-derive version (ADR 0006)"
```

---

### Task 6: Agent-facing documentation

**Files:**

- Create: `docs/adr/index.md` (covering 0000–0006; none exists today)
- Modify: bash-scripting skill reference (the authoritative `mkBash*` doc)
- Modify: `CLAUDE.md` files that mention versioning (nix-repo-base, agent-support, ziprecruiter)
- Modify: inline doc-comments in `lib/version.nix`, `lib/bash-builders.nix`, `lib/go-builders.nix`, `lib/python-package.nix`

- [ ] **Step 1: Create `docs/adr/index.md`**

A markdown table listing ADRs 0000–0006 with one-line summaries and links. Mark 0005 "version contract superseded by 0006".

- [ ] **Step 2: Add the convention to the bash-scripting skill + CLAUDE.md**

Add a short "Versioning" subsection stating the rule verbatim:

- Custom artifact version = `YY.MM.DD.SSSSS+<srcdigest8>`; digest from the unit's own source via `mkSrcDigest` (never `self.rev`).
- Repo-meta module is the only `self.rev` consumer.
- Third-party deps bump only via `update-locks.sh`.
- Link ADR 0006.

- [ ] **Step 3: Update inline doc-comments** in the four builder files to reference ADR 0006 and `mkSrcDigest`.

- [ ] **Step 4: Commit**

```bash
git add docs/adr/index.md CLAUDE.md lib/*.nix
git commit -m "docs: document per-source versioning convention (ADR 0006)"
```

---

### Task 7: Integration verification (the falsifiable end-state)

**Files:** none (verification only).

- [ ] **Step 1: Re-run the dry-run on the unchanged tree — expect 0 custom rebuilds**

Re-run the Task 0 Step 1 command (with all five overrides). Compare against `/tmp/versioning-baseline.txt`:

```bash
grep -c '\.drv$' /tmp/versioning-after.txt   # vs baseline
```

Expected: only the 5 `*-install-metadata` derivations (repo-meta) remain; all bash/python/go artifacts are cache hits (0).

- [ ] **Step 2: Prove single-unit isolation**

Edit one script's `.sh` body, re-run the dry-run. Expected: ONLY that script (+ its man page; + any sibling sharing a sourced library) rebuilds — not the whole tier.

- [ ] **Step 3: Prove unrelated-commit isolation**

Commit an unrelated doc change in one repo (HEAD moves), re-run the dry-run with overrides. Expected: only the 5 repo-meta files rebuild; no custom artifacts.

- [ ] **Step 4: Record results on the tracking issue**

```bash
bd update pg2-xx4g --notes="VERIFIED: dry-run before=<N> custom rebuilds, after=0 (only 5 repo-meta). Single-edit isolation + unrelated-commit isolation confirmed."
```

---

## Self-review notes

- **Spec coverage:** mkSrcDigest (Task 1), bash (Task 2), python (Task 3), go (Task 4), call-site cleanup (Task 5), repo-meta-unchanged (asserted, not modified), docs+ADR index (Task 6), the falsifiable "84→0" verification (Tasks 0+7). fmt/neovim explicitly out of scope (separate plans).
- **Type/name consistency:** helper is `mkSrcDigest` everywhere; bash uses `srcDigest`/`SRC_DIGEST`; python uses `srcDigest`; go inlines `mkSrcDigest src`.
- **Known under-specified spots (intentional, flagged for the implementer):** Task 3 Step 2 (exact factory-wiring of `mkSrcDigest` into the agent-support python helper) and Task 6 Step 2 (exact skill/CLAUDE.md file paths) depend on details the implementer confirms in-repo; both have concrete instructions, not placeholders. Go build-time timestamp is deferred by decision (Task 4 Step 3).
