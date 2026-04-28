{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-check";
  src = ./.;
  description = "Run pre-commit checks for all workspace repos";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.pre-commit
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
