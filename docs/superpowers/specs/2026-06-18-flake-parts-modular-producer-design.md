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

| Module | Contributes | Imports (implicit) | Consumer-declared input |
|---|---|---|---|
| `flakeModules.checks` | `perSystem.checks.{formatting,linting,consumer-input-alignment}` + `config.phillipgreenii.checks.helpers.*` | — | — |
| `flakeModules.pre-commit` | `perSystem.checks.pre-commit` + `perSystem.packages.install-pre-commit-hooks` | `flakeModules.treefmt` | — |
| `flakeModules.devshell` | `perSystem.devShells.default` | — | — |
| `flakeModules.treefmt` | `perSystem.formatter` + `perSystem.treefmt.*` (treefmt-nix's standard option tree) | — | — |
| `flakeModules.unstable-overlay` | `flake.overlays.unstable` | — | `nixpkgs-unstable` |
| `flakeModules.llm-agents-overlay` | `flake.overlays.llm-agents` | — | `llm-agents` |
| `flakeModules.vscode-extensions-overlay` | `flake.overlays.vscode-extensions` | — | `nix-vscode-extensions` |
| `flakeModules.flox-overlay` | `flake.overlays.flox` | — | `flox` |
| `flakeModules.gomod2nix-overlay` | `flake.overlays.gomod2nix` | — | — (light upstream, see §3.3) |

`flakeModules.pre-commit` IMPORTS `flakeModules.treefmt` implicitly because
pre-commit needs the formatter wrapper. Consumers who import pre-commit do
NOT need to also import treefmt — it composes in automatically. (Consumers
who want treefmt WITHOUT pre-commit import treefmt alone.)

**Consumer source root options default to `inputs.self`.** Both
`phillipgreenii.src` (used by formatting + linting) and
`phillipgreenii.pre-commit.src` (used by git-hooks for hook registration)
default to the consumer's flake root (i.e., `inputs.self.outPath`). Consumers
who want subdirectory scoping for formatting/linting can override
`phillipgreenii.src` to a path; `pre-commit.src` rarely needs overriding.
This removes the duplicate-config foot-gun where every consumer had to set
both options to `./.` redundantly.

`flakeModules.checks.helpers` exposes the following per-system functions
(identical signatures to today's `lib.mkChecks pkgs`):

- `helpers.formatting :: root -> derivation`
- `helpers.linting :: root -> derivation`
- `helpers.shellcheck :: { scripts, exclude ? [], allowWarnings ? false } -> derivation`
- `helpers.testBashScripts :: { package, tests, extraInputs ? [] } -> derivation`
- `helpers.testPythonProject :: { package, src, name, checkLibDir ? null } -> derivation`
- `helpers.testUpdateLocksLib :: { testsDir ? ../lib/tests, scriptsDir ? ../lib/scripts } -> derivation` — the `..` defaults resolve relative to `flake-modules/checks.nix` (i.e., to nix-repo-base's own `lib/tests` and `lib/scripts`). Override if you vendor the module elsewhere.

The module auto-contributes `perSystem.checks.formatting = helpers.formatting
config.phillipgreenii.src` and `perSystem.checks.linting = helpers.linting
config.phillipgreenii.src` when `config.phillipgreenii.src` is set
(`mkOption { type = lib.types.path; }`, no default). The
`consumer-input-alignment` check (§5) is also auto-contributed unconditionally.
All other checks (shellcheck, the test* helpers) are opt-in: consumer wires
them by hand in `perSystem.checks.<name> = config.phillipgreenii.checks.helpers.<name> {...}`.

**Becomes a configurable HM module (1):**

`homeModules.install-metadata` — Shape B. The module declares
`options.phillipgreenii.install-metadata.{flakeSelf, name}` (both `mkOption`
of appropriate types: `flakeSelf` is `lib.types.attrs` for the consumer's
`self`; `name` is `lib.types.str`, no default). Consumer imports the module
into their home-manager config AND sets the options. Replaces the
`mkInstallMetadata { flakeSelf, name }` factory function.

Consumers that re-export their own `homeModules.install-metadata` for
downstream flakes (verified today: nix-overlay:109, nix-personal:362) do so
by wrapping the producer's module in an inline attrset:

```nix
# In consumer flake.nix (top-level, not perSystem)
homeModules.install-metadata = { ... }: {
  imports = [ inputs.phillipgreenii-nix-base.homeModules.install-metadata ];
  phillipgreenii.install-metadata = {
    flakeSelf = self;            # CONSUMER's self
    name = "phillipgreenii-nix-personal";
  };
};
```

The wrapper is one HM module that imports + configures, exported as the
consumer's own `homeModules.install-metadata`. Downstream consumers
(machines, etc.) import THE CONSUMER's `homeModules.install-metadata` and get
the configured behavior with no further options to set.

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

**Existing top-level outputs preserved (unchanged behavior):**

- `overlays.default` — keeps surfacing `pn` to consumers as today
  (`flake.nix:199` of the pre-chunk repo). The new `flake.overlays.<name>`
  outputs from the 5 overlay modules are SIBLINGS, not replacements. A consumer
  applies them by listing whichever they want:
  `nixpkgs.overlays = [ phillipgreenii-nix-base.overlays.default
  phillipgreenii-nix-base.overlays.unstable ... ];`
- `darwinModules.default` — unchanged (the aggregate carrying the pn darwin
  module).
- `homeModules.pn` — unchanged (the pn home-manager module).
- `packages.<system>.{pn, update-locks-lib, determine-ul-lib-dir, fix-lint}`
  — STAY as direct `flake.packages.<system>.<name>` outputs declared in
  nix-repo-base's own `flake.nix` (not contributed by any module). These are
  nix-repo-base's own concerns, not behavior shipped TO consumers.
  `install-pre-commit-hooks` MOVES to `flakeModules.pre-commit` so consumers
  who import that module get it under their own `packages.<system>`.

### 3.3 Light vs heavy upstreams: input ownership and follows discipline

Each flake module needs upstream inputs. Two ownership patterns, distinguished
by which `inputs` closure the module uses:

- **Light upstream** (small, stable, single canonical pin): nix-repo-base owns
  the input. The module file is written as a *function that takes
  nix-repo-base's own `inputs` as a parameter* and returns the flake-parts
  module. Nix-repo-base's `flake.nix` calls this function with
  `nix-repo-base-self.inputs.<name>` baked in. The resulting module is what's
  exported as `flakeModules.<name>`. Consumers do not declare these inputs
  directly; they appear as transitive nodes in the consumer's lock (one each,
  after follows). Examples: `git-hooks`, `treefmt-nix`, `gomod2nix`, `nixpkgs`
  (the producer's canonical, typically `follows`-overridden).

  Pattern:
  ```nix
  # flake-modules/treefmt.nix
  producerInputs:                # <- closed over at producer-export time
  { ... }: {
    perSystem = { pkgs, ... }: {
      treefmt = {                # uses producerInputs.treefmt-nix
        # producerInputs.treefmt-nix.lib.evalModule …
      };
    };
  }
  # flake.nix
  flakeModules.treefmt = import ./flake-modules/treefmt.nix self.inputs;
  ```

- **Heavy upstream** (large flake graphs, want consumer-controlled
  revisions): consumer owns the input. The module reads it from the
  CONSUMER's `inputs` via the standard flake-parts `{ inputs, ... }:` arg.
  Without a consumer declaration, evaluation fails with a clear error.
  Examples: `nixpkgs-unstable`, `llm-agents`, `flox`, `nix-vscode-extensions`.

  Pattern:
  ```nix
  # flake-modules/overlays/unstable.nix
  { inputs, ... }: {            # <- inputs HERE is the consumer's inputs
    flake.overlays.unstable = final: _prev: {
      unstable = import inputs.nixpkgs-unstable {
        inherit (final.stdenv.hostPlatform) system;
        config.allowUnfree = true;
      };
    };
    config.phillipgreenii.alignment.requires = [ "nixpkgs-unstable" ];
  }
  # flake.nix
  flakeModules.unstable-overlay = ./flake-modules/overlays/unstable.nix;
  ```

  Critical: the overlay function (`final: _prev: { unstable = ...; }`) closes
  over the `inputs` it received — i.e., the CONSUMER's inputs at the
  consumer's mkFlake eval time. The consumer's `nixpkgs.overlays = [
  phillipgreenii-nix-base.overlays.unstable ]` therefore uses the consumer's
  pin of `nixpkgs-unstable`, not nix-repo-base's (nix-repo-base no longer
  declares one).

This is the core mechanism that drops nix-repo-base's lock graph: heavy
upstreams are no longer declared by nix-repo-base.

**nix-repo-base does NOT import its own heavy-overlay modules into its own
flake.nix.** It exports them via `flakeModules.<name>-overlay` for consumers
but has no internal need for them (no internal use of unstable/llm-agents/flox/
vscode-extensions). It DOES import its own `flakeModules.{checks, devshell,
pre-commit}` (treefmt comes in transitively via pre-commit) and its own
light-overlay module (`flakeModules.gomod2nix-overlay`) for internal use.
This avoids the chicken-and-egg where nix-repo-base would need to declare
the heavy inputs it's trying to shed.

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

Expected nix-repo-base `flake.lock` node count: single-digit (target ≤ 12;
was ~25). The exact count depends on what flake-parts and git-hooks pull
transitively in their current versions, so the AC is "no `<heavy-input>_<N>`
duplicates remain AND no node references any of {flox, llm-agents,
nix-vscode-extensions, nixpkgs-unstable, fenix, crane, bun2nix, blueprint,
rust-analyzer-src} at any depth" rather than a fixed integer. Verified by
inspecting `flake.lock` after the migration; AC #1 captures the exact
assertion.

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
  - Reconciles the tracked `.pre-commit-config.yaml` file BEFORE the clean-tree
    gate (so a stale value left by a prior `nix flake update` self-heals
    instead of tripping the gate). The file is typically a symlink into
    `/nix/store/...` regenerated by `nix run .#install-pre-commit-hooks`, but
    the contract is the same whether it's a symlink or a regular file.
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

Auto-contributed under `perSystem.checks.consumer-input-alignment` by
`flakeModules.checks` so every consumer of nix-repo-base gets it automatically
under `nix flake check`. The check uses two eval-time inputs:

- `config.phillipgreenii.alignment.requires` — a list of input names that the
  imported overlay modules declared they need. Each overlay module from §3.2
  sets this via `config.phillipgreenii.alignment.requires = [ "<name>" ];`
  (module-system list merging accumulates the requirements across all imported
  modules).
- The CONSUMER's `flake.lock` — read at build time inside the check derivation
  via `${inputs.self}/flake.lock`. flake-parts passes `inputs` into perSystem
  modules from the importing flake's outputs context, so `inputs.self` is the
  consumer's flake (not nix-repo-base's). The lock is available to
  `pkgs.runCommand` at build time via store-path coercion (no IFD required —
  it's a build-time `cat`, not eval-time `readFile`).

Note: the alignment check intentionally uses `inputs.self` rather than
`config.phillipgreenii.src`. `src` defaults to `inputs.self` but may be
overridden to a subdirectory for scoping formatting/linting; the alignment
check always wants the TOP-LEVEL `flake.lock`, never a subdirectory's view.

Implementation sketch:

```nix
# Inside flake-modules/checks.nix's perSystem block
{ self, pkgs, config, ... }: {
  checks.consumer-input-alignment = pkgs.runCommand "consumer-input-alignment" {
    requires = builtins.toJSON config.phillipgreenii.alignment.requires;
    consumerLock = "${self}/flake.lock";
    nativeBuildInputs = [ pkgs.jq ];
  } ''
    set -euo pipefail
    # For each required input name, assert presence + no _<N> duplicates
    # across all node keys in the lock.
    failed=0
    for name in $(echo "$requires" | jq -r '.[]'); do
      # … jq logic …
    done
    [ "$failed" = 0 ] || exit 1
    touch $out
  '';
}
```

For each required input name, the check asserts:

1. The input exists at `.nodes.root.inputs.<name>` of `flake.lock`.
2. No `<name>_<N>` (N ≥ 2) sibling key exists at `.nodes` (signals missing
   `follows` on a downstream flake).
3. If multiple flakes in the lock reference the same input name, all resolve
   to the same `.locked.rev` (cross-checked via `jq` reduction).

Failures emit an actionable message naming the missing/divergent input AND
the downstream flake responsible. Example:

```
error: nix-personal declares inputs.nixpkgs-unstable but your flake.lock
shows nixpkgs-unstable (rev: abc123) AND nixpkgs-unstable_2 (rev: def456).
Add `nix-personal.inputs.nixpkgs-unstable.follows = "nixpkgs-unstable"`
to your flake.nix.
```

When the consumer is nix-repo-base itself, `config.phillipgreenii.alignment.requires`
is empty (nix-repo-base doesn't import its own heavy-overlay modules per §3.3);
the check is a no-op `true && touch $out`.

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
   one-line `jq` verification command:
   ```bash
   jq -r '.nodes | keys[] | select(test("^(nixpkgs-unstable|llm-agents|flox|nix-vscode-extensions)(_[0-9]+)?$"))' flake.lock
   # Should print at most one line per heavy input. A `_N` suffix means a
   # downstream flake's view is unaligned and needs `follows`.
   ```
5. **Migration from the pre-chunk API** — a deletion table mapping
   `lib.mkChecks` → `flakeModules.checks` etc.; a `lib.mkInstallMetadata { … }`
   → `imports = [ homeModules.install-metadata ]` + options example.

## 7. Acceptance criteria

The chunk is complete when ALL of the following pass:

1. **nix-repo-base's `flake.lock` is free of bloat.** Run:
   ```bash
   jq -r '.nodes | keys[]' flake.lock | grep -vE '^(root|nixpkgs|nixpkgs-lib|flake-parts|git-hooks|treefmt-nix|gomod2nix|flake-compat|gitignore|systems)$'
   ```
   Must print zero lines. (Allowed: nixpkgs, nixpkgs-lib, flake-parts,
   git-hooks, treefmt-nix, gomod2nix, plus standard small transitives
   flake-compat/gitignore/systems brought in by those.) Equivalently, every
   one of {`flox`, `llm-agents`, `nix-vscode-extensions`, `nixpkgs-unstable`,
   `fenix`, `crane`, `bun2nix`, `blueprint`, `rust-analyzer-src`} must be
   absent from the lock. Lock node count is implicitly single-digit.
2. **`nix flake check` passes** on nix-repo-base. Per §3.3, nix-repo-base does
   not import its own heavy-overlay modules, so
   `config.phillipgreenii.alignment.requires` is empty in nix-repo-base's
   self-eval; the `consumer-input-alignment` check is a no-op there. The
   alignment check's effectiveness is verified by AC #5's fixture.
3. **`nix flake show` returns** the expected top-level outputs:
   - `flakeModules.{checks, pre-commit, devshell, treefmt, unstable-overlay,
     llm-agents-overlay, vscode-extensions-overlay, flox-overlay,
     gomod2nix-overlay}`
   - `homeModules.{pn, install-metadata}` (`pn` = existing aggregate;
     `install-metadata` = the new Shape B configurable module)
   - `darwinModules.default`
   - `overlays.default` (unchanged — surfaces pn)
   - `lib.{mkBashBuilders, mkGoBuilders, mkManPage, mkGitHash, mkVersion,
     mkSrcDigest, mkSimplePackageModule, mkEnableablePackageModule,
     mkDockRegistration, mkProgramModule}` (NOT: `mkChecks`, `mkPreCommitHooks`,
     `mkDevShell`, `mkTreefmtConfig`, `mkInstallMetadata`, `mkUnstableOverlay`,
     `mkLlmAgentsOverlay`, `mkVscodeExtensionsOverlay`, `mkFloxOverlay`)
   - `packages.<system>.{update-locks-lib, determine-ul-lib-dir, pn, fix-lint}`
     (declared directly in nix-repo-base's flake.nix per §3.2; NOT contributed
     by modules). Note `install-pre-commit-hooks` is no longer in nix-repo-base's
     own packages because nix-repo-base imports its own `flakeModules.pre-commit`,
     which DOES contribute it under nix-repo-base's perSystem.packages.
4. **All deleted lib symbols are absent** from `nix eval .#lib --apply
   'lib: builtins.attrNames lib'`.
5. **A consumer-fixture flake under `tests/consumer-fixture/`** evaluates
   cleanly: imports the 9 flake modules + 1 HM module, declares the 4 heavy
   inputs and `gomod2nix`, exercises `lib.mkBashBuilders`/`mkGoBuilders`/`mkManPage`
   from an overlay context, exercises the `homeModules.install-metadata` options
   via the wrapper pattern from §3.2, and runs `nix flake check` (which fires
   the `consumer-input-alignment` check non-trivially). The fixture is built
   as part of nix-repo-base's CI. This is what verifies the producer-side
   change end-to-end without waiting for consumer-side migrations.
6. **`update-locks-lib.bash` CONTRACT block** is in place; `test-update-locks-lib`
   check still passes (semantics unchanged).
7. **`README.md` documents the consumer wiring + follows pattern** (§6).

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
  anchor names instead of line numbers (tiny doc change). Was AC #7 in an
  earlier draft of this spec; moved here because it's a consumer-side change
  the producer chunk cannot perform itself. The CONTRACT anchors are stable
  in nix-repo-base regardless of when nix-overlay updates its references.

## 10. Beads disposition when chunk lands

- **tc-8rzk6** — closes. Heavy inputs dropped; nix-repo-base's lock shrinks
  from ~25 to single-digit nodes per AC #1. Consumer-side pruning (tc-rzgzq
  for nix-overlay) unblocks.
- **tc-henah** — closes. 9 flake modules + 1 configurable HM module shipped.
  Consumer migration (tc-zt0hh for nix-overlay M3) unblocks.
- **tc-qcqwu** — closes. CONTRACT block shipped; named anchors replace
  cross-repo line references.
- **tc-66t7y** — closes as "addressed by chunk architecture." Tag-releases arm
  is dead (no releases). Vendor-small-pieces arm is superseded (modules
  provide cleaner reuse). Module-metadata + pin-as-version arm is what the
  chunk delivers.
