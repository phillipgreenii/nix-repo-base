{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-store-deepclean";
  src = ./.;
  description = "Clean old Nix profile generations, stale GC roots, and garbage collect the store";
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
