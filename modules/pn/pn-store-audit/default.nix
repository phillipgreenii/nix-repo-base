{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-store-audit";
  src = ./.;
  description = "Audit nix store usage across all profiles and devbox projects";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.nix
    pkgs.git
    pkgs.coreutils
    pkgs.gawk
    pkgs.jq
    pkgs.findutils
    pkgs.yq-go
  ];
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.gawk
    pkgs.findutils
    pkgs.yq-go
  ];
  inherit testSupport;
}
