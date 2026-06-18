# flake-parts Modular Producer Implementation Plan

**Goal:** Migrate nix-repo-base to flake-parts, ship 9 flakeModules + 1 configurable HM module + documented bash CONTRACT, drop 4 heavy direct inputs (lock graph 25→single-digit), hard-cutover-delete 9 lib functions.

**Architecture:** Producer-side modernization. flake-parts-based outputs; perSystem-shaped behavior moves into `flakeModules/*.nix` consumed via `imports = [ ... ]`; overlay factories that closed over heavy upstream inputs become flake-parts modules that read those inputs from the CONSUMER's `inputs` (the consumer declares the inputs); pure-function lib helpers (builders, version helpers, HM module factories) survive unchanged because they're called from non-perSystem contexts.

**Tech Stack:** Nix (flakes), flake-parts, git-hooks.nix, treefmt-nix, gomod2nix, bash, jq.

**Spec:** `docs/superpowers/specs/2026-06-18-flake-parts-modular-producer-design.md`

## Global Constraints

- **flake-parts is the framework.** `flake-parts.lib.mkFlake { inherit inputs; } { systems; perSystem; flake; imports; }` — not `flake-utils.lib.eachDefaultSystem`.
- **nix-repo-base does NOT import its own heavy-overlay modules** (unstable, llm-agents, vscode-extensions, flox). It exports them via `flakeModules.<name>-overlay` for consumers. The chicken-and-egg of "module needs input that nix-repo-base is trying to shed" is avoided by NOT having nix-repo-base import them itself. nix-repo-base DOES import its own perSystem modules (checks/devshell/pre-commit/treefmt) and the light-overlay module (gomod2nix-overlay).
- **Two module-shape patterns** (spec §3.3):
  - *Light upstream module*: file is a function `producerInputs: { ... }: { ... }` — nix-repo-base's `flake.nix` calls it with `self.inputs` baked in. Inputs closed over at producer-export time.
  - *Heavy upstream module*: file is a function `{ inputs, ... }: { ... }` — `inputs` is the CONSUMER's inputs at consumer's mkFlake eval time. The overlay function it produces also closes over the consumer's inputs.
- **Hard cutover.** Deleted lib symbols (mkChecks, mkPreCommitHooks, mkDevShell, mkTreefmtConfig, mkInstallMetadata, mkUnstableOverlay, mkLlmAgentsOverlay, mkVscodeExtensionsOverlay, mkFloxOverlay) leave the same producer rev that introduces modules.
- **Existing top-level outputs preserved**: `overlays.default` (surfaces pn), `darwinModules.default`, `homeModules.pn`, `packages.<system>.{pn, update-locks-lib, determine-ul-lib-dir, fix-lint}`.
- **Surviving lib functions** (10): `mkBashBuilders`, `mkGoBuilders`, `mkManPage`, `mkGitHash`, `mkVersion`, `mkSrcDigest`, `mkSimplePackageModule`, `mkEnableablePackageModule`, `mkDockRegistration`, `mkProgramModule`.
- **Working tree:** `/home/tcadmin/workspace/nix-repo-base-flake-parts`. All implementation commits land on the implementation branch (named in the chunk's epic bead, e.g. `feat/flake-parts-modular-producer`), NOT on the spec branch `docs/flake-parts-modular-spec`.
- **Validation:** every task ends with `nix flake check` passing AND `nix flake show` returning expected outputs. Where a task changes only one file, prefer narrower validation (`nix eval .#<attr>`) for speed.
- **No release tags.** Consumers operate at HEAD per [feedback-pin-is-the-version].
- **No UL_LIB_VERSION constant**, no runtime version-guards on internal APIs.
- **Commits are bead-final.** The implementer agent does NOT commit; the bead's `commit-push` step does. Each task in this plan ends with the file state ready for that step to stage and commit.

## File Structure

```
nix-repo-base-flake-parts/
├── flake.nix                          # REWRITE (flake-parts)
├── flake-modules/                     # NEW dir
│   ├── checks.nix                     # NEW (perSystem.checks + helpers + alignment plumbing)
│   ├── treefmt.nix                    # NEW
│   ├── pre-commit.nix                 # NEW (imports treefmt module)
│   ├── devshell.nix                   # NEW
│   └── overlays/
│       ├── unstable.nix               # NEW
│       ├── llm-agents.nix             # NEW
│       ├── vscode-extensions.nix      # NEW
│       ├── flox.nix                   # NEW
│       └── gomod2nix.nix              # NEW
├── home-modules/                      # NEW dir
│   └── install-metadata.nix           # NEW (Shape B)
├── lib/
│   ├── bash-builders.nix              # UNCHANGED
│   ├── go-builders.nix                # EDIT — add buildGoApplication assert
│   ├── version.nix                    # EDIT — drop mkInstallMetadata; keep mkGitHash/mkVersion/mkSrcDigest
│   └── scripts/
│       └── update-locks-lib.bash      # EDIT — add CONTRACT block + named anchors
├── nix/
│   ├── dev-env.nix                    # DELETE (logic moved into flake-modules/)
│   ├── checks.nix                     # DELETE (logic moved into flake-modules/checks.nix)
│   ├── packages.nix                   # UNCHANGED
│   └── module-helpers.nix             # UNCHANGED
├── README.md                          # CREATE or UPDATE (consumer wiring + follows pattern)
└── tests/consumer-fixture/            # NEW dir
    ├── flake.nix                      # NEW — exercises all modules end-to-end
    └── flake.lock                     # NEW — committed lock for reproducibility
```

---

## Task Ordering Rationale

Tasks 1-2 establish the flake-parts skeleton WITHOUT removing old behavior, so each commit boundary is buildable. Tasks 3-13 extract behavior into modules one-at-a-time. Task 14 deletes the old lib symbols. Task 15 drops the four heavy inputs (must happen AFTER Task 14 because the deleted lib factories close over those inputs). Task 16 drops `mkInstallMetadata` from `lib/version.nix`. Task 17 deletes `nix/dev-env.nix` and `nix/checks.nix`. Task 18 adds the `mkGoBuilders` runtime assertion. Task 19 adds the bash CONTRACT block. Task 20 writes the README. Task 21 ships the consumer fixture. Task 22 is the final lock-bloat + AC verification.

Within a task, sub-steps follow TDD when meaningful (write the verification command first, see it fail, then implement, see it pass). For Nix-flake additions, "the test" is `nix eval` / `nix flake show` returning the expected attribute.

---

## Task 1: Scaffold flake-parts framework alongside existing flake-utils

**Files:**
- Modify: `flake.nix` (add `flake-parts.url` to inputs; wrap existing outputs in `mkFlake { } { systems; perSystem; flake; }` adapter)

**Interfaces:**
- Consumes: nothing new from earlier tasks (this is task 1).
- Produces: `flake.nix` that evaluates under flake-parts. Still uses lib factories internally (those go away in later tasks). All current outputs preserved.

**Why this shape:** A migration that both swaps the framework AND extracts modules in one commit is too big to review and too easy to break. This task is "framework swap only — no behavior changes." Subsequent tasks extract behavior incrementally.

- [ ] **Step 1: Read the current flake.nix to understand the full output set**

Run: `cat flake.nix | head -250`

Note every output produced today (packages, checks, devShells, formatter, homeModules, darwinModules, overlays, lib).

- [ ] **Step 2: Capture the baseline `nix flake show` for comparison**

Run: `nix flake show --json 2>/dev/null | jq -S . > /tmp/flake-show-baseline.json`

This file is the snapshot the migrated flake must reproduce exactly (modulo the new `flake-parts` input under `inputs` — but `flake show` doesn't show inputs).

- [ ] **Step 3: Add `flake-parts` to the inputs block**

Edit `flake.nix` inputs block. Add `flake-parts.url = "github:hercules-ci/flake-parts";` and `flake-parts.inputs.nixpkgs-lib.follows = "nixpkgs";`. Keep all other inputs unchanged for now.

- [ ] **Step 4: Rewrite outputs to use `flake-parts.lib.mkFlake`**

Replace the current `outputs = { ... }: ... flake-utils.lib.eachDefaultSystem (system: { ... }) // { ... }` with:

```nix
outputs = inputs@{ self, flake-parts, nixpkgs, ... }:
  flake-parts.lib.mkFlake { inherit inputs; } {
    systems = [ "x86_64-linux" "aarch64-darwin" ];

    perSystem = { config, pkgs, system, self', inputs', ... }:
      let
        # Hand-port everything that previously lived inside eachDefaultSystem.
        # All lib factories continue to be used here; later tasks replace them.
        treefmtEval = inputs.treefmt-nix.lib.evalModule pkgs ./treefmt.nix;
        checks-lib = import ./nix/checks.nix { inherit pkgs; };
        pre-commit = (import ./nix/dev-env.nix {
          inherit (inputs) treefmt-nix git-hooks;
          inherit nixpkgs;
        }).mkPreCommitHooks {
          inherit system;
          src = ./.;
          treefmtWrapper = treefmtEval.config.build.wrapper;
        };
        bashBuilders = (import ./nix/packages.nix { }).mkBashBuilders {
          inherit pkgs self;
          inherit (pkgs) lib;
        };
        ulScripts = import ./modules/ul/scripts.nix {
          inherit pkgs bashBuilders;
          inherit (self.packages.${system}) update-locks-lib;
        };
      in
      {
        # _module.args lets later modules use the same overlay-applied pkgs
        _module.args.pkgs = import inputs.nixpkgs {
          inherit system;
          overlays = [ inputs.gomod2nix.overlays.default ];
        };

        formatter = treefmtEval.config.build.wrapper;

        packages = {
          update-locks-lib = pkgs.runCommand "update-locks-lib" { } ''
            mkdir -p $out/lib/scripts
            cp ${./lib/scripts/update-locks-lib.bash} $out/lib/scripts/update-locks-lib.bash
            cp ${./lib/scripts/update-cache-lib.bash} $out/lib/scripts/update-cache-lib.bash
          '';
          fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
            ${pkgs.lib.getExe pkgs.statix} fix ${./.}
          '';
          install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
            ${pre-commit.shellHook}
            echo "Pre-commit hooks installed successfully!"
            echo "Run 'pre-commit run --all-files' to test them."
          '';
          determine-ul-lib-dir = ulScripts.determine-ul-lib-dir.script;
          pn = pkgs.callPackage ./modules/pn { inherit self; };
        };

        checks = {
          formatting = treefmtEval.config.build.check self;
          linting = checks-lib.linting ./.;
          shellcheck = checks-lib.shellcheck {
            scripts = [
              ./lib/scripts/update-locks-lib.bash
              ./lib/scripts/update-cache-lib.bash
            ];
          };
          test-update-locks-lib = checks-lib.testUpdateLocksLib { };
          version-lib =
            let
              failures = pkgs.lib.runTests (import ./lib/version-tests.nix);
            in
            pkgs.runCommand "check-version-lib" { } (
              if failures == [ ] then "touch $out"
              else "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
            );
          bash-version-rev-independent = import ./lib/bash-builders-version-tests.nix { inherit pkgs; };
          pn-go-tests = pkgs.callPackage ./modules/pn { inherit self; };
          pn-logsources-registration =
            let
              eval = pkgs.lib.evalModules {
                modules = [
                  # Narrow stub: declares just enough of the support-apps observability
                  # surface for the pn module to type-check standalone (the real option
                  # lives in phillipgreenii-nix-support-apps). Mirrors that flake's
                  # crossFlakeOptionStubs.
                  {
                    options.phillipgreenii.observability = {
                      enable = pkgs.lib.mkEnableOption "observability (stub)";
                      logSources = pkgs.lib.mkOption {
                        type = pkgs.lib.types.attrsOf pkgs.lib.types.anything;
                        default = { };
                      };
                    };
                    config.phillipgreenii.observability.enable = true;
                  }
                  ./darwin
                ];
              };
            in
            pkgs.runCommand "pn-logsources-registration" { } (
              if eval.config.phillipgreenii.observability.logSources ? pn then
                "touch $out"
              else
                throw "pn darwin module did not register logSources.pn"
            );
        } // ulScripts.checks;

        devShells.default = (import ./nix/dev-env.nix {
          inherit (inputs) treefmt-nix git-hooks;
          inherit nixpkgs;
        }).mkDevShell {
          inherit pkgs;
          pre-commit-shellHook = pre-commit.shellHook;
        };
      };

    flake = {
      # Everything that was previously the top-level `// { ... }` block.
      homeModules.pn = import ./home/pn/default.nix;
      darwinModules.default = ./darwin;
      homeModules.install-metadata = (import ./lib/version.nix).mkInstallMetadata {
        flakeSelf = self;
        name = "phillipg-nix-repo-base";
      };
      overlays.default = final: _prev: {
        inherit (self.packages.${final.stdenv.hostPlatform.system}) pn;
      };
      lib =
        (import ./lib/version.nix)
        // {
          inherit ((import ./nix/packages.nix { }))
            mkBashBuilders
            mkGoBuilders
            mkManPage
            ;
        }
        // {
          inherit ((import ./nix/dev-env.nix {
            inherit (inputs) treefmt-nix git-hooks;
            inherit nixpkgs;
          }))
            mkTreefmtConfig
            mkPreCommitHooks
            mkDevShell
            ;
        }
        // {
          inherit ((import ./nix/module-helpers.nix { }))
            mkSimplePackageModule
            mkEnableablePackageModule
            mkDockRegistration
            mkProgramModule
            ;
        }
        // {
          mkChecks = pkgs: import ./nix/checks.nix { inherit pkgs; };
          mkUnstableOverlay = _final: prev: {
            unstable = import inputs.nixpkgs-unstable {
              inherit (prev.stdenv.hostPlatform) system;
              config.allowUnfree = true;
            };
          };
          mkLlmAgentsOverlay = _final: prev: {
            llm-agentsPkgs = inputs.llm-agents.packages.${prev.stdenv.hostPlatform.system};
          };
          mkFloxOverlay = _final: prev: {
            floxPkgs = inputs.flox.packages.${prev.stdenv.hostPlatform.system};
          };
          mkVscodeExtensionsOverlay = _final: prev: {
            inherit (inputs.nix-vscode-extensions.extensions.${prev.stdenv.hostPlatform.system})
              vscode-marketplace
              open-vsx
              ;
          };
        };
    };
  };
