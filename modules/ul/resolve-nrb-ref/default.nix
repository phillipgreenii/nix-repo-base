{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "resolve-nrb-ref";
  src = ./.;
  description = "Print the flake.lock-pinned nix-repo-base flake ref; fail closed";
  public = true;
  runtimeDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
}
