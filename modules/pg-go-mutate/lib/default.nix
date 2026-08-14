{
  mkBashLibrary,
  pkgs,
}:

mkBashLibrary {
  name = "pg-go-mutate-lib";
  src = ./.;
  description = "Guards, engine invocation and report transform for pg-go-mutate";
  # mkBashLibrary has no `runtimeDeps` parameter (that exists only on
  # mkBashScript, which wraps the command's PATH) -- a library is just
  # sourced text, so jq/findutils/gnused/gnugrep/coreutils are pulled onto
  # PATH by whichever mkBashScript consumes this library (Task 4), not here.
  # stdenv's initialPath already provides GNU coreutils/findutils/gnused/
  # gnugrep inside this check derivation's own sandbox, so shellcheck/bats
  # run correctly without listing them.
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