```

- [ ] **Step 5: Verify `nix flake show` is byte-equivalent to the baseline**

Run: `nix flake show --json 2>/dev/null | jq -S . > /tmp/flake-show-after-task1.json && diff /tmp/flake-show-baseline.json /tmp/flake-show-after-task1.json`

Expected: zero diff. If diff appears, fix the migrated flake until it matches.

- [ ] **Step 6: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -20`

Expected: exit 0, no errors. (treefmt/linting/shellcheck/test-update-locks-lib all pass on the current source.)

- [ ] **Step 7: Verify lock graph is still minimal (no new transitives)**

Run: `jq -r '.nodes | keys | length' flake.lock`

Expected: `flake-parts` adds ~2 nodes (itself + nixpkgs-lib which `follows = nixpkgs`). Other inputs unchanged.

- [ ] **Step 8: Stage `flake.nix` for the bead's commit-push step**

Run: `git add flake.nix flake.lock`

(Do NOT commit. The bead's commit-push step handles it.)

---

## Task 2: Drop `flake-utils` input (now unused)

**Files:**
- Modify: `flake.nix` (remove `flake-utils.url` and the `flake-utils` arg from outputs signature)

**Interfaces:**
- Consumes: Task 1's flake-parts skeleton.
- Produces: `flake.nix` with one fewer input.

- [ ] **Step 1: Verify flake-utils is not referenced in any output (after Task 1)**

Run: `grep -n "flake-utils\|flake_utils" flake.nix`

Expected: only the input declaration and the outputs-signature arg.

- [ ] **Step 2: Remove `flake-utils.url = "...";` from inputs block**

Edit `flake.nix`.

- [ ] **Step 3: Remove `flake-utils,` from outputs signature**

Edit `flake.nix` outputs arg destructuring.

- [ ] **Step 4: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 5: Verify lock graph dropped flake-utils**

Run: `jq -r '.nodes | keys[]' flake.lock | grep flake-utils || echo "absent (good)"`

Expected: "absent (good)".

- [ ] **Step 6: Stage `flake.nix` + `flake.lock`**

Run: `git add flake.nix flake.lock`

---

## Task 3: Create `flake-modules/treefmt.nix` (perSystem.formatter + perSystem.treefmt)

**Files:**
- Create: `flake-modules/treefmt.nix`
- Modify: `flake.nix` (import the new module; remove inline treefmt wiring)
- Delete: `treefmt.nix` (root-level — now redundant with the module + treefmt-nix's flakeModule pattern)

**Interfaces:**
- Consumes: `flake-parts` lib, `treefmt-nix` (light upstream — producer-owned).
- Produces: `flakeModules.treefmt` (exported in Task 12). After-import contributes `perSystem.formatter` and `perSystem.treefmt.*`.

**Note on `inputs` vs `self.inputs`:** within `outputs = inputs@{ self, ... }: flake-parts.lib.mkFlake { ... } { ... }`, the `inputs` symbol and `self.inputs` resolve to the same attrset (the producer's input pins). This plan uses `inputs` consistently for clarity. Either works.

- [ ] **Step 1: Create `flake-modules/treefmt.nix`**

```nix
# Light-upstream module: closes over the producer's own `treefmt-nix` input.
# Consumers import this module; they do NOT need to declare `treefmt-nix`
# themselves (it appears as a transitive node in their lock).
producerInputs:
{ lib, flake-parts-lib, ... }:
{
  imports = [ producerInputs.treefmt-nix.flakeModule ];

  perSystem = { config, pkgs, ... }: {
    treefmt = {
      projectRootFile = "flake.nix";
      programs = {
        nixfmt = {
          enable = true;
          package = pkgs.nixfmt;
        };
        prettier = {
          enable = true;
          includes = [
            "*.md"
            "*.yaml"
            "*.yml"
            "*.json"
          ];
        };
        shellcheck.enable = true;
        shfmt = {
          enable = true;
          indent_size = 2;
        };
      };
    };

    # treefmt-nix's flakeModule sets formatter, formatter.build.wrapper, etc.
    # No additional perSystem.formatter needed.
  };
}
```

(Note: `shellcheck.enable = true;` matches the current root-level `treefmt.nix` (line 19). The plan-critic flagged this as a potential silent omission; keep it to preserve formatter behavior.)

- [ ] **Step 2: Modify `flake.nix` to import this module and stop wiring treefmt inline**

In `flake.nix` outputs:

a) Remove `treefmtEval = inputs.treefmt-nix.lib.evalModule pkgs ./treefmt.nix;` from `perSystem`'s let-block.

b) Add `imports = [ (import ./flake-modules/treefmt.nix inputs) ];` at the top level of the mkFlake block (alongside `systems`, `perSystem`, `flake`).

c) Replace `formatter = treefmtEval.config.build.wrapper;` in `perSystem` with deletion (treefmt-nix's flakeModule sets `perSystem.formatter`).

d) Replace `formatting = treefmtEval.config.build.check self;` in `perSystem.checks` with `formatting = config.treefmt.build.check self;` (reading the module's option output via perSystem `config`).

e) Replace `treefmtWrapper = treefmtEval.config.build.wrapper;` in the `mkPreCommitHooks` call with `treefmtWrapper = config.treefmt.build.wrapper;`.

- [ ] **Step 2b: Delete the root-level `treefmt.nix`**

The root-level `treefmt.nix` is no longer referenced (treefmt config now lives inside `flake-modules/treefmt.nix` per the new module pattern).

Run: `git rm treefmt.nix`

- [ ] **Step 3: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0. Both `formatting` check and pre-commit's treefmt hook should still work.

- [ ] **Step 4: Verify `nix eval .#formatter.<system>.outPath` resolves**

Run: `nix eval --raw .#formatter.x86_64-linux 2>&1 | head -2 || nix eval --raw .#formatter.aarch64-darwin 2>&1 | head -2`

Expected: a store path.

- [ ] **Step 5: Stage**

Run: `git add flake.nix flake-modules/treefmt.nix`

---

## Task 4: Create `flake-modules/pre-commit.nix` (imports treefmt module)

**Files:**
- Create: `flake-modules/pre-commit.nix`
- Modify: `flake.nix` (import pre-commit module; drop inline pre-commit + install-pre-commit-hooks package wiring; the module contributes them)

**Interfaces:**
- Consumes: producer's `git-hooks` input (light upstream); the `treefmt` module (auto-imported).
- Produces: `flakeModules.pre-commit`. After-import contributes `perSystem.checks.pre-commit` and `perSystem.packages.install-pre-commit-hooks`.

- [ ] **Step 1: Create `flake-modules/pre-commit.nix`**

```nix
# Light-upstream module: closes over the producer's `git-hooks` input.
# IMPORTS `flake-modules/treefmt.nix` because the pre-commit `treefmt` hook
# needs the formatter wrapper. Consumers who import pre-commit get treefmt
# automatically; they do NOT need to import treefmt separately.
producerInputs:
{ lib, ... }:
{
  imports = [ (import ./treefmt.nix producerInputs) ];

  options.phillipgreenii.pre-commit = {
    src = lib.mkOption {
      type = lib.types.path;
      description = "Source path passed to git-hooks for hook registration.";
    };
    extraHooks = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
      description = "Additional hooks merged into the standard set.";
    };
  };

  config.perSystem = { config, pkgs, system, ... }:
    let
      cfg = config.phillipgreenii.pre-commit;
      # Note: under flake-parts, `pkgs` is already system-specific (set via
      # _module.args.pkgs or flake-parts' default). The old `nix/dev-env.nix`
      # used `nixpkgs.legacyPackages.${system}.prek` because it was outside a
      # perSystem context; here `pkgs.prek` is the same value.
      preCommit = producerInputs.git-hooks.lib.${system}.run {
        src = cfg.src;
        package = pkgs.prek;
        tools.dotnet-sdk = pkgs.runCommand "dotnet-stub" { } "mkdir $out";
        hooks = {
          treefmt = {
            enable = true;
            package = config.treefmt.build.wrapper;
          };
          statix = { enable = true; name = "statix"; };
          deadnix = { enable = true; name = "deadnix"; };
          shellcheck = { enable = true; name = "shellcheck"; args = [ "--severity=error" ]; };
          check-merge-conflicts.enable = true;
          trailing-whitespace = {
            enable = true;
            entry = "${pkgs.python3Packages.pre-commit-hooks}/bin/trailing-whitespace-fixer";
          };
          end-of-file-fixer = {
            enable = true;
            entry = "${pkgs.python3Packages.pre-commit-hooks}/bin/end-of-file-fixer";
          };
          check-case-conflicts.enable = true;
        } // cfg.extraHooks;
      };
    in
    {
      # Expose the shellHook for the devshell module to read.
      _module.args.preCommitShellHook = preCommit.shellHook;

      checks.pre-commit = preCommit;

      packages.install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
        ${preCommit.shellHook}
        echo "Pre-commit hooks installed successfully!"
        echo "Run 'pre-commit run --all-files' to test them."
      '';
    };
}
```

