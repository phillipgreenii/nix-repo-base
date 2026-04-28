{
  mkBashScript,
  pn-workspace-update,
  pn-workspace-apply,
  testSupport ? null,
  ...
}:

mkBashScript {
  name = "pn-workspace-upgrade";
  src = ./.;
  description = "Complete workspace upgrade (update + apply)";
  runtimeDeps = [
    pn-workspace-update.script
    pn-workspace-apply.script
  ];
  inherit testSupport;
}
