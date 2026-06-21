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
        inherit (topLevelCfg) src;
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
    in
    {
      _module.args.preCommitShellHook = preCommit.shellHook;
      checks.pre-commit = preCommit;
      packages.install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
        ${preCommit.shellHook}
        echo "Pre-commit hooks installed successfully!"
        echo "Run 'pre-commit run --all-files' to test them."
      '';
    };
}
