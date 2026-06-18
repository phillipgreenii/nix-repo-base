# flake-parts Modular Producer — Design

**Status:** Draft
**Date:** 2026-06-18
**Beads:** tc-8rzk6 (P2), tc-henah (P2), tc-qcqwu (P3), tc-66t7y (P3 — closes as absorbed)
**Worktree:** `/home/tcadmin/workspace/nix-repo-base-flake-parts` on `docs/flake-parts-modular-spec`
**Base:** `main` @ `43d3468`

## 1. Goal

Modernize the nix-repo-base producer surface so the four direct heavy inputs
(`nixpkgs-unstable`, `llm-agents`, `flox`, `nix-vscode-extensions`) and their
transitive bloat (`fenix`, `crane`, `bun2nix`, `blueprint`, `rust-analyzer-src`,
nixpkgs duplicates, …) leave nix-repo-base's flake graph; reshape the producer's
shared behavior as importable flake-parts modules; and formalize the
`update-locks-lib.bash` contract that nix-overlay's verification scripts depend
on.

Consumer flakes inherit none of the dropped bloat in their own locks. Producer
behavior is consumed via `imports = [ phillipgreenii-nix-base.flakeModules.* ];`
instead of stitched-in `lib.mk*` calls.

Single producer-side chunk; consumer-side migrations (nix-overlay, nix-personal,
homelab) are tracked as separate follow-up beads created when this chunk lands.

## 2. Background

The 2026-06-12 nix-overlay deepdive (`/home/tcadmin/workspace/nix-overlay/2026-06-12-nix-overlay-deepdive.md`)
identified four findings that all trace back to nix-repo-base's producer-side
shape:

- **§A4 (Transitive input bloat from `nix-repo-base`)** — nix-overlay's
  `flake.lock` carries 26 nodes despite only directly needing ~6, because
  `phillipgreenii-nix-base` pulls `flox`/`fenix`/`crane`/`llm-agents`/etc.
  through its own inputs. Consumers cannot fix this by `follows`-rewriting;
  the heavy inputs must move out of nix-repo-base.
- **§A6 (Heavy, unversioned coupling to `nix-repo-base` lib)** — five lib
  surfaces (`mkChecks`, `mkPreCommitHooks`, `mkDevShell`, `mkInstallMetadata`,
  `update-locks-lib.bash`) with no version contract; a refactor breaks
  consumers' eval, checks, devShell, AND nightly updater simultaneously.
- **§M3 (flake-parts migration)** — `flake-parts` is already in nix-overlay's
  lock transitively; moving consumers to a `perSystem { ... }` model eliminates
  classes of bugs (B3) and gives nix-repo-base a natural home for shipping
  importable modules.
- **Chunk 6 fallout (`tc-0ixb2` closure)** — nix-overlay's `verify-provenance.sh`
  leans on undocumented `ul_*` invariants (clean-tree gate,
  `git reset --hard HEAD~1` rollback, signal traps) and references
  `update-locks-lib.bash` by line numbers, creating brittle cross-repo
  coupling.

Additional context picked up during brainstorming:

- nix-repo-base's `mkGoBuilders` was made gomod2nix-only by ADR 0008
  (commits `5df0d45`, `b64c7ff`, `43d3468`, post-prompt). `gomod2nix` is now
  a direct input of nix-repo-base because internal `pn` builds need it. It is
  NOT in the "drop" set — it stays.
- Workspace grep shows only nix-personal consumes the `mk*Overlay` factories
  today (`mkUnstableOverlay`, `mkLlmAgentsOverlay`, `mkVscodeExtensionsOverlay`).
  `mkFloxOverlay` has no grep-confirmed workspace consumer. Per
  [feedback-grep-not-canonical-consumers](../../../../../../.claude/projects/-home-tcadmin-workspace/memory/feedback-grep-not-canonical-consumers.md):
  workspace grep is a snapshot, not an enumeration. Out-of-workspace consumers
  may exist; more homelab migration is planned. Design assumes the producer
  API must be preserved (in shape), not vendor-and-deleted.

## 3. Decisions

### 3.1 Standardize on flake-parts

