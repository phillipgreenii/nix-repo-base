{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-tree";
  src = ./.;
  description = "Print ASCII dependency tree of workspace flake repos";
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
