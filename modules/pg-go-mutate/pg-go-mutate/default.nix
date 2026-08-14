{
  mkBashScript,
  pkgs,
  pg-go-mutate-lib,
}:

mkBashScript {
  name = "pg-go-mutate";
  src = ./.;
  description = "Report which assertions a Go package's tests are missing";
  public = true;
  libraries = [ pg-go-mutate-lib ];
  # NOTE: gomu is deliberately NOT a runtimeDep. runtimeDeps are appended with
  # --suffix PATH, so an ambient ~/go/bin/gomu would win and defeat the pin. The
  # engine is bound by home/pg-go-mutate via makeWrapper --set (spec W9).
  runtimeDeps = [
    pkgs.jq
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.coreutils
  ];
  batsJobs = 4;
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
