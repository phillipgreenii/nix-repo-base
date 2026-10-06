{
  mkBashScript,
  pkgs,
  pnwf-lib,
  testSupport ? null,
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
  # The shared git fixture harness (outside this src), passed in by scripts.nix.
  inherit testSupport;
  batsJobs = 8;
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
