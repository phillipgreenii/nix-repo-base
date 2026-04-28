{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-push";
  src = ./.;
  description = "Push all workspace repos to their remotes";
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
