# No producer-input closure needed — devshell uses only the consumer's pkgs.
#
# `extraInputs` is a PER-SYSTEM option: consumers set it inside their own
# `perSystem` using that system's `pkgs` (so packages are the correct platform).
# A previous top-level (`listOf package`, evaluated once) shape forced multi-system
# consumers to hardcode a single system's pkgs, which broke the devShell on every
# other platform (e.g. an x86_64-linux jq is unusable on aarch64-darwin). tc-persys.
_: {
  perSystem =
    {
      lib,
      config,
      pkgs,
      preCommitShellHook ? "",
      ...
    }:
    {
      options.phillipgreenii.devshell.extraInputs = lib.mkOption {
        type = lib.types.listOf lib.types.package;
        default = [ ];
        description = ''
          Additional packages added to the default devShell. Set this inside your
          `perSystem` using the per-system `pkgs` (e.g.
          `perSystem = { pkgs, ... }: { phillipgreenii.devshell.extraInputs = [ pkgs.jq ]; };`)
          so the packages match the building platform.
        '';
      };

      config.devShells.default = pkgs.mkShell {
        shellHook = preCommitShellHook;
        buildInputs = [
          pkgs.nixfmt
          pkgs.statix
          pkgs.deadnix
          pkgs.shellcheck
        ]
        ++ config.phillipgreenii.devshell.extraInputs;
      };
    };
}
