{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-rebase";
  src = ./.;
  description = "Rebase all workspace repos with remote changes";
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
