# Test fixtures for bash-builders
#
# Builds sample libraries and scripts using bashBuilders and exports their checks.
{ bashBuilders, pkgs }:
let
  sample-lib = bashBuilders.mkBashLibrary {
    name = "sample-lib";
    src = ./sample-lib;
    description = "Sample library for testing mkBashLibrary";
  };

  sample-cmd = bashBuilders.mkBashScript {
    name = "sample-cmd";
    src = ./sample-cmd;
    description = "A sample command for testing mkBashScript";
  };

  sample-cmd-with-lib = bashBuilders.mkBashScript {
    name = "sample-cmd-with-lib";
    src = ./sample-cmd-with-lib;
    description = "Command that uses a shared library";
    libraries = [ sample-lib ];
  };

  sample-cmd-with-config = bashBuilders.mkBashScript {
    name = "sample-cmd-with-config";
    src = ./sample-cmd-with-config;
    description = "Command with config injection";
    runtimeDeps = [ pkgs.jq ];
    testDeps = [ pkgs.jq ];
    config = {
      SAMPLE_GREETING = "howdy";
      # Non-string scalar: must be inlined as SAMPLE_PORT=8080, not a JSON file
      # path (bead pg2-jucnb).
      SAMPLE_PORT = 8080;
      # Bool scalars: must inline as the literal true/false via lib.boolToString,
      # NOT toString's "1"/"" (bead pg2-jucnb).
      SAMPLE_FLAG = true;
      SAMPLE_FLAG_OFF = false;
      SAMPLE_CONFIG = {
        name = "testuser";
      };
    };
    exportedConfig = {
      SAMPLE_EXPORTED = "exported-value";
    };
  };

  sample-internal = bashBuilders.mkBashScript {
    name = "sample-internal";
    src = ./sample-internal;
    description = "An internal helper command";
    public = false;
  };

  # No tests/ directory: proves tests/ is optional/best-effort (bead pg2-d7vvp).
  # The script's check still runs the floor smoke; the library's still shellchecks.
  sample-cmd-no-tests = bashBuilders.mkBashScript {
    name = "sample-cmd-no-tests";
    src = ./sample-cmd-no-tests;
    description = "A sample command with no tests/ directory";
  };

  sample-lib-no-tests = bashBuilders.mkBashLibrary {
    name = "sample-lib-no-tests";
    src = ./sample-lib-no-tests;
    description = "A sample library with no tests/ directory";
  };

  sample-module = bashBuilders.mkBashModule {
    name = "sample";
    libraries = [ sample-lib ];
    scripts = [
      sample-cmd
      sample-cmd-with-lib
      sample-internal
    ];
  };

  test-sample-module = pkgs.runCommand "test-sample-module" { } ''
    # Each public script contributes exactly ONE package: since ADR 0011 the man
    # page is folded into the script derivation's installPhase, so mkBashScript
    # returns `packages = if public then [ script ] else [ ]` (one derivation per
    # public script). sample-cmd (public) + sample-cmd-with-lib (public) => 2;
    # sample-internal (public = false) contributes 0. (bead pg2-fqar3)
    expected=2
    actual=${toString (builtins.length sample-module.packages)}
    if [ "$actual" -ne "$expected" ]; then
      echo "FAIL: expected $expected public packages, got $actual"
      exit 1
    fi
    echo "PASS: module has $actual public packages"
    touch $out
  '';
in
{
  checks = {
    test-sample-lib = sample-lib.check;
    test-sample-cmd = sample-cmd.check;
    test-sample-cmd-with-lib = sample-cmd-with-lib.check;
    test-sample-cmd-with-config = sample-cmd-with-config.check;
    test-sample-internal = sample-internal.check;
    test-sample-cmd-no-tests = sample-cmd-no-tests.check;
    test-sample-lib-no-tests = sample-lib-no-tests.check;
    inherit test-sample-module;
  };

  inherit
    sample-lib
    sample-cmd
    sample-cmd-with-lib
    sample-cmd-with-config
    sample-internal
    sample-module
    ;
}