nix-repo-base's `flake.nix` migrates from `flake-utils.lib.eachDefaultSystem` to
`flake-parts.lib.mkFlake`. Rationale:

- Output schema enforcement at eval time (the deepdive B3 yaziPlugins bug is
  impossible to write under `perSystem.checks`/`packages`).
- Module composition — `imports = [ ... ]` is how a library flake ships
  behavior; the alternative (lib factories stitched into per-system outputs by
  hand) is what we have today and is brittle (§A6).
- `inputs'` auto-narrowing eliminates manual `inputs.foo.packages.${system}`
  boilerplate.
- Already in every consumer's lock transitively per deepdive §M3.

`flake-parts` becomes a direct input of nix-repo-base. `flake-utils` is dropped.

### 3.2 API split — modules where they fit, lib for the rest

The producer surface splits along whether the API is naturally a flake-output
contributor or a function called from non-perSystem contexts.

**Becomes a flake module (9 total):**

| Module | Contributes | Heavy upstream consumer declares |
|---|---|---|
| `flakeModules.checks` | `perSystem.checks.*` + `config.phillipgreenii.checks.helpers.*` | — |
| `flakeModules.pre-commit` | `perSystem.checks.pre-commit` + `perSystem.packages.install-pre-commit-hooks` | — |
| `flakeModules.devshell` | `perSystem.devShells.default` | — |
| `flakeModules.treefmt` | `perSystem.formatter` + `perSystem.config.treefmt.*` | — |
| `flakeModules.unstable-overlay` | `flake.overlays.unstable` | `nixpkgs-unstable` |
| `flakeModules.llm-agents-overlay` | `flake.overlays.llm-agents` | `llm-agents` |
| `flakeModules.vscode-extensions-overlay` | `flake.overlays.vscode-extensions` | `nix-vscode-extensions` |
| `flakeModules.flox-overlay` | `flake.overlays.flox` | `flox` |
| `flakeModules.gomod2nix-overlay` | `flake.overlays.gomod2nix` | — (nix-repo-base owns) |

**Becomes a configurable HM module (1):**

`homeModules.install-metadata` — Shape B. The module declares
`options.phillipgreenii.install-metadata.{flakeSelf, name}` (both `mkOption`
of appropriate types). Consumer imports the module into their home-manager
config AND sets the options. Replaces the `mkInstallMetadata { flakeSelf, name }`
factory function.

**Stays as a lib function (10):**

Three categories, all unchanged in shape (just survive the cutover):

*Builder factories — called from overlay contexts (e.g. nix-personal's
`mkCmuxScriptsOverlay` reaches `mkBashBuilders` from inside `overlays.default`);
module config is not in scope there. Lib is the universal API.*

- `lib.mkBashBuilders` — unchanged.
- `lib.mkGoBuilders` — adds a runtime assertion `assert pkgs ? buildGoApplication;`
  with an error message pointing the caller at `flakeModules.gomod2nix-overlay`
  + `inputs.gomod2nix`. Otherwise unchanged.
- `lib.mkManPage` — unchanged.

*Pure version helpers — used inside package derivations and HM modules; not
flake-output-shaped.*

- `lib.mkGitHash` — unchanged.
- `lib.mkVersion` — unchanged.
- `lib.mkSrcDigest` — unchanged.

*HM/NixOS module factories — return modules consumed inside another module's
`imports` list. Same shape rationale as the builders.*

- `lib.mkSimplePackageModule` — unchanged.
- `lib.mkEnableablePackageModule` — unchanged.
- `lib.mkDockRegistration` — unchanged.
- `lib.mkProgramModule` — unchanged.

**Stays as bash (1):**

`lib/scripts/update-locks-lib.bash` — gains a CONTRACT block (§3.7) but its
behavior, exports, and integration tests (`test-update-locks-lib` check) are
unchanged.

### 3.3 Light vs heavy upstreams: input ownership and follows discipline

Each flake module needs upstream inputs. Two ownership patterns:

- **Light upstream** (small, stable, single canonical pin): nix-repo-base owns
  the input. The module closes over nix-repo-base's own `inputs` at
  module-definition time. Consumers do not declare these directly; they appear
  as transitive nodes in the consumer's lock (one each, after follows). Examples:
  `git-hooks`, `treefmt-nix`, `gomod2nix`, `nixpkgs` (the producer's
  canonical, typically `follows`-overridden).