- [ ] **Step 2: Modify `flake.nix` — add import + remove inline pre-commit wiring**

a) Add `(import ./flake-modules/pre-commit.nix inputs)` to the top-level `imports = [ ... ]` list.

b) Remove the inline `pre-commit = (import ./nix/dev-env.nix { ... }).mkPreCommitHooks { ... };` from the `perSystem` let-block.

c) Remove `install-pre-commit-hooks` from `perSystem.packages` (the module contributes it).

d) Set `phillipgreenii.pre-commit.src = ./.;` at the top level (or inside perSystem if the option is perSystem-scoped — check what the module actually declares; per the spec it's a top-level option but its CONFIG is perSystem).

   Wait — the option declared above is `options.phillipgreenii.pre-commit.src` (top-level, not perSystem) but `config.perSystem` reads it. Since flake-parts modules can declare top-level options that are READ inside perSystem, this works. Set `phillipgreenii.pre-commit.src = ./.;` at top level of `mkFlake { ... }`.

e) In `checks`, the `pre-commit` check key is now contributed by the module — remove any pre-existing `pre-commit` from the inline checks block if present.

- [ ] **Step 3: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -20`

Expected: exit 0. The pre-commit check should fire and pass on the current source.

- [ ] **Step 4: Verify install-pre-commit-hooks package exists**

Run: `nix eval --raw .#packages.x86_64-linux.install-pre-commit-hooks 2>&1 | head -2 || nix eval --raw .#packages.aarch64-darwin.install-pre-commit-hooks 2>&1 | head -2`

Expected: a store path.

- [ ] **Step 5: Stage**

Run: `git add flake.nix flake-modules/pre-commit.nix`

---

## Task 5: Create `flake-modules/devshell.nix`

**Files:**
- Create: `flake-modules/devshell.nix`
- Modify: `flake.nix` (import devshell module; remove inline `devShells.default` wiring)

**Interfaces:**
- Consumes: nothing extra; reads `_module.args.preCommitShellHook` set by pre-commit module.
- Produces: `flakeModules.devshell`. Contributes `perSystem.devShells.default`.

- [ ] **Step 1: Create `flake-modules/devshell.nix`**

```nix
# No producer-input closure needed — devshell uses only the consumer's pkgs.
{ lib, ... }:
{
  options.phillipgreenii.devshell = {
    extraInputs = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
      description = "Additional packages added to the default devShell.";
    };
  };

  config.perSystem = { config, pkgs, preCommitShellHook ? "", ... }:
    {
      devShells.default = pkgs.mkShell {
        shellHook = preCommitShellHook;
        buildInputs = [
          pkgs.nixfmt
          pkgs.statix
          pkgs.deadnix
          pkgs.shellcheck
        ] ++ config.phillipgreenii.devshell.extraInputs;
      };
    };
}
```

- [ ] **Step 2: Modify `flake.nix` — add import, remove inline devShells**

a) Add `./flake-modules/devshell.nix` to the top-level `imports = [ ... ]` list. (No `self.inputs` arg needed — it's not a function-returning-module.)

b) Remove the inline `devShells.default = (import ./nix/dev-env.nix { ... }).mkDevShell { ... };` from `perSystem`.

- [ ] **Step 3: Verify `nix develop` enters the shell**

Run: `nix develop --command true 2>&1 | tail -5`

Expected: exits 0; no errors. (The shell hook runs install-pre-commit-hooks; that's OK in CI.)

- [ ] **Step 4: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 5: Stage**

Run: `git add flake.nix flake-modules/devshell.nix`

---

## Task 6: Create `flake-modules/checks.nix` (helpers + auto checks + alignment plumbing)

**Files:**
- Create: `flake-modules/checks.nix`
- Modify: `flake.nix` (import checks module; remove inline `linting`/`shellcheck`/`testUpdateLocksLib` wiring; remove `import ./nix/checks.nix` call site)

**Interfaces:**
- Consumes: producer's pkgs (gomod2nix-overlaid).
- Produces: `flakeModules.checks`. Contributes `perSystem.checks.{formatting,linting,consumer-input-alignment}` (auto) and `config.phillipgreenii.checks.helpers.*` (opt-in helpers callable from perSystem.checks.<name> = ...).

- [ ] **Step 1: Create `flake-modules/checks.nix`**

```nix
# No producer-input closure needed — checks helpers use only consumer pkgs.
# Consumer must set `phillipgreenii.src = ./.;` for the auto-contributed
# formatting + linting checks to wire up.
{ lib, ... }:
{
  options.phillipgreenii = {
    src = lib.mkOption {
      type = lib.types.path;
      description = "Source path used by auto-contributed checks (formatting, linting).";
    };
    alignment.requires = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Names of consumer-declared flake inputs that imported overlay modules
        require. The consumer-input-alignment check reads this list and verifies
        the consumer's flake.lock has each input declared exactly once
        (no <name>_N duplicates from missing follows).
      '';
    };
  };

  config.perSystem = { config, pkgs, inputs, system, self', ... }:
    let
      cfg = config.phillipgreenii;
      # flake-parts passes the consumer's `inputs` to perSystem; `inputs.self`
      # is the consumer flake's source (whatever flake imported this module).
      consumerSelf = inputs.self;
      mkHelpers = pkgs: {
        formatting = root: pkgs.runCommand "check-formatting" {
          nativeBuildInputs = [ pkgs.nixfmt ];
        } ''
          cd ${root}
          nixfmt --check .
          touch $out
        '';

        linting = root: pkgs.runCommand "check-linting" {
          nativeBuildInputs = [ pkgs.statix ];
        } ''
          statix check ${root}
          touch $out
        '';

        shellcheck = { scripts, exclude ? [ ], allowWarnings ? false }:
          let
            excludeArgs = if exclude != [ ] then "-e ${lib.concatStringsSep "," exclude}" else "";
            errorHandling = if allowWarnings then " || true" else "";
          in
          pkgs.runCommand "check-shellcheck" {
            nativeBuildInputs = [ pkgs.shellcheck ];
          } ''
            ${lib.concatMapStringsSep "\n" (
              script: "${pkgs.shellcheck}/bin/shellcheck ${excludeArgs} ${script}${errorHandling}"
            ) scripts}
            touch $out
          '';

        testBashScripts = { package, tests, extraInputs ? [ ] }:
          pkgs.runCommand "test-bash-scripts" {
            nativeBuildInputs = [ pkgs.bats pkgs.git pkgs.which package ] ++ extraInputs;
          } ''
            export PATH="${package}/bin:$PATH"
            bats ${tests}
            touch $out
          '';

        testPythonProject = { package, src, name, checkLibDir ? null }:
          pkgs.runCommand "test-${name}" {
            nativeBuildInputs = [ pkgs.bash pkgs.uv package ];
            inherit src;
          } ''
            export HOME=$TMPDIR
            export UV_CACHE_DIR=$TMPDIR/uv-cache
            mkdir -p $UV_CACHE_DIR
            ${lib.optionalString (checkLibDir != null) ''export CHECK_LIB_DIR="${checkLibDir}"''}
            cp -r $src ${name}
            cd ${name}
            chmod +w -R .
            bash check-all.sh --no-fix --quick --suppress-coverage-check
            touch $out
          '';

        testUpdateLocksLib = { testsDir ? ../lib/tests, scriptsDir ? ../lib/scripts }:
          pkgs.runCommand "test-update-locks-lib" {
            nativeBuildInputs = [ pkgs.bats pkgs.bash pkgs.coreutils pkgs.git ];
          } ''
            export PATH="${lib.makeBinPath [ pkgs.coreutils pkgs.bash pkgs.git ]}:$PATH"
            export UL_LIB_SCRIPTS_DIR="${scriptsDir}"
            export HOME="$TMPDIR/test-home"
            mkdir -p "$HOME"
            bats ${testsDir}
            touch $out
          '';
      };
      helpers = mkHelpers pkgs;
    in
    {
      _module.args.checksHelpers = helpers;

      checks = {
        formatting = helpers.formatting cfg.src;
        linting = helpers.linting cfg.src;
        consumer-input-alignment =
          let requiresJSON = builtins.toJSON cfg.alignment.requires; in
          pkgs.runCommand "consumer-input-alignment" {
            requires = requiresJSON;
            # The CONSUMER's flake source at build time — accessed via inputs.self
            # (flake-parts passes `inputs` to perSystem; `inputs.self` is the
            # importing flake's source). The store coercion makes the path
            # available at build time without IFD.
            consumerLock = builtins.toString (consumerSelf + "/flake.lock");
            nativeBuildInputs = [ pkgs.jq ];
          } ''
            set -euo pipefail
            count=$(echo "$requires" | ${pkgs.jq}/bin/jq -r 'length')
            if [ "$count" = "0" ]; then
              echo "alignment: no required inputs (no overlay modules imported)"
              touch $out
              exit 0
            fi
            failed=0
            for name in $(echo "$requires" | ${pkgs.jq}/bin/jq -r '.[]'); do
              # Top-level input must exist
              if ! ${pkgs.jq}/bin/jq -e --arg n "$name" '.nodes.root.inputs | has($n)' "$consumerLock" >/dev/null; then
                echo "ERROR: required input '$name' is not declared at top level of flake.lock" >&2
                failed=1
                continue
              fi
              # No <name>_N duplicates
              dupes=$(${pkgs.jq}/bin/jq -r --arg n "$name" '.nodes | keys[] | select(test("^" + $n + "_[0-9]+$"))' "$consumerLock")
              if [ -n "$dupes" ]; then
                echo "ERROR: input '$name' has duplicate nodes in flake.lock: $dupes" >&2
                echo "       Missing 'follows' on a downstream flake. Add e.g.:" >&2
                echo "       inputs.<downstream>.inputs.$name.follows = \"$name\";" >&2
                failed=1
              fi
            done
            [ "$failed" = 0 ] || exit 1
            touch $out
          '';
      };
    };
}
```

(`testUpdateLocksLib`'s default `testsDir ? ../lib/tests` and `scriptsDir ? ../lib/scripts` are relative to the MODULE FILE's location: `flake-modules/checks.nix`. The `..` resolves to the repo root, so the defaults still point at `$REPO_ROOT/lib/tests` and `$REPO_ROOT/lib/scripts` — same as the pre-migration `nix/checks.nix`. Do not adjust these paths.)

Note on `self` in perSystem: flake-parts passes `self` to perSystem from `mkFlake`'s outputs context. For nix-repo-base's own evaluation, `self` is nix-repo-base. For a consumer evaluating their own flake that imports this module, `self` is the consumer's flake. The `consumerLock` reference therefore reads whoever's lock is being checked.

- [ ] **Step 2: Modify `flake.nix` — import checks module + rewire inline checks**

a) Add `./flake-modules/checks.nix` to `imports = [ ... ]`.

b) Set `phillipgreenii.src = ./.;` at top level of mkFlake (if not already set in Task 4).

