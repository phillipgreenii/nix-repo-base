{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-status";
  src = ./.;
  description = "Show git status for all workspace repos";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.git
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
