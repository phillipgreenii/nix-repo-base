{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-discover-workspace";
  src = ./.;
  description = "Discover workspace repos and their dependency order";
  runtimeDeps = [
    pkgs.nix
    pkgs.jq
    pkgs.git
  ];
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.nix
  ];
  inherit testSupport;
}
