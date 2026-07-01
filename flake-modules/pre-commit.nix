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
      type = lib.types.attrsOf lib.types.anything;
      default = { };
      description = "Additional hooks merged into the standard set.";
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
        }
        // topLevelCfg.extraHooks;
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
