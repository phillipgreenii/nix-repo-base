{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "pg-git-check-identity";
  src = ./.;
  description = "Reject a commit whose author or committer identity looks like a test/placeholder account";
  public = false;
  runtimeDeps = [ pkgs.git ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
  ];
}
