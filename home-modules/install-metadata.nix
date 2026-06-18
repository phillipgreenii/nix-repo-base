# Shape B home-manager module. Configurable via options; consumer imports
# and sets phillipgreenii.install-metadata.{flakeSelf, name}.
{
  lib,
  pkgs,
  config,
  ...
}:

let
  inherit (import ../lib/version.nix) mkVersion;
  cfg = config.phillipgreenii.install-metadata;
  version = mkVersion cfg.flakeSelf;
in
{
  options.phillipgreenii.install-metadata = {
    flakeSelf = lib.mkOption {
      type = lib.types.attrs;
      description = "The consumer flake's self (carries rev/lastModified/narHash for the version string).";
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