- **Heavy upstream** (large flake graphs, often want consumer-controlled
  revisions): consumer owns the input. The module reads it from the consumer's
  `inputs` via the standard flake-parts `{ inputs, ... }:` arg. Without a
  consumer declaration, evaluation fails with a clear error. Examples:
  `nixpkgs-unstable`, `llm-agents`, `flox`, `nix-vscode-extensions`.

This is the core mechanism that drops nix-repo-base's lock graph: heavy
upstreams are no longer declared by nix-repo-base.

### 3.4 Inputs before / after

**Before (nix-repo-base/flake.nix):**

```
nixpkgs, nixpkgs-unstable, llm-agents, flox, nix-vscode-extensions,
flake-utils, git-hooks, treefmt-nix, gomod2nix
```

**After (nix-repo-base/flake.nix):**

```
nixpkgs, flake-parts, git-hooks, treefmt-nix, gomod2nix
```

Dropped: `nixpkgs-unstable`, `llm-agents`, `flox`, `nix-vscode-extensions`,
`flake-utils`. Added: `flake-parts`.

Expected nix-repo-base `flake.lock` node count: ~6-8 (was ~25). Verified by
counting nodes in `flake.lock` after the migration; CI check (§5) enforces.

### 3.5 Hard cutover — no compat shims

The producer rev that lands the modules also deletes the lib functions they
replace:

**Deleted in same producer rev:**

`lib.mkChecks`, `lib.mkPreCommitHooks`, `lib.mkDevShell`, `lib.mkTreefmtConfig`,
`lib.mkInstallMetadata`, `lib.mkUnstableOverlay`, `lib.mkLlmAgentsOverlay`,
`lib.mkVscodeExtensionsOverlay`, `lib.mkFloxOverlay`.

Consumers must migrate at the same time they update the nix-repo-base pin.
Operating at HEAD with no formal releases, "lock-step migration" is just
"coordinated update across 4 repos." Carrying a deprecation layer that nobody
will read provides no value; the principle is recorded in
[feedback-pin-is-the-version](../../../../../../.claude/projects/-home-tcadmin-workspace/memory/feedback-pin-is-the-version.md).

### 3.6 Consumer alignment story

Each module that needs a heavy upstream declares
`config.phillipgreenii.alignment.requires = [ "<input-name>" ];`. The README
includes a copy-pasteable snippet documenting the pattern. A `nix flake check`
derivation (§5) catches misconfiguration.

The pattern at the consuming flake:

```nix
# Top-level consumer (e.g. Repo A) declares heavy inputs once.
inputs = {
  nixpkgs.url = "...";
  nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  llm-agents.url = "github:numtide/llm-agents.nix";
  nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";

  phillipgreenii-nix-base = {
    url = "github:phillipgreenii/nix-repo-base";
    inputs.nixpkgs.follows = "nixpkgs";
  };

  # Any DOWNSTREAM flake that ALSO uses these heavy inputs needs follows.
  nix-personal = {
    url = "github:phillipgreenii/nix-personal";
    inputs = {
      nixpkgs.follows = "nixpkgs";
      nixpkgs-unstable.follows = "nixpkgs-unstable";
      llm-agents.follows = "llm-agents";
      nix-vscode-extensions.follows = "nix-vscode-extensions";
      phillipgreenii-nix-base.follows = "phillipgreenii-nix-base";
    };
  };
};
```

Without the per-downstream follows, the consumer's lock contains
`nixpkgs-unstable` AND `nixpkgs-unstable_2`. Each downstream's overlay uses its
own pin; the last-applied wins. Catastrophic mode: silent divergence in
`pkgs.unstable` across the consumer's machines.

### 3.7 update-locks-lib.bash CONTRACT (tc-qcqwu)

Add a top-of-file CONTRACT block to `lib/scripts/update-locks-lib.bash`
documenting:

