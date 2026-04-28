{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-osx-tcc-check";
  src = ./.;
  description = "Check for duplicate TCC entries from Nix store path changes";
  runtimeDeps = [
    pkgs.sqlite
  ];
  testDeps = [
    pkgs.sqlite
  ];
  inherit testSupport;
}
