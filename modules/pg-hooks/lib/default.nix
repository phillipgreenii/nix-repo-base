{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "pg-hooks-lib";
  src = ./.;
  description = "Bundle resolution, pointer validation, staleness stamp and message texts for pg-hooks";
  # The git fixture harness, the stub template and the shared test helper live
  # outside this directory; scripts.nix passes them in as `testSupport`.
  inherit testSupport;
  # mkBashLibrary has no runtimeDeps (that exists only on mkBashScript): a
  # library is sourced text, so jq/git/coreutils reach PATH through whichever
  # mkBashScript consumes it. These are the tools the tests need.
  testDeps = [
    pkgs.git
    pkgs.jq
    pkgs.coreutils
  ];
}
