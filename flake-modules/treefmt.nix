# Light-upstream module: closes over the producer's own treefmt-nix input.
# Consumers import this module; they do NOT need to declare treefmt-nix
# themselves (it appears as a transitive node in their lock).
producerInputs:
{ ... }:
{
  imports = [ producerInputs.treefmt-nix.flakeModule ];

  perSystem = { pkgs, ... }: {
    treefmt = {
      projectRootFile = "flake.nix";
      # Generated nvfetcher manifests (_sources/) are tool-owned and regenerated;
      # never prettier/nixfmt them. Mirrors the pre-commit `excludes` default so a
      # single convention governs generated-path skipping. The producer has no
      # `_sources/`, so this is a no-op here; nvfetcher-using consumers get correct
      # behaviour with zero per-repo config. Definitions concatenate (extendable).
      settings.global.excludes = [ "_sources/*" ];
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
        shellcheck = {
          enable = true;
          # Single severity policy shared with the pre-commit shellcheck hook
          # and checksHelpers.shellcheck. Without it, treefmt defaults to
          # `style`, failing consumers' `nix flake check` on info/style findings
          # (incl. shellcheck false positives like SC2329 on indirectly-invoked
          # functions) that the hook tolerated — the inconsistency tc-neh26 fixes.
          severity = "warning";
        };
        shfmt = {
          enable = true;
          indent_size = 2;
        };
      };
    };
  };
}
