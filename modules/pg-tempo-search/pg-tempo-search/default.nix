{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "pg-tempo-search";
  src = ./.;
  description = "Search Tempo with an explicit time window: slices the range, splits truncated slices, merges and flattens the traces";
  public = true;
  runtimeDeps = [
    pkgs.curl
    pkgs.jq
    pkgs.coreutils
  ];
  # python3 runs the loopback stub Tempo server the tests talk to
  # (tests/stub-tempo.py); no live Tempo and no outside host is contacted.
  testDeps = [
    pkgs.curl
    pkgs.jq
    pkgs.coreutils
    pkgs.python3
  ];
  batsJobs = 4;
}