- **`ul_setup <project-name> <script-dir>`** —
  - Disables `core.fsmonitor` (re-enabled on EXIT/INT/TERM via trap).
  - Reconciles `.pre-commit-config.yaml` symlink BEFORE the clean-tree gate (so
    a stale symlink left by a prior `nix flake update` self-heals instead of
    tripping the gate).
  - Asserts the working tree is clean; exits 1 on dirty.
  - Arms the full cleanup trap (rollback-on-failure) only AFTER the clean-tree
    assertion passes.
- **`ul_run_step <step-name> <commit-msg> <cmd…>`** —
  - Skips if the step's per-window stamp is current (cache hit).
  - Asserts clean tree before invoking the child command.
  - On exit 0: runs `nix fmt`, stages all, writes stamp, commits as one
    commit (`<commit-msg>`). On a no-op exit 0 (no content change): writes
    stamp, commits stamp-only.
  - On exit `$UL_RC_ATTEMPTED` (75): rolls back content (`git reset --hard
    HEAD; git clean -fd`), writes + commits stamp-only with a "deferred"
    message.
  - On any other non-zero exit: rolls back content, records failure, does NOT
    commit.
- **`ul_reexec_in_dev_shell`** —
  - If `IN_NIX_SHELL` set: no-op, returns 0.
  - Otherwise: re-execs `$0 "$@"` inside `nix develop` for the script's
    directory. On nix-develop entry failure: warns, returns 0 (caller can run
    with host tooling).
  - Propagates `UL_LIB_DIR` so the in-shell re-run reuses the resolved lib.
- **`ul_finalize`** —
  - Prints run summary (ran / passed / upgraded / deferred / failed / skipped).
  - Exits 0 if no failed steps; exits 1 listing failed steps otherwise.

Replace nix-overlay's hard-coded cross-repo references
(`update-locks.sh:32-39`, `update-locks-lib.bash:35,262`) with named anchors in
the CONTRACT block (e.g., `# ANCHOR: self-repair-nrb-rev-fallback`,
`# ANCHOR: clean-tree-gate`). nix-overlay's comments switch to referencing
anchors rather than line numbers.

No `UL_LIB_VERSION` constant, no runtime version-guards, no new tests. Existing
`test-update-locks-lib` check stays.

## 4. Producer file layout (target)

```
nix-repo-base/
├── flake.nix                          # flake-parts.lib.mkFlake; imports own modules
├── flake-modules/                     # NEW directory
│   ├── checks.nix                     # flakeModules.checks
│   ├── pre-commit.nix                 # flakeModules.pre-commit
│   ├── devshell.nix                   # flakeModules.devshell
│   ├── treefmt.nix                    # flakeModules.treefmt
│   ├── overlays/
│   │   ├── unstable.nix               # flakeModules.unstable-overlay
│   │   ├── llm-agents.nix             # flakeModules.llm-agents-overlay
│   │   ├── vscode-extensions.nix      # flakeModules.vscode-extensions-overlay
│   │   ├── flox.nix                   # flakeModules.flox-overlay
│   │   └── gomod2nix.nix              # flakeModules.gomod2nix-overlay
│   └── alignment.nix                  # contributes the alignment check + option
├── home-modules/                      # NEW directory (replaces ad-hoc lib export)
│   └── install-metadata.nix           # homeModules.install-metadata (Shape B)
├── lib/
│   ├── bash-builders.nix              # unchanged (mkBashBuilders)
│   ├── go-builders.nix                # add runtime assert; otherwise unchanged
│   ├── version.nix                    # EDIT — keep mkGitHash/mkVersion/mkSrcDigest; DROP mkInstallMetadata
│   │                                  #   (hard cutover per §3.5; install-metadata becomes the Shape B HM module)
│   └── scripts/
│       └── update-locks-lib.bash      # add CONTRACT block at top
├── nix/
│   ├── dev-env.nix                    # DELETED (logic moves into flake-modules/*)
│   ├── checks.nix                     # DELETED (logic moves into flake-modules/checks.nix)
│   ├── packages.nix                   # UNCHANGED — backs mkBashBuilders/mkGoBuilders/mkManPage
│   └── module-helpers.nix             # UNCHANGED — backs mkSimplePackageModule + friends
├── README.md                          # NEW or updated: consumer alignment section
└── modules/pn/                        # unchanged; nix-repo-base's own use of mkGoApp
```

