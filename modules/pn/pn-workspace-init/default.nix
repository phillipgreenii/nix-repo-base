{
  mkBashScript,
  pkgs,
  pn-discover-workspace,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-init";
  src = ./.;
  description = "Initialize a pn workspace directory";
  runtimeDeps = [
    pn-discover-workspace.script
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
