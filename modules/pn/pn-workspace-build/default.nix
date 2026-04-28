{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-build";
  src = ./.;
  description = "Format and build workspace configuration without activating";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.nix
    pkgs.jq
    pkgs.yq-go
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
    pkgs.yq-go
  ];
  inherit testSupport;
}