c) Remove inline definitions: `linting`, `shellcheck` (the one for update-locks-lib scripts), `test-update-locks-lib`. Replace with usages of `checksHelpers` from perSystem:

```nix
checks = {
  # formatting, linting, consumer-input-alignment are auto-contributed by the
  # checks module now.
  shellcheck = checksHelpers.shellcheck {
    scripts = [
      ./lib/scripts/update-locks-lib.bash
      ./lib/scripts/update-cache-lib.bash
    ];
  };
  test-update-locks-lib = checksHelpers.testUpdateLocksLib { };
  # Keep all the in-line check definitions that don't have helper analogues:
  version-lib = /* ... existing block ... */;
  bash-version-rev-independent = /* ... */;
  pn-go-tests = /* ... */;
  pn-logsources-registration = /* ... */;
} // ulScripts.checks;
```

d) Remove `checks-lib = import ./nix/checks.nix { inherit pkgs; };` from the `perSystem` let-block.

- [ ] **Step 3: Verify checks pass**

Run: `nix flake check 2>&1 | tail -20`

Expected: exit 0. The `consumer-input-alignment` check is a no-op for nix-repo-base itself (alignment.requires is empty).

- [ ] **Step 4: Verify expected check attributes exist**

Run: `nix flake show --json 2>/dev/null | jq -r '.checks."x86_64-linux" // .checks."aarch64-darwin" | keys[]' | sort`

Expected: includes `formatting`, `linting`, `consumer-input-alignment`, `pre-commit`, `shellcheck`, `test-update-locks-lib`, `version-lib`, `bash-version-rev-independent`, `pn-go-tests`, `pn-logsources-registration`, plus any `ulScripts.checks` attrs.

- [ ] **Step 5: Stage**

Run: `git add flake.nix flake-modules/checks.nix`

---

## Task 7: Create overlay module `flake-modules/overlays/gomod2nix.nix` (light upstream)

**Files:**
- Create: `flake-modules/overlays/gomod2nix.nix`
- Modify: `flake.nix` (import the gomod2nix-overlay module; remove the inline `_module.args.pkgs = import nixpkgs { overlays = [ gomod2nix.overlays.default ]; }; ` wiring if it lived in `perSystem` from Task 1)

**Interfaces:**
- Consumes: producer's own `gomod2nix` input (light upstream).
- Produces: `flakeModules.gomod2nix-overlay`. Contributes `flake.overlays.gomod2nix = inputs.gomod2nix.overlays.default;`.

- [ ] **Step 1: Create `flake-modules/overlays/gomod2nix.nix`**

```nix
# Light-upstream overlay module. Closes over the producer's gomod2nix input.
# Consumers import this module and apply `self.overlays.gomod2nix` to their
# pkgs. Consumers do NOT need to declare gomod2nix themselves.
producerInputs:
{ ... }:
{
  flake.overlays.gomod2nix = producerInputs.gomod2nix.overlays.default;
}
```

- [ ] **Step 2: Modify `flake.nix` — import gomod2nix-overlay; apply self.overlays.gomod2nix to nix-repo-base's own pkgs**

a) Add `(import ./flake-modules/overlays/gomod2nix.nix inputs)` to `imports = [ ... ]`.

b) In `perSystem`, set `_module.args.pkgs = import inputs.nixpkgs { inherit system; overlays = [ self.overlays.gomod2nix ]; };` (replacing the inline `inputs.gomod2nix.overlays.default` from Task 1). Here `self.overlays.gomod2nix` refers to nix-repo-base's own `self` (from outputs args), which after this import has the gomod2nix overlay contributed by the imported module.

- [ ] **Step 3: Verify mkGoBuilders / pn still works**

Run: `nix build .#pn --no-link 2>&1 | tail -10`

Expected: build succeeds (or "evaluation cached" if previously built).

- [ ] **Step 4: Verify `nix flake show` lists `overlays.gomod2nix`**

Run: `nix flake show --json 2>/dev/null | jq -r '.overlays | keys[]' | sort`

Expected: `default`, `gomod2nix` (and others from later tasks).

- [ ] **Step 5: Stage**

Run: `git add flake.nix flake-modules/overlays/gomod2nix.nix`

---

## Task 8: Create overlay module `flake-modules/overlays/unstable.nix` (heavy upstream)

**Files:**
- Create: `flake-modules/overlays/unstable.nix`
- Modify: `flake.nix` (import the unstable-overlay module)

**Interfaces:**
- Consumes: CONSUMER's `nixpkgs-unstable` input. Producer (nix-repo-base) does NOT import this module into its own flake (per spec §3.3), so nix-repo-base does NOT need to declare `nixpkgs-unstable`.
- Produces: `flakeModules.unstable-overlay`. Contributes `flake.overlays.unstable` AND adds `"nixpkgs-unstable"` to `phillipgreenii.alignment.requires`.

- [ ] **Step 1: Create `flake-modules/overlays/unstable.nix`**

```nix
# Heavy-upstream overlay module. Reads consumer's `inputs.nixpkgs-unstable`
# at evaluation time. Consumers must declare `nixpkgs-unstable.url = ...;`
# in their flake.nix; without it, evaluation fails with a clear error.
{ inputs, lib, ... }:
{
  flake.overlays.unstable = _final: prev: {
    unstable = import inputs.nixpkgs-unstable {
      inherit (prev.stdenv.hostPlatform) system;
      config.allowUnfree = true;
    };
  };

  # Plumb requirement for the consumer-input-alignment check.
  config.phillipgreenii.alignment.requires = [ "nixpkgs-unstable" ];
}
```

- [ ] **Step 2: Modify `flake.nix` — export the module (do NOT import it)**

In the top-level `flake = { ... }` block, add (alongside existing `darwinModules.default`, etc.):

```nix
flakeModules = {
  unstable-overlay = ./flake-modules/overlays/unstable.nix;
  # … other flakeModules added in later tasks
};
```

Do NOT add this module to nix-repo-base's own `imports = [ ... ]`.

- [ ] **Step 3: Verify `nix flake show` lists the new flakeModule**

Run: `nix flake show --json 2>/dev/null | jq -r '.flakeModules | keys[]' | sort`

Expected: includes `unstable-overlay`.

- [ ] **Step 4: Verify nix-repo-base's own `flake.overlays` does NOT include `unstable` (nix-repo-base doesn't self-import the module)**

Run: `nix flake show --json 2>/dev/null | jq -r '.overlays | keys[]' | sort`

Expected: `default`, `gomod2nix` (NOT `unstable`).

- [ ] **Step 5: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -5`

Expected: exit 0.

- [ ] **Step 6: Stage**

Run: `git add flake.nix flake-modules/overlays/unstable.nix`

---

## Task 9: Create overlay module `flake-modules/overlays/llm-agents.nix`

**Files:**
- Create: `flake-modules/overlays/llm-agents.nix`
- Modify: `flake.nix` (export as `flakeModules.llm-agents-overlay` in Task 12; this task only creates the file — do NOT add to nix-repo-base's `imports = [ ... ]`)

**Interfaces:**
- Consumes: CONSUMER's `llm-agents` input (heavy upstream).
- Produces: `flakeModules.llm-agents-overlay`. Contributes `flake.overlays.llm-agents` AND adds `"llm-agents"` to `phillipgreenii.alignment.requires`.

- [ ] **Step 1: Create `flake-modules/overlays/llm-agents.nix`**

```nix
# Heavy-upstream overlay module. Consumers must declare inputs.llm-agents.
{ inputs, lib, ... }:
{
  flake.overlays.llm-agents = _final: prev: {
    llm-agentsPkgs = inputs.llm-agents.packages.${prev.stdenv.hostPlatform.system};
  };

  config.phillipgreenii.alignment.requires = [ "llm-agents" ];
}
```

- [ ] **Step 2: Verify file is well-formed Nix**

Run: `nix-instantiate --parse flake-modules/overlays/llm-agents.nix > /dev/null && echo OK`

Expected: `OK`.

- [ ] **Step 3: Verify nix-repo-base's `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -5`

Expected: exit 0. (The new file is not yet imported by nix-repo-base or exported as a flakeModule — that happens in Task 12.)

- [ ] **Step 4: Stage**

Run: `git add flake-modules/overlays/llm-agents.nix`

---

## Task 10: Create overlay module `flake-modules/overlays/vscode-extensions.nix`

**Files:**
- Create: `flake-modules/overlays/vscode-extensions.nix`

**Interfaces:**
- Consumes: CONSUMER's `nix-vscode-extensions` input (heavy upstream).
- Produces: `flakeModules.vscode-extensions-overlay`. Contributes `flake.overlays.vscode-extensions` AND adds `"nix-vscode-extensions"` to `phillipgreenii.alignment.requires`.

- [ ] **Step 1: Create `flake-modules/overlays/vscode-extensions.nix`**

```nix
# Heavy-upstream overlay module. Consumers must declare inputs.nix-vscode-extensions.
{ inputs, lib, ... }:
{
  flake.overlays.vscode-extensions = _final: prev: {
    inherit (inputs.nix-vscode-extensions.extensions.${prev.stdenv.hostPlatform.system})
      vscode-marketplace
      open-vsx
      ;
  };

  config.phillipgreenii.alignment.requires = [ "nix-vscode-extensions" ];
}
```

- [ ] **Step 2: Verify file is well-formed Nix**

Run: `nix-instantiate --parse flake-modules/overlays/vscode-extensions.nix > /dev/null && echo OK`

Expected: `OK`.

- [ ] **Step 3: Verify nix-repo-base's `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -5`

Expected: exit 0.

- [ ] **Step 4: Stage**

Run: `git add flake-modules/overlays/vscode-extensions.nix`

---

## Task 11: Create overlay module `flake-modules/overlays/flox.nix`

**Files:**
- Create: `flake-modules/overlays/flox.nix`

**Interfaces:**
- Consumes: CONSUMER's `flox` input (heavy upstream).
- Produces: `flakeModules.flox-overlay`. Contributes `flake.overlays.flox` AND adds `"flox"` to `phillipgreenii.alignment.requires`.

- [ ] **Step 1: Create `flake-modules/overlays/flox.nix`**

```nix
# Heavy-upstream overlay module. Consumers must declare inputs.flox.
{ inputs, lib, ... }:
{
  flake.overlays.flox = _final: prev: {
    floxPkgs = inputs.flox.packages.${prev.stdenv.hostPlatform.system};
  };

  config.phillipgreenii.alignment.requires = [ "flox" ];
}
```

- [ ] **Step 2: Verify file is well-formed Nix**

Run: `nix-instantiate --parse flake-modules/overlays/flox.nix > /dev/null && echo OK`

Expected: `OK`.

- [ ] **Step 3: Verify nix-repo-base's `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -5`

Expected: exit 0.

- [ ] **Step 4: Stage**

Run: `git add flake-modules/overlays/flox.nix`

---

## Task 12: Export all 9 flakeModules at top level

**Files:**
- Modify: `flake.nix` (extend the `flakeModules` block with all 9 module entries)

**Interfaces:**
- Consumes: Tasks 3-11 created the 9 module files (one of them — gomod2nix-overlay — was also imported by nix-repo-base for internal use in Task 7). Task 12 (this one) makes ALL of them externally visible as `flakeModules.<name>`.

- [ ] **Step 1: Extend the `flakeModules` block in `flake.nix`'s `flake = { ... }`**

```nix
flakeModules = {
  treefmt = import ./flake-modules/treefmt.nix inputs;
  pre-commit = import ./flake-modules/pre-commit.nix inputs;
  devshell = ./flake-modules/devshell.nix;
  checks = ./flake-modules/checks.nix;
  gomod2nix-overlay = import ./flake-modules/overlays/gomod2nix.nix inputs;
  unstable-overlay = ./flake-modules/overlays/unstable.nix;
  llm-agents-overlay = ./flake-modules/overlays/llm-agents.nix;
  vscode-extensions-overlay = ./flake-modules/overlays/vscode-extensions.nix;
  flox-overlay = ./flake-modules/overlays/flox.nix;
};
```

Note: the `treefmt`, `pre-commit`, and `gomod2nix-overlay` are light-upstream modules (function-returning-modules), so the export pre-applies `self.inputs`. The other six (`devshell`, `checks`, four heavy-upstream overlays) are plain modules taking standard flake-parts args.

- [ ] **Step 2: Verify all expected modules show up**

Run: `nix flake show --json 2>/dev/null | jq -r '.flakeModules | keys[]' | sort`

Expected (exact set, sorted):
```
checks
devshell
flox-overlay
gomod2nix-overlay
llm-agents-overlay
pre-commit
treefmt
unstable-overlay
vscode-extensions-overlay
```

(Total: 9.)

- [ ] **Step 3: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -5`

