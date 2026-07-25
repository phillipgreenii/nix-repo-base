{
  mkBashScript,
  pkgs,
  pnwf-lib,
}:

mkBashScript {
  name = "pnwf";
  src = ./.;
  description = "Deterministic helper for the workforest work-cycle (fork/validate/land/cleanup)";
  public = true;
  libraries = [ pnwf-lib ];
  runtimeDeps = [
    pkgs.git
    pkgs.jq
  ];
  # Every test isolates itself (own mktemp TEST_DIR + own MOCK_BIN), so the suite
  # is parallel-safe; run it under `bats --jobs 8` to cut the check's wall time
  # (bead pg2-nh1t3). The builder pulls in pkgs.parallel automatically when > 1.
  batsJobs = 8;
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
