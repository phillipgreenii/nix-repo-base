# Light-upstream module: closes over the producer's git-hooks input.
# IMPORTS flake-modules/treefmt.nix because the pre-commit treefmt hook
# needs the formatter wrapper. Consumers who import pre-commit get treefmt
# automatically; they do NOT need to import treefmt separately.
producerInputs:
{
  lib,
  config,
  inputs,
  ...
}:
let
  topLevelCfg = config.phillipgreenii.pre-commit;
in
{
  imports = [ (import ./treefmt.nix producerInputs) ];

  options.phillipgreenii.pre-commit = {
    src = lib.mkOption {
      type = lib.types.path;
      default = inputs.self.outPath;
      defaultText = lib.literalExpression "inputs.self";
      description = ''
        Source path passed to git-hooks for hook registration. Defaults to the
        consumer's flake root; rarely needs overriding.
      '';
    };
    extraHooks = lib.mkOption {
      type = lib.types.either (lib.types.attrsOf lib.types.anything) (
        lib.types.functionTo (lib.types.attrsOf lib.types.anything)
      );
      default = { };
      description = ''
        Additional hooks merged into the standard set. Accepts either an
        attrset of hooks, or a function `pkgs -> attrset` that is applied with
        the per-system `pkgs` inside this module's `perSystem`. The function
        form lets hook `entry` store paths (e.g. host-native `go` /
        `golangci-lint`) follow the building/committing system instead of a
        single statically pinned system — so the committing machine can build
        the hook tooling for its own platform. See phillipgreenii-nix-agent-support
        for a function-form example.
      '';
    };
    excludes = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "^_sources/" ];
      description = ''
        File patterns (git-hooks/pre-commit regexes) excluded from ALL hooks
        (deadnix, end-of-file-fixer, trailing-whitespace, shellcheck, etc.).

        Defaults to nvfetcher's generated `_sources/` tree: those files are
        tool-generated and regenerated, so formatting/linting them is both wrong
        and unstable. The producer itself has no `_sources/`, so the default is a
        harmless no-op here while giving every nvfetcher-using consumer correct
        behaviour with zero per-repo config. Consumers can extend this list for
        other generated/vendored paths; definitions concatenate.
      '';
    };
  };

  config.perSystem =
    {
      config,
      pkgs,
      system,
      ...
    }:
    let
      # Module-aware golangci-lint entry. The stock git-hooks.nix
      # `hooks.golangci-lint` runs `golangci-lint run ./<dir>` from the repo
      # root, which fails here: this repo is MULTI-MODULE (go.mod lives in
      # modules/pn and modules/jira, not at the repo root), and golangci-lint
      # must be invoked from within the Go module. This entry instead walks each
      # changed .go file up to its nearest go.mod and lints that whole module
      # once. The repo-root .golangci.yml (discovered by walking up) pins the
      # linter set so this hook and a manual `golangci-lint run` agree.
      golangciLintEntry = pkgs.writeShellScript "precommit-golangci-lint" ''
        set -euo pipefail
        # Opt-in guard: this hook lints Go ONLY in a repo that ships a root
        # golangci config. git-hooks/pre-commit runs hooks from the repo root, so
        # $PWD here is the repo root. repo-base has one (it lints modules/pn +
        # modules/jira); consumers of flakeModules.pre-commit do NOT, so the hook
        # no-ops for them instead of linting their differently-maintained,
        # sometimes sandbox-unbuildable Go. (bd: pg2-q6i5)
        if [ ! -f .golangci.yml ] && [ ! -f .golangci.yaml ] && [ ! -f .golangci.toml ]; then
          exit 0
        fi
        golangci="${pkgs.golangci-lint}/bin/golangci-lint"
        # golangci-lint loads the full package graph via `go/packages`, so it
        # needs the `go` toolchain on PATH (matched to golangci-lint's build) and
        # writable module/build caches. Provide them here rather than relying on
        # the ambient shell so the hook works from `nix flake check` too.
        export PATH="${pkgs.go}/bin:$PATH"
        export GOFLAGS="-mod=mod"
        export GOCACHE="''${GOCACHE:-$(mktemp -d)/go-build}"
        export GOPATH="''${GOPATH:-$(mktemp -d)/go}"
        # Resolve each changed file to its owning go.mod directory, dedupe.
        mods=""
        for f in "$@"; do
          dir=$(dirname "$f")
          while [ "$dir" != "." ] && [ "$dir" != "/" ]; do
            if [ -f "$dir/go.mod" ]; then
              mods="$mods$dir"$'\n'
              break
            fi
            dir=$(dirname "$dir")
          done
        done
        [ -n "$mods" ] || exit 0
        rc=0
        for mod in $(printf '%s' "$mods" | sort -u); do
          ( cd "$mod" && "$golangci" run ./... ) || rc=1
        done
        exit $rc
      '';

      # Resolve the function-or-attrset extraHooks against the per-system pkgs so
      # a function-form definition (pkgs -> hooks) picks up the building system's
      # tooling. An attrset-form definition passes through unchanged.
      resolvedExtraHooks =
        if lib.isFunction topLevelCfg.extraHooks then
          topLevelCfg.extraHooks pkgs
        else
          topLevelCfg.extraHooks;
      preCommit = producerInputs.git-hooks.lib.${system}.run {
        # `excludes` becomes a top-level pre-commit `exclude` regex applied to
        # every hook (git-hooks modules/pre-commit.nix). Single source of truth
        # for generated-path exclusion — see the option doc above.
        inherit (topLevelCfg) src excludes;
        package = pkgs.prek;
        tools.dotnet-sdk = pkgs.runCommand "dotnet-stub" { } "mkdir $out";
        hooks = {
          treefmt = {
            enable = true;
            package = config.treefmt.build.wrapper;
          };
          statix = {
            enable = true;
            name = "statix";
          };
          deadnix = {
            enable = true;
            name = "deadnix";
          };
          # Severity matches the treefmt shellcheck formatter and
          # checksHelpers.shellcheck (all three = warning) so a single, consistent
          # policy governs shellcheck everywhere. error was too lenient (let
          # info/style findings pass the hook but fail `nix flake check`); style
          # was too strict (info-level false positives: bats subshell SC2030/2031,
          # source-following SC1091, indirectly-invoked SC2329). See tc-neh26.
          shellcheck = {
            enable = true;
            name = "shellcheck";
            args = [ "--severity=warning" ];
          };
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
          # Go linting. The stock git-hooks.nix golangci-lint hook is not
          # multi-module-aware (see golangciLintEntry above), so this uses a
          # custom module-aware entry while keeping the standard hook shape
          # (files, require_serial). package is wired to pkgs.golangci-lint.
          golangci-lint = {
            enable = true;
            name = "golangci-lint";
            package = pkgs.golangci-lint;
            entry = builtins.toString golangciLintEntry;
            files = "\\.go$";
            require_serial = true;
          };
        }
        // resolvedExtraHooks;
      };

      # ADR 0016: the git-hooks.nix-generated `.pre-commit-config.yaml` is a
      # symlink into `/nix/store` and MUST NOT be committed — a committed
      # store path is GC-eligible and rots into a dangling symlink, breaking
      # the hook. Enforce that every consumer gitignores it. Pure eval-time
      # read of the flake source's `.gitignore` (no IFD: `src` is an
      # already-realised store path); an exact full-line match avoids matching
      # the explanatory comment line.
      gitignorePath = topLevelCfg.src + "/.gitignore";
      gitignoreLines =
        if builtins.pathExists gitignorePath then
          lib.splitString "\n" (builtins.readFile gitignorePath)
        else
          null;
      ignoresPreCommitConfig =
        gitignoreLines != null
        && lib.any (l: lib.removeSuffix "\r" l == ".pre-commit-config.yaml") gitignoreLines;
      preCommitConfigGitignoredCheck =
        if gitignoreLines == null then
          throw "phillipgreenii.pre-commit: ${toString topLevelCfg.src}/.gitignore is missing; it MUST exist and ignore the generated .pre-commit-config.yaml store-symlink (ADR 0016 in phillipg-nix-repo-base)."
        else if !ignoresPreCommitConfig then
          throw "phillipgreenii.pre-commit: .gitignore MUST contain a line '.pre-commit-config.yaml'. The git-hooks.nix config is a generated /nix/store symlink and must not be committed (ADR 0016 in phillipg-nix-repo-base)."
        else
          pkgs.runCommand "pre-commit-config-gitignored" { } "touch $out";
    in
    {
      _module.args.preCommitShellHook = preCommit.shellHook;
      checks.pre-commit = preCommit;
      checks.pre-commit-config-gitignored = preCommitConfigGitignoredCheck;
      packages.install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
        ${preCommit.shellHook}
        echo "Pre-commit hooks installed successfully!"
        echo "Run 'pre-commit run --all-files' to test them."
      '';
    };
}