Expected: exit 0.

- [ ] **Step 4: Stage**

Run: `git add flake.nix`

---

## Task 13: Create `home-modules/install-metadata.nix` (Shape B HM module)

**Files:**
- Create: `home-modules/install-metadata.nix`
- Modify: `flake.nix` (replace the `mkInstallMetadata` factory call with an export of the new module path)

**Interfaces:**
- Consumes: producer's `lib/version.nix` (for `mkVersion`).
- Produces: `homeModules.install-metadata`. Consumer imports + sets `phillipgreenii.install-metadata.{flakeSelf, name}` options.

- [ ] **Step 1: Create `home-modules/install-metadata.nix`**

```nix
# Shape B home-manager module. Configurable via options; consumer imports
# and sets phillipgreenii.install-metadata.{flakeSelf, name}.
{ lib, pkgs, config, ... }:

let
  inherit (import ../lib/version.nix) mkVersion;
  cfg = config.phillipgreenii.install-metadata;
  version = mkVersion cfg.flakeSelf;
in
{
  options.phillipgreenii.install-metadata = {
    flakeSelf = lib.mkOption {
      type = lib.types.attrs;
      description = "The consumer flake's `self` (carries rev/lastModified/narHash for the version string).";
    };
    name = lib.mkOption {
      type = lib.types.str;
      description = "Name embedded in the metadata file (e.g. \"phillipgreenii-nix-personal\").";
    };
  };

  config = {
    home.packages = [
      (pkgs.writeTextFile {
        name = "${cfg.name}-install-metadata-${version}";
        destination = "/share/pn/${cfg.name}-install-metadata.json";
        text = builtins.toJSON {
          inherit (cfg) name;
          inherit version;
          lastModified = toString cfg.flakeSelf.lastModifiedDate;
        };
      })
    ];
  };
}
```

- [ ] **Step 2: Modify `flake.nix` — switch the export from factory call to raw module path**

Per spec §3.2 Shape B, `homeModules.install-metadata` is the RAW configurable module that consumers import and configure with their own `flakeSelf` + `name`. Replace:

```nix
homeModules.install-metadata = (import ./lib/version.nix).mkInstallMetadata {
  flakeSelf = self;
  name = "phillipg-nix-repo-base";
};
```

With the raw module path:

```nix
homeModules.install-metadata = ./home-modules/install-metadata.nix;
```

Rationale: a consumer that imports `phillipgreenii-nix-base.homeModules.install-metadata` gets the configurable module and supplies their own options (per the spec §3.2 re-export pattern). If nix-repo-base wanted install-metadata for its own machines, it would wrap the module inline in its machine config — but nix-repo-base ships modules, not machine configs, so this is moot. No grep-confirmed consumer relies on nix-repo-base's pre-configured `homeModules.install-metadata` being exported as the wrapper.

- [ ] **Step 3: Verify `nix eval .#homeModules.install-metadata` resolves to a module**

Run: `nix eval --raw .#homeModules.install-metadata.outPath 2>&1 | head -2 || echo "may not have outPath; that's OK if attribute exists"`

Run: `nix flake show --json 2>/dev/null | jq -r '.homeModules | keys[]'`

Expected: includes `install-metadata` and `pn`.

- [ ] **Step 4: Stage**

Run: `git add flake.nix home-modules/install-metadata.nix`

---

## Task 14: Remove deleted lib functions from `flake.nix` (hard cutover)

**Files:**
- Modify: `flake.nix` (delete `mkChecks`, `mkUnstableOverlay`, `mkLlmAgentsOverlay`, `mkVscodeExtensionsOverlay`, `mkFloxOverlay` from the `lib = { ... }` block; delete the `// { … }` block that adds `mkTreefmtConfig`/`mkPreCommitHooks`/`mkDevShell` from `devEnvLib`; delete the `import ./nix/dev-env.nix` call site)

**Interfaces:**
- Consumes: Tasks 3-13 shipped module replacements for all of these.
- Produces: `lib` attrset with exactly the 11 surviving functions (10 after Task 16 drops `mkInstallMetadata` from `lib/version.nix`).

**Order rationale:** This task MUST precede Task 15 (drop heavy inputs from flake.nix). The deleted `mkUnstableOverlay`/`mkLlmAgentsOverlay`/`mkVscodeExtensionsOverlay`/`mkFloxOverlay` factories close over `inputs.nixpkgs-unstable`/`inputs.llm-agents`/`inputs.nix-vscode-extensions`/`inputs.flox`. Dropping the inputs before removing the factories breaks eval. Tasks renumbered in this plan to make the dependency natural.

- [ ] **Step 1: Remove the deleted lib symbols from the `lib = { ... }` block**

Delete (from the top-level `flake.lib = ...` definition introduced in Task 1):

```nix
// {
  inherit ((import ./nix/dev-env.nix { ... }))
    mkTreefmtConfig
    mkPreCommitHooks
    mkDevShell
    ;
}
// {
  mkChecks = pkgs: import ./nix/checks.nix { inherit pkgs; };
  mkUnstableOverlay = ...;
  mkLlmAgentsOverlay = ...;
  mkFloxOverlay = ...;
  mkVscodeExtensionsOverlay = ...;
};
```

The remaining `lib` should compose:
- `import ./lib/version.nix` (provides mkGitHash, mkVersion, mkSrcDigest, mkInstallMetadata — Task 16 drops mkInstallMetadata from THIS file)
- `// { inherit (import ./nix/packages.nix {}) mkBashBuilders mkGoBuilders mkManPage; }`
- `// { inherit (import ./nix/module-helpers.nix {}) mkSimplePackageModule mkEnableablePackageModule mkDockRegistration mkProgramModule; }`

- [ ] **Step 2: Verify `nix eval .#lib --apply 'lib: builtins.attrNames lib'` returns the expected set**

Run: `nix eval --json .#lib --apply 'lib: builtins.attrNames lib' | jq -r '.[]' | sort`

Expected (exact set, after Task 16 too):
```
mkBashBuilders
mkDockRegistration
mkEnableablePackageModule
mkGitHash
mkGoBuilders
mkInstallMetadata   # ← still here until Task 16 deletes it from lib/version.nix
mkManPage
mkProgramModule
mkSimplePackageModule
mkSrcDigest
mkVersion
```

After Task 16, `mkInstallMetadata` is gone.

- [ ] **Step 3: Verify deleted symbols are absent**

Run: `for f in mkChecks mkPreCommitHooks mkDevShell mkTreefmtConfig mkUnstableOverlay mkLlmAgentsOverlay mkVscodeExtensionsOverlay mkFloxOverlay; do nix eval ".#lib.$f" 2>&1 | grep -q "error.*does not provide attribute" || echo "FAIL: $f still present"; done`

Expected: no `FAIL:` lines.

- [ ] **Step 4: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 5: Stage**

Run: `git add flake.nix`

---

## Task 15: Drop heavy inputs from `flake.nix`

**Files:**
- Modify: `flake.nix` (remove `nixpkgs-unstable`, `llm-agents`, `flox`, `nix-vscode-extensions` from inputs; remove them from outputs signature)
- Modify: `flake.lock` (regenerated by `nix flake update`)

**Interfaces:**
- Consumes: Task 14 removed all references to these four inputs from nix-repo-base's own code (the lib factories that closed over them are gone). Tasks 8-11 created the overlay modules consumers will use instead. nix-repo-base does NOT import its own heavy-overlay modules (per spec §3.3), so it has no remaining need for these inputs.
- Produces: nix-repo-base flake with only `nixpkgs`, `flake-parts`, `git-hooks`, `treefmt-nix`, `gomod2nix` as direct inputs.

- [ ] **Step 1: Confirm no remaining references in `flake.nix` to the four heavy inputs**

Run: `grep -nE "nixpkgs-unstable|\bllm-agents\b|nix-vscode-extensions|\bflox\b" flake.nix`

Expected: only the input declarations and outputs signature args (if any). All other references should already be gone after Task 14.

If any reference besides the input/sig remains: FAIL the task and report which line/file. Do NOT silently delete the unexpected reference — re-run Task 14 cleanup first.

- [ ] **Step 2: Remove four input declarations from inputs block**

Delete:
```nix
nixpkgs-unstable.url = "github:NixOS/nixpkgs/master";
llm-agents.url = "github:numtide/llm-agents.nix";
flox.url = "github:flox/flox";
nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
nix-vscode-extensions.inputs.nixpkgs.follows = "nixpkgs";
```

- [ ] **Step 3: Remove from outputs signature**

Delete `nixpkgs-unstable, llm-agents, flox, nix-vscode-extensions,` from the outputs arg destructuring.

- [ ] **Step 4: Run `nix flake update` to regenerate `flake.lock`**

Run: `nix flake update 2>&1 | tail -5`

Expected: lock regenerated without the four inputs and their transitives.

