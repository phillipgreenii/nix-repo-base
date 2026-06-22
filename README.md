# nix-repo-base

Shared Nix infrastructure consumed by other nix-\* flakes via flake-parts modules.

## What this flake provides

| Surface                                                                                              | Form          | Notes                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------------------------------------------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `flakeModules.checks`                                                                                | flake-part    | Auto-contributes `perSystem.checks.{formatting, linting, consumer-input-alignment}`. Exposes `_module.args.checksHelpers` (formatting, linting, shellcheck, testBashScripts, testPythonProject, testUpdateLocksLib) for opt-in checks. Requires `phillipgreenii.src = ./.;`. |
| `flakeModules.pre-commit`                                                                            | flake-part    | Implicitly imports `flakeModules.treefmt`. Contributes `perSystem.checks.pre-commit` + `perSystem.packages.install-pre-commit-hooks`. Requires `phillipgreenii.pre-commit.src = ./.;`.                                                                                       |
| `flakeModules.devshell`                                                                              | flake-part    | Contributes `perSystem.devShells.default`. Reads `_module.args.preCommitShellHook` (set by pre-commit module) for the shellHook.                                                                                                                                             |
| `flakeModules.treefmt`                                                                               | flake-part    | Standard treefmt-nix wrapper (nixfmt, prettier, shfmt). Contributes `perSystem.formatter`.                                                                                                                                                                                   |
| `flakeModules.unstable-overlay`                                                                      | flake-part    | Contributes `flake.overlays.unstable`; consumer must declare `inputs.nixpkgs-unstable`.                                                                                                                                                                                      |
| `flakeModules.llm-agents-overlay`                                                                    | flake-part    | Contributes `flake.overlays.llm-agents`; consumer must declare `inputs.llm-agents`.                                                                                                                                                                                          |
| `flakeModules.vscode-extensions-overlay`                                                             | flake-part    | Contributes `flake.overlays.vscode-extensions`; consumer must declare `inputs.nix-vscode-extensions`.                                                                                                                                                                        |
| `flakeModules.flox-overlay`                                                                          | flake-part    | Contributes `flake.overlays.flox`; consumer must declare `inputs.flox`.                                                                                                                                                                                                      |
| `flakeModules.gomod2nix-overlay`                                                                     | flake-part    | Contributes `flake.overlays.gomod2nix`; consumer does NOT need to declare gomod2nix (producer owns it).                                                                                                                                                                      |
| `homeModules.install-metadata`                                                                       | HM module     | Configurable: set `phillipgreenii.install-metadata.{flakeSelf, name}`.                                                                                                                                                                                                       |
| `homeModules.pn`                                                                                     | HM module     | The pn workspace tool's home-manager module.                                                                                                                                                                                                                                 |
| `darwinModules.default`                                                                              | darwin module | Aggregate carrying the pn darwin module.                                                                                                                                                                                                                                     |
| `overlays.default`                                                                                   | overlay       | Surfaces `pn` to consumers.                                                                                                                                                                                                                                                  |
| `lib.mkBashBuilders`                                                                                 | lib function  | Factory for bash-script packaging. Universal (called from any context).                                                                                                                                                                                                      |
| `lib.mkGoBuilders`                                                                                   | lib function  | Factory for Go-app packaging (gomod2nix engine). Requires `pkgs ? buildGoApplication` (apply `self.overlays.gomod2nix` to your pkgs).                                                                                                                                        |
| `lib.mkManPage`                                                                                      | lib function  | help2man wrapper.                                                                                                                                                                                                                                                            |
| `lib.mkGitHash` / `mkVersion` / `mkSrcDigest`                                                        | lib function  | Version-string helpers (ADR 0006).                                                                                                                                                                                                                                           |
| `lib.mkSimplePackageModule` / `mkEnableablePackageModule` / `mkDockRegistration` / `mkProgramModule` | lib function  | Home-manager module factories.                                                                                                                                                                                                                                               |
| `lib/scripts/update-locks-lib.bash`                                                                  | bash          | Source from your update-locks.sh. CONTRACT documented at the top of the file.                                                                                                                                                                                                |

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
        # pre-commit transitively pulls in treefmt; no separate treefmt import needed
        phillipgreenii-nix-base.flakeModules.pre-commit
        phillipgreenii-nix-base.flakeModules.devshell
        phillipgreenii-nix-base.flakeModules.checks
      ];
      # phillipgreenii.src and phillipgreenii.pre-commit.src default to your
      # flake root (inputs.self). Set them only if you want to scope
      # formatting/linting to a subdirectory.
    };
}
```

## Heavy-input overlay modules: consumer declares the upstream input

```nix
{
  inputs = {
    nixpkgs.url = "...";
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
      ];
      # phillipgreenii.src defaults to inputs.self (no setting needed).
    };
}
```

## Cross-flake input alignment (follows discipline)

When your flake consumes both nix-repo-base AND another downstream flake (e.g. nix-personal) that ALSO declares the same heavy inputs, you MUST add `follows` to align them. Without follows, your flake.lock contains `nixpkgs-unstable` AND `nixpkgs-unstable_2` (one per declaration), each downstream's overlay uses its own pin, the last-applied wins, and `pkgs.unstable` diverges silently across machines.

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
# Should print at most one line per heavy input. A _N suffix means a
# downstream flake's view is unaligned and needs follows.
```

The `consumer-input-alignment` check (auto-contributed by `flakeModules.checks`) fails `nix flake check` with an actionable error message if any of the imported overlay modules' required inputs are missing or duplicated.

## Migration from the pre-modules API

| Old API (deleted)                                   | New API                                                                             |
| --------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `lib.mkChecks pkgs`                                 | `imports = [ flakeModules.checks ];` (src defaults to inputs.self)                  |
| `lib.mkPreCommitHooks { … }`                        | `imports = [ flakeModules.pre-commit ];` (src defaults to inputs.self)              |
| `lib.mkDevShell { … }`                              | `imports = [ flakeModules.devshell ]; phillipgreenii.devshell.extraInputs = [...];` |
| `lib.mkTreefmtConfig { … }`                         | `imports = [ flakeModules.treefmt ];` (pre-commit imports treefmt implicitly)       |
| `lib.mkInstallMetadata { flakeSelf, name }`         | Import `homeModules.install-metadata` and set options.                              |
| `lib.mkUnstableOverlay` / `mkLlmAgentsOverlay` etc. | `imports = [ flakeModules.<x>-overlay ]; nixpkgs.overlays = [ self.overlays.<x> ];` |

## Repo tooling

| Script                          | Purpose                                                                                                                                                                                                                                                               |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `scripts/sync-repo-settings.sh` | Idempotently sync GitHub repo settings (merge modes, auto-merge, branch features) across the four `phillipgreenii/nix-*` repos to a canonical set. Run with `--dry-run` to audit only. Source of truth for tc-olcz3. Re-run when a new repo is added or to fix drift. |
