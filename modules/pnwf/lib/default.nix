# Shared bash library of guarded git/pn primitives for the `pnwf` subcommands.
{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "pnwf-lib";
  src = ./.;
  description = "pnwf: guarded git/pn primitives shared by every pnwf subcommand";
  # Every test isolates itself (own mktemp TEST_DIR + own MOCK_DIR), so the suite
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
