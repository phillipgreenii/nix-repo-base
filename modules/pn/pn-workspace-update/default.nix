{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-update";
  src = ./.;
  description = "Update all flake dependencies";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.nix
    pkgs.jq
    pkgs.yq-go
    pkgs.git
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.yq-go
  ];
  inherit testSupport;
}
