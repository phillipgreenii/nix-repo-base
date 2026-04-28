{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-apply";
  src = ./.;
  description = "Format and apply workspace configuration";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.nix
    pkgs.jq
    pkgs.nvd
    pkgs.yq-go
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.yq-go
  ];
  inherit testSupport;
}
