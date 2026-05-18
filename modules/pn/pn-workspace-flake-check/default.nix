{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-ws-nix,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-flake-check";
  src = ./.;
  description = "Run nix flake check for all workspace repos via pn-ws-nix";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.yq-go
    pn-ws-nix.script
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
    pkgs.yq-go
  ];
  inherit testSupport;
}