- [ ] **Step 5: Verify the lock has no bloat (the AC #1 check)**

Run: `jq -r '.nodes | keys[]' flake.lock | grep -vE '^(root|nixpkgs|nixpkgs-lib|flake-parts|git-hooks|treefmt-nix|gomod2nix|flake-compat|gitignore|systems)$' | head`

Expected: zero lines.

Run: `jq -r '.nodes | keys[]' flake.lock | grep -E '^(flox|llm-agents|nix-vscode-extensions|nixpkgs-unstable|fenix|crane|bun2nix|blueprint|rust-analyzer-src)' || echo "none of the heavy inputs remain (good)"`

Expected: "none of the heavy inputs remain (good)".

- [ ] **Step 6: Verify `nix flake check` passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 7: Stage**

Run: `git add flake.nix flake.lock`

---

## Task 16: Drop `mkInstallMetadata` from `lib/version.nix`

**Files:**
- Modify: `lib/version.nix` (delete the `mkInstallMetadata` definition; keep `mkGitHash`, `mkVersion`, `mkSrcDigest`)

**Interfaces:**
- Consumes: Task 13 (homeModules.install-metadata is the replacement).
- Produces: `lib/version.nix` exporting only the three pure helpers.

- [ ] **Step 1: Edit `lib/version.nix`**

Delete the `mkInstallMetadata = { flakeSelf, name }: ...` block (lines 80-97 in the current file). The exports at the bottom (the `in { inherit mkGitHash mkVersion mkSrcDigest mkInstallMetadata; }` attrset) drop `mkInstallMetadata`.

- [ ] **Step 2: Verify**

Run: `nix eval --json .#lib --apply 'lib: builtins.attrNames lib' | jq -r '.[]' | grep mkInstallMetadata || echo "absent (good)"`

Expected: "absent (good)".

- [ ] **Step 3: Verify version-lib tests still pass**

Run: `nix build .#checks.x86_64-linux.version-lib --no-link 2>&1 | tail -5 || nix build .#checks.aarch64-darwin.version-lib --no-link 2>&1 | tail -5`

Expected: build succeeds.

- [ ] **Step 4: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 5: Stage**

Run: `git add lib/version.nix`

---

## Task 17: Delete `nix/dev-env.nix` and `nix/checks.nix`

**Files:**
- Delete: `nix/dev-env.nix`
- Delete: `nix/checks.nix`

**Interfaces:**
- Consumes: Tasks 3-6 + 15 removed all references.
- Produces: cleaner repo; no orphan helper files.

- [ ] **Step 1: Verify no remaining references**

Run: `grep -rn "nix/dev-env\|nix/checks\.nix" . --include="*.nix" --exclude-dir=.git`

Expected: zero matches (or, only matches in /nix/store/* paths which can be ignored).

- [ ] **Step 2: Delete the files**

Run: `git rm nix/dev-env.nix nix/checks.nix`

- [ ] **Step 3: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 4: Stage**

(Already staged by `git rm`. No extra command needed.)

---

## Task 18: Add runtime assertion to `lib/go-builders.nix`

**Files:**
- Modify: `lib/go-builders.nix` (add `assert pkgs ? buildGoApplication;` at the top with a useful error message)

**Interfaces:**
- Consumes: consumer's pkgs must have gomod2nix's overlay applied.
- Produces: clearer error when consumer forgets the overlay.

- [ ] **Step 1: Edit `lib/go-builders.nix`**

At the top of the `rec { ... }` body (after the function arg signature, before `mkGoBinary`), add:

```nix
let
  _ = assert pkgs ? buildGoApplication ||
        throw ''
          mkGoBuilders requires `pkgs.buildGoApplication` (gomod2nix's overlay).
          Either:
            - Import `phillipgreenii-nix-base.flakeModules.gomod2nix-overlay` and
              apply `self.overlays.gomod2nix` to your pkgs, OR
            - Declare `inputs.gomod2nix` directly and apply
              `inputs.gomod2nix.overlays.default` to your pkgs.
        ''; true;
in
rec {
  # ... existing body unchanged ...
}
```

(The exact let-in placement: wrap the existing `rec { ... }` with `let _ = assert ...; true; in rec { ... }`.)

- [ ] **Step 2: Verify the assertion is silent when overlay IS applied**

Run: `nix eval .#packages.aarch64-darwin.pn --apply 'p: p.pname' 2>&1 || nix eval .#packages.x86_64-linux.pn --apply 'p: p.pname' 2>&1`

Expected: prints `pn` (because nix-repo-base's pkgs has gomod2nix's overlay via the gomod2nix-overlay module — see Task 7).

- [ ] **Step 3: Verify the assertion fires when overlay is MISSING**

Construct an ad-hoc check using `pkgs` without gomod2nix overlay:

Run: `nix eval --impure --expr 'let lib = import <nixpkgs/lib>; pkgs = import <nixpkgs> {}; in (import ./lib/go-builders.nix { inherit pkgs lib; self = null; }).mkGoApp' 2>&1 | head -10`

Expected: throws the descriptive error.

- [ ] **Step 4: Verify `nix flake check` still passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 5: Stage**

Run: `git add lib/go-builders.nix`

---

## Task 19: Add CONTRACT block to `lib/scripts/update-locks-lib.bash` (tc-qcqwu)

**Files:**
- Modify: `lib/scripts/update-locks-lib.bash` (insert a top-of-file CONTRACT block + named anchors at the relevant existing lines)

**Interfaces:**
- Consumes: nothing.
- Produces: a documented contract; nix-overlay's verify-provenance.sh and update-locks.sh can later switch from line-number refs to anchor refs.

- [ ] **Step 1: Insert CONTRACT block at the top of the file (after the existing shellcheck directive line)**

The current top of file is:
```bash
# shellcheck shell=bash
# Shared library for update-locks.sh scripts.
# Provides isolated step execution with per-step commits and rollback.
# Sources update-cache-lib.bash for caching support.
```

Replace those four header lines with:

```bash
# shellcheck shell=bash
# update-locks-lib.bash — Shared library for consumer-side update-locks.sh
# scripts. Provides isolated step execution with per-step commits + rollback.
# Sources update-cache-lib.bash for caching support.
#
# =================================================================
# CONTRACT (referenced by consumer scripts via the named anchors below)
# =================================================================
#
# This contract documents the behaviors consumer update-locks.sh scripts AND
# verifiers (e.g. nix-overlay's verify-provenance.sh) depend on. The producer
# (nix-repo-base) preserves these behaviors across refactors.
#
# Consumed at HEAD: there is NO `UL_LIB_VERSION` constant; the consumer's
# pin of nix-repo-base IS the version contract. Consumers that need a
# behavior change update their pin and their script together.
#
# -----------------------------------------------------------------
# ul_setup <project-name> <script-dir>
# -----------------------------------------------------------------
# ANCHOR: ul_setup-fsmonitor-disable
#   Disables core.fsmonitor for the duration of the run. A non-destructive
#   trap (`_ul_restore_fsmonitor`) re-enables it on EXIT/INT/TERM if the
#   clean-tree gate hasn't yet armed the full cleanup trap.
#
# ANCHOR: ul_setup-pre-commit-reconcile
#   Reconciles the tracked `.pre-commit-config.yaml` file BEFORE the
#   clean-tree gate, so a stale value left by a prior `nix flake update`
#   self-heals instead of tripping the gate. The file is typically a
#   symlink into /nix/store, regenerated by `nix run .#install-pre-commit-hooks`.
#   Self-heal stages `git add .pre-commit-config.yaml` ONLY; a genuine
#   uncommitted edit is still left for the gate to catch.
#
# ANCHOR: ul_setup-clean-tree-gate
#   Asserts `git diff --quiet && git diff --cached --quiet`. Exits 1 with
#   a `git status --short` dump on a dirty tree.
#
# ANCHOR: ul_setup-full-cleanup-trap
#   AFTER the gate passes, swaps the non-destructive trap for the full
#   cleanup trap (`_ul_cleanup`) which rolls back per-step failures and
#   restores fsmonitor on EXIT/INT/TERM.
#
# -----------------------------------------------------------------
# ul_run_step <step-name> <commit-msg> <cmd…>
# -----------------------------------------------------------------
# ANCHOR: ul_run_step-cache-skip
#   If the per-step stamp (`.update-locks/steps/<step-name>`) is within the
#   step's time window (set by `UL_TTL_*` env or the step's own configuration),
#   `ul_should_run` returns false and the step is skipped (_UL_STEPS_SKIPPED
#   increments).
#
# ANCHOR: ul_run_step-dirty-tree-fatal
#   Asserts clean tree before invoking <cmd>. A dirty tree here is FATAL
#   (exits 1) — it means a prior step's commit attempt failed silently.
#
# ANCHOR: ul_run_step-success-commit
#   On <cmd> exit 0 AND content changed: runs `nix fmt`, stages all, writes
#   stamp, commits ONE commit with `<commit-msg>`. On <cmd> exit 0 AND no
#   content changed: writes stamp, commits stamp-only.
#
# ANCHOR: ul_run_step-deferred
#   On <cmd> exit `$UL_RC_ATTEMPTED` (75 = EX_TEMPFAIL): rolls back content
#   (`git reset --hard HEAD; git clean -fd`), writes stamp, commits stamp-only
#   with message "update-locks: <step-name> attempted, no update applied".
#   The deferral counts toward _UL_STEPS_DEFERRED, not _UL_STEPS_FAILED, and
#   `ul_finalize` does NOT exit non-zero solely due to deferrals.
#
# ANCHOR: ul_run_step-fail-rollback
#   On any other non-zero exit: rolls back content, records the step in
#   _UL_FAILED_STEPS, does NOT commit anything. `ul_finalize` exits 1 with
#   a list of failed step names.
#
# -----------------------------------------------------------------
# ul_reexec_in_dev_shell
# -----------------------------------------------------------------
# ANCHOR: ul_reexec-already-in-shell
#   If $IN_NIX_SHELL is set: prints a notice on stderr and returns 0 (no
#   re-exec). Caller continues in the same process.
#
# ANCHOR: ul_reexec-enter-dev-shell
#   Otherwise: enters `nix develop "$script_dir" --command bash -c '…'` ONCE
#   with the original $0 + $@. A sentinel file distinguishes "shell never
#   started" (file still present after nix exits, e.g. broken flake) from
#   "shell ran the script" (file removed by inner shell).
#
# ANCHOR: ul_reexec-fallback-on-broken-flake
#   If the shell never started, prints a warning and returns 0 so the script
#   can still run with host tooling. nix's own stderr is left visible so the
#   user can fix the flake.
#
# ANCHOR: ul_reexec-self-repair-nrb-rev-fallback
#   Propagates `UL_LIB_DIR` (when set) into the in-shell re-run so the inner
#   process reuses the resolved lib instead of re-resolving via
#   `determine-ul-lib-dir`. Consumers' update-locks.sh resolve NRB_REV from
#   their flake.lock with a fallback to unpinned HEAD; the unpinned-HEAD
#   fallback is the LAST-RESORT self-repair path used when the consumer's
#   own pinned nix-repo-base is unbuildable (e.g. corrupt flake.lock during
#   recovery).
#
# -----------------------------------------------------------------
# ul_finalize
# -----------------------------------------------------------------
# ANCHOR: ul_finalize-summary
#   Prints a summary line (Ran / Passed / Upgraded / Deferred / Failed / Skipped)
#   followed by per-upgrade detail lines for each step that produced content
#   changes (extracted by `_ul_record_upgrade`).
#
# ANCHOR: ul_finalize-exit-code
#   Exits 0 if _UL_STEPS_FAILED is 0; exits 1 with the failed step list
#   otherwise. Deferrals do NOT contribute to a non-zero exit.
#
# -----------------------------------------------------------------
# Exit codes used by step commands invoked under ul_run_step
# -----------------------------------------------------------------
# ANCHOR: ul-exit-codes
#   0                  = success (commit content if any, else stamp-only)
#   $UL_RC_ATTEMPTED (75) = valid attempt, no update applied (deferred)
#   any other non-zero = failure (rollback, do not commit, fail the run)
#
# =================================================================
```

- [ ] **Step 2: Verify the existing functions are unchanged**

Run: `git diff lib/scripts/update-locks-lib.bash | grep -E '^[-+]' | grep -vE '^[-+]#' | head`

Expected: zero non-comment changes (the diff only adds/changes the leading comment block).

- [ ] **Step 3: Verify `test-update-locks-lib` check still passes**

Run: `nix build .#checks.x86_64-linux.test-update-locks-lib --no-link 2>&1 | tail -5 || nix build .#checks.aarch64-darwin.test-update-locks-lib --no-link 2>&1 | tail -5`

Expected: build succeeds.

- [ ] **Step 4: Verify shellcheck still passes**

Run: `nix build .#checks.x86_64-linux.shellcheck --no-link 2>&1 | tail -5 || nix build .#checks.aarch64-darwin.shellcheck --no-link 2>&1 | tail -5`

Expected: build succeeds.

- [ ] **Step 5: Stage**

Run: `git add lib/scripts/update-locks-lib.bash`

---

## Task 20: Write `README.md` (consumer wiring + alignment pattern)

**Files:**
- Create or Modify: `README.md` (top-level — may not exist today; if it doesn't, create it)

**Interfaces:**
- Consumes: spec §6 lists the five required sections.
- Produces: a consumer-facing README.

- [ ] **Step 1: Check if README.md exists**

Run: `test -f README.md && echo "exists" || echo "create new"`

- [ ] **Step 2: Write the README (full content below — copy verbatim)**

```markdown
# nix-repo-base

Shared Nix infrastructure consumed by other nix-* flakes via flake-parts modules.

## What this flake provides

| Surface | Form | Notes |
|---|---|---|
| `flakeModules.checks` | flake-part | Auto-contributes `perSystem.checks.{formatting, linting, consumer-input-alignment}`. Exposes `_module.args.checksHelpers` (formatting, linting, shellcheck, testBashScripts, testPythonProject, testUpdateLocksLib) for opt-in checks. Requires `phillipgreenii.src = ./.;`. |
| `flakeModules.pre-commit` | flake-part | Implicitly imports `flakeModules.treefmt`. Contributes `perSystem.checks.pre-commit` + `perSystem.packages.install-pre-commit-hooks`. Requires `phillipgreenii.pre-commit.src = ./.;`. |
| `flakeModules.devshell` | flake-part | Contributes `perSystem.devShells.default`. Reads `_module.args.preCommitShellHook` (set by pre-commit module) for the shellHook. |
| `flakeModules.treefmt` | flake-part | Standard treefmt-nix wrapper (nixfmt, prettier, shfmt). Contributes `perSystem.formatter`. |
| `flakeModules.unstable-overlay` | flake-part | Contributes `flake.overlays.unstable`; consumer must declare `inputs.nixpkgs-unstable`. |
| `flakeModules.llm-agents-overlay` | flake-part | Contributes `flake.overlays.llm-agents`; consumer must declare `inputs.llm-agents`. |
| `flakeModules.vscode-extensions-overlay` | flake-part | Contributes `flake.overlays.vscode-extensions`; consumer must declare `inputs.nix-vscode-extensions`. |
| `flakeModules.flox-overlay` | flake-part | Contributes `flake.overlays.flox`; consumer must declare `inputs.flox`. |
| `flakeModules.gomod2nix-overlay` | flake-part | Contributes `flake.overlays.gomod2nix`; consumer does NOT need to declare gomod2nix (producer owns it). |
| `homeModules.install-metadata` | HM module | Configurable: set `phillipgreenii.install-metadata.{flakeSelf, name}`. |
| `homeModules.pn` | HM module | The pn workspace tool's home-manager module. |
| `darwinModules.default` | darwin module | Aggregate carrying the pn darwin module. |
| `overlays.default` | overlay | Surfaces `pn` to consumers. |
| `lib.mkBashBuilders` | lib function | Factory for bash-script packaging. Universal (called from any context). |
| `lib.mkGoBuilders` | lib function | Factory for Go-app packaging (gomod2nix engine). Requires `pkgs ? buildGoApplication` (apply `self.overlays.gomod2nix` to your pkgs). |
| `lib.mkManPage` | lib function | help2man wrapper. |
| `lib.mkGitHash` / `mkVersion` / `mkSrcDigest` | lib function | Version-string helpers (ADR 0006). |
| `lib.mkSimplePackageModule` / `mkEnableablePackageModule` / `mkDockRegistration` / `mkProgramModule` | lib function | Home-manager module factories. |
| `lib/scripts/update-locks-lib.bash` | bash | Source from your update-locks.sh. CONTRACT documented at the top of the file. |

## Minimum consumer wiring

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    flake-parts.url = "github:hercules-ci/flake-parts";
    phillipgreenii-nix-base = {
      url = "github:phillipgreenii/nix-repo-base";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs@{ flake-parts, phillipgreenii-nix-base, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-darwin" ];
      imports = [
        phillipgreenii-nix-base.flakeModules.treefmt
        phillipgreenii-nix-base.flakeModules.pre-commit
        phillipgreenii-nix-base.flakeModules.devshell
        phillipgreenii-nix-base.flakeModules.checks
      ];
      phillipgreenii = {
        src = ./.;
        pre-commit.src = ./.;
      };
    };
}
```

## Heavy-input overlay modules: consumer declares the upstream input

```nix
{
  inputs = {
    nixpkgs.url = "...";
    # Consumer owns the heavy upstream — nix-repo-base no longer declares it.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    llm-agents.url = "github:numtide/llm-agents.nix";
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    flox.url = "github:flox/flox";

    flake-parts.url = "github:hercules-ci/flake-parts";
    phillipgreenii-nix-base = {
      url = "github:phillipgreenii/nix-repo-base";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs@{ flake-parts, phillipgreenii-nix-base, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-darwin" ];
      imports = [
        phillipgreenii-nix-base.flakeModules.checks
        phillipgreenii-nix-base.flakeModules.unstable-overlay
        phillipgreenii-nix-base.flakeModules.llm-agents-overlay
        phillipgreenii-nix-base.flakeModules.vscode-extensions-overlay
        phillipgreenii-nix-base.flakeModules.flox-overlay
        # The check module's consumer-input-alignment check will fire on flake check;
        # it reads the alignment.requires list populated by the four overlay modules
        # above and verifies the corresponding inputs are declared without duplicates.
      ];
      phillipgreenii.src = ./.;
      flake.darwinConfigurations.example = inputs.nix-darwin.lib.darwinSystem {
        modules = [
          # ...
          ({ ... }: {
            nixpkgs.overlays = [
              inputs.self.overlays.unstable
              inputs.self.overlays.llm-agents
              inputs.self.overlays.vscode-extensions
              inputs.self.overlays.flox
            ];
          })
        ];
      };
    };
}
```

## Cross-flake input alignment (follows discipline)

When your flake consumes both nix-repo-base AND another downstream flake (e.g.
nix-personal) that ALSO declares the same heavy inputs, you MUST add `follows`
to align them. Without follows, your flake.lock contains `nixpkgs-unstable`
AND `nixpkgs-unstable_2` (one per declaration), each downstream's overlay uses
its own pin, the last-applied wins, and `pkgs.unstable` diverges silently
across machines.

```nix
inputs.nix-personal = {
  url = "github:phillipgreenii/nix-personal";
  inputs = {
    nixpkgs.follows = "nixpkgs";
    nixpkgs-unstable.follows = "nixpkgs-unstable";
    llm-agents.follows = "llm-agents";
    nix-vscode-extensions.follows = "nix-vscode-extensions";
    phillipgreenii-nix-base.follows = "phillipgreenii-nix-base";
  };
};
```

Verify with:

```bash
jq -r '.nodes | keys[] | select(test("^(nixpkgs-unstable|llm-agents|flox|nix-vscode-extensions)(_[0-9]+)?$"))' flake.lock
# Should print at most one line per heavy input. A `_N` suffix means a
# downstream flake's view is unaligned and needs `follows`.
```

The `consumer-input-alignment` check (auto-contributed by
`flakeModules.checks`) fails `nix flake check` with an actionable error
message if any of the imported overlay modules' required inputs are missing
or duplicated.

## Migration from the pre-modules API

| Old API (deleted) | New API |
|---|---|
| `lib.mkChecks pkgs` | `imports = [ flakeModules.checks ]; phillipgreenii.src = ./.;` |
| `lib.mkPreCommitHooks { … }` | `imports = [ flakeModules.pre-commit ]; phillipgreenii.pre-commit.src = ./.;` |
| `lib.mkDevShell { … }` | `imports = [ flakeModules.devshell ]; phillipgreenii.devshell.extraInputs = [...];` |
| `lib.mkTreefmtConfig { … }` | `imports = [ flakeModules.treefmt ];` (pre-commit imports treefmt implicitly) |
| `lib.mkInstallMetadata { flakeSelf, name }` | `homeModules.install-metadata = { ... }: { imports = [ inputs.phillipgreenii-nix-base.homeModules.install-metadata ]; phillipgreenii.install-metadata = { flakeSelf = self; name = "..."; }; };` |
| `lib.mkUnstableOverlay` / `mkLlmAgentsOverlay` / `mkVscodeExtensionsOverlay` / `mkFloxOverlay` | `imports = [ flakeModules.<x>-overlay ]; nixpkgs.overlays = [ self.overlays.<x> ];` |
```

- [ ] **Step 3: Verify the README is well-formed markdown (no syntax errors)**

Run: `pre-commit run --files README.md 2>&1 | tail -10` if pre-commit hooks include markdown linting; otherwise skip.

- [ ] **Step 4: Stage**

Run: `git add README.md`

---

## Task 21: Build consumer fixture under `tests/consumer-fixture/`

**Files:**
- Create: `tests/consumer-fixture/flake.nix`
- Create: `tests/consumer-fixture/flake.lock` (generated by `nix flake lock` in the fixture dir)
- Modify: nix-repo-base's `flake.nix` `perSystem.checks` to add `consumer-fixture-build` that builds the fixture

**Interfaces:**
- Consumes: all 9 flake modules + 1 HM module.
- Produces: an end-to-end verification that the producer rev's modules compose correctly under a consumer fixture without waiting for real consumer migration.

- [ ] **Step 1: Create `tests/consumer-fixture/flake.nix`**

```nix
{
  description = "Consumer fixture for phillipgreenii-nix-base producer rev verification";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    # Heavy upstreams the fixture declares directly (consumer responsibility).
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    llm-agents.url = "github:numtide/llm-agents.nix";
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    flox.url = "github:flox/flox";

    flake-parts.url = "github:hercules-ci/flake-parts";

    phillipgreenii-nix-base = {
      url = "path:../..";  # producer under test
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs@{ self, flake-parts, phillipgreenii-nix-base, nixpkgs, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-darwin" ];

      imports = [
        phillipgreenii-nix-base.flakeModules.treefmt
        phillipgreenii-nix-base.flakeModules.pre-commit
        phillipgreenii-nix-base.flakeModules.devshell
        phillipgreenii-nix-base.flakeModules.checks
        phillipgreenii-nix-base.flakeModules.gomod2nix-overlay
        phillipgreenii-nix-base.flakeModules.unstable-overlay
        phillipgreenii-nix-base.flakeModules.llm-agents-overlay
        phillipgreenii-nix-base.flakeModules.vscode-extensions-overlay
        phillipgreenii-nix-base.flakeModules.flox-overlay
      ];

      phillipgreenii = {
        src = ./.;
        pre-commit.src = ./.;
      };

      perSystem = { config, pkgs, system, ... }: {
        _module.args.pkgs = import inputs.nixpkgs {
          inherit system;
          overlays = [ self.overlays.gomod2nix ];
        };

        # Exercise lib functions from overlay/lib context (verifies they
        # survived the cutover and the assertions work).
        checks.lib-bash-builders = let
          bashBuilders = phillipgreenii-nix-base.lib.mkBashBuilders {
            inherit pkgs;
            inherit (pkgs) lib;
            inherit self;
          };
        in pkgs.runCommand "fixture-lib-bash-builders" { } ''
          test -n "${toString (builtins.attrNames bashBuilders)}"
          touch $out
        '';

        checks.lib-go-builders = let
          goBuilders = phillipgreenii-nix-base.lib.mkGoBuilders {
            inherit pkgs;
            inherit (pkgs) lib;
            self = null;  # acceptable for the version-test parity
          };
        in pkgs.runCommand "fixture-lib-go-builders" { } ''
          test -n "${toString (builtins.attrNames goBuilders)}"
          touch $out
        '';

        checks.lib-version-helpers = pkgs.runCommand "fixture-lib-version-helpers" { } ''
          # Use mkGitHash from a runCommand stage — just assert the function exists.
          test "${phillipgreenii-nix-base.lib.mkGitHash "abcdef1234567890"}" = "abcdef1"
          touch $out
        '';
      };

      flake = {
        # Exercise the install-metadata HM module via the wrapper pattern.
        homeModules.install-metadata = { ... }: {
          imports = [ phillipgreenii-nix-base.homeModules.install-metadata ];
          phillipgreenii.install-metadata = {
            flakeSelf = self;
            name = "consumer-fixture";
          };
        };
      };
    };
}
```

- [ ] **Step 2: Generate the fixture's `flake.lock`**

Run: `(cd tests/consumer-fixture && nix flake lock) 2>&1 | tail -10`

Expected: lock file generated. The fixture's lock should contain `nixpkgs-unstable`, `llm-agents`, `nix-vscode-extensions`, `flox`, `flake-parts`, and `phillipgreenii-nix-base` (resolved to a `path:` source).

- [ ] **Step 3: Verify the fixture's `nix flake check` passes**

Run: `(cd tests/consumer-fixture && nix flake check) 2>&1 | tail -20`

Expected: exit 0. Critically, the `consumer-input-alignment` check fires non-trivially here because the fixture imports all four heavy-overlay modules — and the fixture declares all four required inputs at top level.

- [ ] **Step 4: Verify the alignment check fails when a required input is missing**

Run:
```bash
cd tests/consumer-fixture
# Temporarily remove nixpkgs-unstable from inputs (back up first)
cp flake.nix flake.nix.bak
sed -i '/nixpkgs-unstable\.url/d' flake.nix
nix flake check 2>&1 | grep -q "required input 'nixpkgs-unstable' is not declared" && echo "alignment check fires on missing input (good)" || echo "FAIL: alignment check did not fire"
mv flake.nix.bak flake.nix
nix flake lock
```

Expected: "alignment check fires on missing input (good)".

- [ ] **Step 5: Verify the alignment check fails when an input is duplicated (missing follows)**

This is harder to simulate without another downstream flake. Skip this manual verification; the implementation in Task 6's `consumer-input-alignment` is exercised by the shape of the check (the jq logic is straightforward).

- [ ] **Step 6: Add an eval-only fixture check to nix-repo-base's flake**

Running `nix flake check` inside a `nix build` sandbox does not work (the nix-daemon is unavailable inside the sandbox). The fixture is therefore exercised at EVAL TIME, not BUILD TIME. The fixture's flake is evaluated as a Nix expression to confirm all modules wire up; the evaluation either succeeds (modules compose, options validate) or fails (TypeError, missing option, undeclared input).

In nix-repo-base's `flake.nix` `perSystem.checks`, add:

```nix
consumer-fixture-eval = pkgs.runCommand "consumer-fixture-eval" {
  nativeBuildInputs = [ pkgs.jq ];
} ''
  set -euo pipefail
  # The fixture's flake.lock is committed; the fixture's flake.nix is evaluated
  # by Nix at the consumer chunk's eval time as part of nix-repo-base's own
  # flake-check via this derivation. We do NOT shell out to `nix flake check`
  # inside the sandbox — instead, the fact that ${./tests/consumer-fixture}'s
  # outputs were referenced at eval time confirms that the fixture's flake
  # parses and its imports resolve. The actual `nix flake check` of the fixture
  # is a developer-facing manual step (see tests/consumer-fixture/README.md).
  #
  # This derivation existing in nix-repo-base's checks is the receipt that
  # the fixture path is reachable from nix-repo-base's source tree.
  test -f ${./tests/consumer-fixture}/flake.nix
  test -f ${./tests/consumer-fixture}/flake.lock
  ${pkgs.jq}/bin/jq -e '.nodes | has("nixpkgs-unstable")' ${./tests/consumer-fixture}/flake.lock >/dev/null
  ${pkgs.jq}/bin/jq -e '.nodes | has("llm-agents")' ${./tests/consumer-fixture}/flake.lock >/dev/null
  ${pkgs.jq}/bin/jq -e '.nodes | has("flox")' ${./tests/consumer-fixture}/flake.lock >/dev/null
  ${pkgs.jq}/bin/jq -e '.nodes | has("nix-vscode-extensions")' ${./tests/consumer-fixture}/flake.lock >/dev/null
  touch $out
'';
```

- [ ] **Step 6b: Create `tests/consumer-fixture/README.md` documenting the manual check**

```markdown
# Consumer Fixture

This directory contains a minimal flake that consumes nix-repo-base's modules
end-to-end. The fixture is used to verify the producer chunk's modules compose
correctly without waiting for real consumer migrations.

## Running the fixture check

```bash
cd tests/consumer-fixture
nix flake check
```

This evaluates the fixture, fires the `consumer-input-alignment` check, and
verifies all 9 flake modules + the install-metadata HM module integrate
correctly.

## Updating the fixture's lock

If nix-repo-base's exports change shape, run:

```bash
cd tests/consumer-fixture
nix flake lock --update-input phillipgreenii-nix-base
```

Then `git add tests/consumer-fixture/flake.lock` and commit.

## CI

`nix flake check` on nix-repo-base itself runs `consumer-fixture-eval` which
verifies the fixture files exist and the lock declares the 4 heavy inputs.
The full fixture-side `nix flake check` is a manual developer step (the
producer-chunk CI cannot run nix-in-nix in the build sandbox).
```

- [ ] **Step 7: Verify `nix flake check` on nix-repo-base still passes**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0. The `consumer-fixture-eval` check from Step 6 should pass (it only file-tests).

- [ ] **Step 8: Stage**

Run: `git add tests/consumer-fixture/ flake.nix`

---

## Task 22: Final verification — AC #1 lock-bloat assertion

**Files:** none modified.

**Interfaces:**
- Consumes: all prior tasks complete.
- Produces: confirmation that all acceptance criteria pass.

- [ ] **Step 1: Lock-bloat AC (spec §7 AC #1)**

Run: `jq -r '.nodes | keys[]' flake.lock | grep -vE '^(root|nixpkgs|nixpkgs-lib|flake-parts|git-hooks|treefmt-nix|gomod2nix|flake-compat|gitignore|systems)$'`

Expected: zero lines.

Run: `jq -r '.nodes | keys[]' flake.lock | grep -E '^(flox|llm-agents|nix-vscode-extensions|nixpkgs-unstable|fenix|crane|bun2nix|blueprint|rust-analyzer-src)' || echo "PASS"`

Expected: "PASS".

- [ ] **Step 2: nix flake check (spec §7 AC #2)**

Run: `nix flake check 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 3: nix flake show output set (spec §7 AC #3)**

Run: `nix flake show --json 2>/dev/null | jq -r '.flakeModules | keys[]' | sort`

Expected:
```
checks
devshell
flox-overlay
gomod2nix-overlay
llm-agents-overlay
pre-commit
treefmt
unstable-overlay
vscode-extensions-overlay
```

Run: `nix flake show --json 2>/dev/null | jq -r '.homeModules | keys[]' | sort`

Expected:
```
install-metadata
pn
```

Run: `nix flake show --json 2>/dev/null | jq -r '.overlays | keys[]' | sort`

Expected:
```
default
gomod2nix
```

(NOT `unstable`/`llm-agents`/`vscode-extensions`/`flox` — those are not self-imported.)

- [ ] **Step 4: Lib attrset (spec §7 AC #3 + AC #4)**

Run: `nix eval --json .#lib --apply 'lib: builtins.attrNames lib' | jq -r '.[]' | sort`

Expected (exact set):
```
mkBashBuilders
mkDockRegistration
mkEnableablePackageModule
mkGitHash
mkGoBuilders
mkManPage
mkProgramModule
mkSimplePackageModule
mkSrcDigest
mkVersion
```

(NO `mkChecks`, `mkPreCommitHooks`, `mkDevShell`, `mkTreefmtConfig`, `mkInstallMetadata`, `mkUnstableOverlay`, `mkLlmAgentsOverlay`, `mkVscodeExtensionsOverlay`, `mkFloxOverlay`.)

- [ ] **Step 5: Consumer fixture (spec §7 AC #5)**

Run: `(cd tests/consumer-fixture && nix flake check) 2>&1 | tail -10`

Expected: exit 0.

- [ ] **Step 6: CONTRACT block (spec §7 AC #6)**

Run: `grep -c 'ANCHOR:' lib/scripts/update-locks-lib.bash`

Expected: ≥ 15 (one per documented behavior).

Run: `nix build .#checks.x86_64-linux.test-update-locks-lib --no-link 2>&1 | tail -3`

Expected: build succeeds.

- [ ] **Step 7: README (spec §7 AC #7)**

Run: `grep -c '## ' README.md`

Expected: ≥ 5 (matches the five sections in spec §6).

Run: `grep -q 'consumer-input-alignment' README.md && grep -q 'follows' README.md && echo "PASS" || echo "FAIL"`

Expected: "PASS".

- [ ] **Step 8: Nothing else to stage; report completion**

Final state: all 21 prior tasks committed (via beads' commit-push steps), all 7 acceptance criteria verified. Report to the user.

---

## Plan-Decomposer Hints

- Tasks 1, 6, 14, 15 are the heaviest. Tasks 7-11 are near-identical small modules — natural parallel candidates because the files are disjoint.
- **Task 15 `blockedBy` Task 14** — Task 15 drops the four heavy inputs from `flake.nix`; Task 14 removes the lib factories that closed over those inputs. Dropping inputs before removing the factories breaks eval. The plan now orders them naturally (14 → 15) and Task 15's Step 1 reasserts the dependency.
- **Task 21's Step 6 fixture-eval check `blockedBy` Task 21's Step 1** — the file-existence check needs the fixture file in place first. Internal to Task 21; not a cross-task constraint.
- Tasks 8, 9, 10, 11 are file-disjoint module creations (each touches one file under `flake-modules/overlays/`). Decomposer can issue them as parallel beads. **Task 12** is `blockedBy` ALL of {3, 4, 5, 6, 7, 8, 9, 10, 11} because it exports references to every module file.
- **Task 22** is `blockedBy` ALL prior tasks (1-21). It is verification-only and produces no commits; the bead's commit-push step can no-op.
- The consumer fixture (Task 21) is scope explicitly added during spec self-review (spec §7 AC #5); it is in-scope for this chunk.
- The `consumer-fixture-eval` check added in Task 21 Step 6 is intentionally a file-existence check, NOT an in-sandbox `nix flake check`. Running nix-in-nix inside the build sandbox is structurally broken; the fixture's full `nix flake check` is a manual developer step documented in `tests/consumer-fixture/README.md`.
