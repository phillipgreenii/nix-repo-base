{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-git-check-identity";
  src = ./.;
  description = "Reject a commit whose author or committer identity looks like a test/placeholder account";
  public = false;
  # The shared git fixture harness (outside this src), passed in by scripts.nix.
  inherit testSupport;
  runtimeDeps = [ pkgs.git ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
  ];
}