## 5. Lint check — `consumer-input-alignment`

Shipped via `flakeModules.checks` so every consumer of nix-repo-base gets it
automatically under `nix flake check`. Reads:

- `config.phillipgreenii.alignment.requires` (a list of input names that the
  imported modules declared they need)
- The consumer's `flake.lock` (via `self.inputs.<name>.sourceInfo` /
  `builtins.readFile flake.lock`)

For each required input name, asserts:

1. The input exists at the top level of `flake.lock` `.nodes.root.inputs.<name>`.
2. No `<name>_<N>` (N ≥ 2) sibling exists (signals missing `follows` on a
   downstream flake).
3. If multiple flakes in the lock declare an input with the same name, all
   resolve to the same locked rev (cross-checked via the `.locked.rev` field).

Failures emit an actionable message naming the missing/divergent input AND the
downstream flake responsible. Example:

```
error: nix-personal declares inputs.nixpkgs-unstable but your flake.lock
shows nixpkgs-unstable (rev: abc123) AND nixpkgs-unstable_2 (rev: def456).
Add `nix-personal.inputs.nixpkgs-unstable.follows = "nixpkgs-unstable"`
to your flake.nix.
```

The check derivation is lightweight (`pkgs.runCommand` + `jq` over the
consumer's flake.lock string).

## 6. README updates

`README.md` gains a top-level section documenting:

1. **What nix-repo-base provides** — table mirroring §3.2 (9 flake modules +
   1 HM module + 3 lib functions + 1 bash script).
2. **Minimum consumer wiring** — declare `inputs.flake-parts` +
   `inputs.phillipgreenii-nix-base`; `flake-parts.lib.mkFlake { … }`;
   `imports = [ phillipgreenii-nix-base.flakeModules.<x> ];`.
3. **Heavy input ownership** — which modules need which consumer-declared
   inputs (the §3.6 pattern), with the snippet from §3.6.
4. **Cross-flake alignment** — the `follows`-per-downstream pattern + the
   one-line `jq` verification command.
5. **Migration from the pre-chunk API** — a deletion table mapping
   `lib.mkChecks` → `flakeModules.checks` etc.; a `lib.mkInstallMetadata { … }`
   → `imports = [ homeModules.install-metadata ]` + options example.

## 7. Acceptance criteria

The chunk is complete when ALL of the following pass:

1. **nix-repo-base's `flake.lock` has ≤ 8 nodes** (target: 6).
2. **`nix flake check` passes** on nix-repo-base, including the new
   `consumer-input-alignment` check (which is a no-op when the consumer is
   nix-repo-base itself since the modules' `requires` list is empty for the
   non-overlay modules; the overlay modules' `requires` are checked by the
   `ci-test`-equivalent consumer fixture, see point 5).
3. **`nix flake show` returns** the expected top-level outputs:
   - `flakeModules.{checks, pre-commit, devshell, treefmt, unstable-overlay,
     llm-agents-overlay, vscode-extensions-overlay, flox-overlay,
     gomod2nix-overlay}`
   - `homeModules.{pn, install-metadata}` (`pn` = existing aggregate;
     `install-metadata` = the new Shape B configurable module)
   - `darwinModules.default`
   - `lib.{mkBashBuilders, mkGoBuilders, mkManPage, mkGitHash, mkVersion,
     mkSrcDigest, mkSimplePackageModule, mkEnableablePackageModule,
     mkDockRegistration, mkProgramModule}` (NOT: `mkChecks`, `mkPreCommitHooks`,
     `mkDevShell`, `mkTreefmtConfig`, `mkInstallMetadata`, `mkUnstableOverlay`,
     `mkLlmAgentsOverlay`, `mkVscodeExtensionsOverlay`, `mkFloxOverlay`)
   - `packages.{install-pre-commit-hooks, update-locks-lib, determine-ul-lib-dir,
     pn, fix-lint}` (now contributed by modules)
4. **All deleted lib symbols are absent** from `nix eval .#lib --apply
   'lib: builtins.attrNames lib'`.
5. **A consumer-fixture flake under `tests/consumer-fixture/`** evaluates
   cleanly: imports the 9 flake modules + 1 HM module, declares the 4 heavy
   inputs, exercises `lib.mkBashBuilders`/`mkGoBuilders`/`mkManPage` from
   overlay context, exercises the `homeModules.install-metadata` options. Built
   under `nix flake check` of the fixture. (Replaces the previous "we'll find
   out when consumers migrate" pattern.)
6. **`update-locks-lib.bash` CONTRACT block** is in place; `test-update-locks-lib`
   check still passes (semantics unchanged).
7. **nix-overlay's `update-locks.sh` and `verify-provenance.sh`** are
   re-pointed to anchor names instead of line numbers — done as a tiny
   follow-up in nix-overlay's repo, NOT in this chunk's worktree; tracked as a
   bead under the consumer-migration epic. The CONTRACT anchors are stable in
   nix-repo-base regardless.
8. **`README.md` documents the consumer wiring + follows pattern** (§6).

## 8. Out of scope

- **Consumer migrations**: nix-overlay (related to in-flight tc-zt0hh M3),
  nix-personal (flake-utils → flake-parts), and homelab (already on
  flake-parts; swaps `nixBaseLib.mk*` lib calls for module imports) are
  separate beads created when this chunk lands.
- **`flake-parts-website`-style declarative HM/darwin configuration**: out of
  scope. nix-repo-base does not declare configurations; it ships modules.
- **`UL_LIB_VERSION` / runtime version-guards on internal APIs**: explicitly
  rejected per
  [feedback-pin-is-the-version](../../../../../../.claude/projects/-home-tcadmin-workspace/memory/feedback-pin-is-the-version.md).
- **Rename of `phillipgreenii-nix-base` input**: keep current name. Cosmetic
  rename on top of the existing migration churn provides no functional
  benefit.
- **Tag releases for nix-repo-base**: not happening. Consumed at HEAD.
- **Backwards-compat shims for the deleted lib functions**: hard cutover by
  decision §3.5.
- **Splitting nix-repo-base into multiple flakes** (the "slim + fat overlays"
  alternative considered during brainstorming): rejected. The flake-parts
  module pattern serves the same goal (consumers only pay for what they
  import) without the maintenance cost of two flakes.

## 9. Consumer migration follow-ups (NEW beads to create when this chunk merges)

These are NOT in scope for the producer chunk but must be tracked when this
ships so consumers don't remain broken on the next pull:

- **nix-overlay flake-parts migration** — relates to in-flight tc-zt0hh M3.
  Likely supersedes or absorbs that bead. Migrates nix-overlay's flake.nix off
  flake-utils, imports the new modules, drops `phillipgreenii-nix-base.lib.*`
  call sites.
- **nix-personal flake-parts migration** — bigger lift than nix-overlay because
  nix-personal has many module-using outputs. Declares the 4 heavy inputs at
  top level, imports the 8 perSystem + overlay modules + 1 HM module, adds
  follows discipline.
- **homelab module-import migration** — homelab already uses flake-parts.
  Swaps the `nixBaseLib = inputs.phillipgreenii-nix-base.lib;` indirection for
  `imports = [ phillipgreenii-nix-base.flakeModules.{treefmt, pre-commit,
  devshell} ];`. Smallest of the three consumer migrations.
- **nix-overlay anchor switch** — when the CONTRACT block ships, update
  nix-overlay's `update-locks.sh` and `verify-provenance.sh` to reference
  anchor names instead of line numbers (tiny doc change).

## 10. Beads disposition when chunk lands

- **tc-8rzk6** — closes. Heavy inputs dropped; nix-repo-base's lock shrinks
  from ~25 to ~6 nodes. Consumer-side pruning (tc-rzgzq for nix-overlay)
  unblocks.
- **tc-henah** — closes. 9 flake modules + 1 configurable HM module shipped.
  Consumer migration (tc-zt0hh for nix-overlay M3) unblocks.
- **tc-qcqwu** — closes. CONTRACT block shipped; named anchors replace
  cross-repo line references.
- **tc-66t7y** — closes as "addressed by chunk architecture." Tag-releases arm
  is dead (no releases). Vendor-small-pieces arm is superseded (modules
  provide cleaner reuse). Module-metadata + pin-as-version arm is what the
  chunk delivers.
