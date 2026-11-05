# pg-nix-shim home-manager module: puts the pg-nix-shim package (bin/nix and
# bin/darwin-rebuild, both symlinks to pg-nix-log-wrapped) on the user's PATH so
# every `nix` and `darwin-rebuild` call is observed without a prefix.
#
# Placement: home-manager packages land in the per-user profile
# (`/etc/profiles/per-user/<user>`), a profile separate from the nix-darwin
# system profile, and `~/.nix-profile/bin` and the per-user profile are listed
# before `/run/current-system/sw/bin`. PATH order alone therefore gives the
# shim precedence over the system nix; there is no `lib.hiPrio` and no
# profile collision.
#
# Off by default, so importing the module changes nothing. Exported from the
# flake as a PATH (`homeModules.pg-nix-shim = ./home/pg-nix-shim/default.nix`),
# not an `import`, so the module system keys it by path and a consumer that
# pulls it in twice does not raise a duplicate-declaration error (the
# capability-framework precedent).
#
# `package` defaults to `pkgs.pg-nix-shim`, supplied by this flake's
# `overlays.default`; the nix-darwin module shares the same default so the
# explicit wrapper, the user shim and the system shim are one store binary.
#
# `extraVerbs` renders `~/.config/pn/shim.toml` (`verbs = [...]`) only when
# non-empty; those verbs are wrapped in addition to the built-in allowlist and
# never remove a default. With the module off nothing is added or written.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib)
    mkEnableOption
    mkPackageOption
    mkOption
    mkIf
    types
    ;
  cfg = config.phillipgreenii.pg-nix-shim;
  tomlFormat = pkgs.formats.toml { };
in
{
  options.phillipgreenii.pg-nix-shim = {
    enable = mkEnableOption "the pg-nix-shim `nix` and `darwin-rebuild` wrappers on the user PATH";
    package = mkPackageOption pkgs "pg-nix-shim" { };
    extraVerbs = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [
        "eval"
        "fmt"
      ];
      description = ''
        Extra `nix` verbs to wrap, in addition to the built-in allowlist.
        Rendered to `~/.config/pn/shim.toml` as `verbs = [...]` only when
        non-empty. They never remove a default verb.
      '';
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];
    home.file.".config/pn/shim.toml" = mkIf (cfg.extraVerbs != [ ]) {
      source = tomlFormat.generate "pg-nix-shim.toml" { verbs = cfg.extraVerbs; };
    };
  };
}
