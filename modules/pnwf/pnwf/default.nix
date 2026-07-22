{
  mkBashScript,
  pkgs,
  pnwf-lib,
}:

mkBashScript {
  name = "pnwf";
  src = ./.;
  description = "Deterministic helper for the workforest work-cycle (fork/validate/land/cleanup)";
  public = true;
  libraries = [ pnwf-lib ];
  runtimeDeps = [
    pkgs.git
    pkgs.jq
  ];
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
