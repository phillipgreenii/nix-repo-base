{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-ws-nix";
  src = ./.;
  description = "Workspace-aware nix wrapper that injects --override-input for every project in pn-workspace.toml";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.yq-go
    pkgs.nix
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
    pkgs.yq-go
  ];
  inherit testSupport;
}
