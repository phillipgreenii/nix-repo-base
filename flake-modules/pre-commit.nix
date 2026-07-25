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
          # NOTE: Go linting is intentionally NOT a pre-commit hook. golangci-lint
          # must load the full package graph, which cannot be done offline in the
          # no-network `nix flake check` sandbox that runs checks.pre-commit
          # (bead pg2-6wly). It is instead a dedicated, sandbox-safe check per Go
          # module (checks.<module>-golangci) via gomod2nix's vendored dep env —
          # see lib/go-builders.nix `mkGoLint`. Repos wanting local commit/push-time
          # Go lint feedback can add their own hook via `extraHooks` (e.g. at
          # stages = [ "pre-push" ] to keep it out of the sandboxed check).
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

      # pg2-vqyw3: harden the generated pre-push hook against a MISSING config.
      # git-hooks.nix's installer (the `preCommit.shellHook` above) hardcodes
      # `prek install -c .pre-commit-config.yaml -t pre-push` with NO
      # `--allow-missing-config`, so the shim it writes runs
      # `prek hook-impl … --config=.pre-commit-config.yaml` and ABORTS every push
      # with "config file not found" whenever that config is absent — which is
      # ALWAYS true in a fresh checkout/worktree, because the config is the
      # gitignored, nix-generated /nix/store symlink (ADR 0016) that no commit
      # carries. That breaks pushes from pn's temp worktrees (pg2-x42j3).
      #
      # The framework exposes no option to thread install flags, so AFTER its
      # installer runs we RE-INSTALL just the pre-push shim with
      # `--allow-missing-config`, which bakes `--skip-on-missing-config` into it.
      # That flag ONLY no-ops the config-ABSENT branch; with a config present the
      # shim still runs every hook exactly as before (enforcement intact) —
      # unlike `--no-verify`. Defense-in-depth complementing pg2-m75sq.
      #
      # Regeneration-safe: it WRAPS the framework's opaque shellHook instead of
      # editing it, keys off the observable shim file (not the framework's
      # command text), and is idempotent — the grep guard skips the re-install
      # (and its filesystem churn under lorri/direnv) once the shim is already
      # tolerant, and the whole block no-ops when hook install is disabled or
      # pre-push is not a configured stage (no shim exists to harden).
      hardenPrePushHook = ''
        if ${lib.getExe' pkgs.git "git"} rev-parse --git-dir >/dev/null 2>&1; then
          _pgii_prepush="$(${lib.getExe' pkgs.git "git"} rev-parse --path-format=absolute --git-path hooks/pre-push 2>/dev/null || true)"
          if [ -n "$_pgii_prepush" ] && [ -e "$_pgii_prepush" ] \
            && ! grep -q -- '--skip-on-missing-config' "$_pgii_prepush"; then
            ${lib.getExe pkgs.prek} install -c .pre-commit-config.yaml --allow-missing-config -t pre-push -f
          fi
          unset _pgii_prepush
        fi
      '';
      # Single source of truth consumed by BOTH the devShell (via
      # `_module.args.preCommitShellHook`) and `install-pre-commit-hooks`, so the
      # tolerant pre-push shim is installed on either entry point.
      preCommitShellHook = ''
        ${preCommit.shellHook}
        ${hardenPrePushHook}
      '';
    in
    {
      _module.args.preCommitShellHook = preCommitShellHook;
      checks.pre-commit = preCommit;
      checks.pre-commit-config-gitignored = preCommitConfigGitignoredCheck;
      packages.install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
        ${preCommitShellHook}
        echo "Pre-commit hooks installed successfully!"
        echo "Run 'pre-commit run --all-files' to test them."
      '';

      # Autofix helper for the `statix` pre-commit hook. Runs `statix fix` over
      # the CURRENT working directory (or the paths given as args) — NOT ${./.},
      # which resolves to a read-only /nix/store copy statix can never write to.
      # Auto-contributed to every consumer that imports this flakeModule, so the
      # six hand-rolled per-repo copies (five of them broken with the ${./.} bug)
      # are deleted in favour of this single source of truth (bead pg2-7vhvn).
      packages.fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
        exec ${pkgs.lib.getExe pkgs.statix} fix "''${@:-.}"
      '';
    };
}
