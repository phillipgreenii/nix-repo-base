# Shared bash library of guarded git/pn primitives for the `pnwf` subcommands.
{
  mkBashLibrary,
  pkgs,
}:

mkBashLibrary {
  name = "pnwf-lib";
  src = ./.;
  description = "pnwf: guarded git/pn primitives shared by every pnwf subcommand";
  # Every test isolates itself (own mktemp TEST_DIR + own MOCK_DIR), so the suite
  # is parallel-safe; run it under `bats --jobs 8` to cut the check's wall time
  # (bead pg2-nh1t3). The builder pulls in pkgs.parallel automatically when > 1.
  batsJobs = 8;
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
