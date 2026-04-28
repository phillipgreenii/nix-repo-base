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
    # sample-cmd is public (packages list has entries)
    # sample-cmd-with-lib is public (packages list has entries)
    # sample-internal is NOT public (packages list is empty)
    # Count total packages
    expected=4
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
