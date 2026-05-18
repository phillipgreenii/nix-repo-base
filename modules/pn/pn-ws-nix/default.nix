{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-ws-nix";
  src = ./.;
  description = "Workspace-aware nix wrapper that injects --override-input for every project in pn-workspace.toml";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.nix
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
