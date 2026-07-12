# Proves mkBashScript injects config/exportedConfig values safely (pg2-92603):
#   1. shell metacharacters in a value are NOT executed — they reach the
#      variable verbatim (lib.escapeShellArg single-quotes the value).
#   2. a config attr name that is not a valid bash identifier fails at eval.
# The payload is a double-quote breakout (`"; echo PWNED #`) rather than a
# command substitution: escapeShellArg single-quotes it (shellcheck-clean, so
# the builder's buildPhase shellcheck passes), whereas a `$(…)`/backtick value
# would trip SC2016 and make the fixture script fail to build.
# Wired into flake `checks.bash-config-injection`.
{ pkgs }:
let
  inherit (pkgs) lib;
  bashBuilders = import ./bash-builders.nix {
    inherit pkgs lib;
    self = {
      rev = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
      lastModifiedDate = "20260101000000";
      narHash = "sha256-AAA";
    };
  };

  # Body echoes the injected value; the value tries to break out of the
  # double-quoted assignment context and run `echo PWNED`.
  evil = bashBuilders.mkBashScript {
    name = "inject";
    src = ./fixtures/inject;
    description = "config-injection fixture";
    config = {
      INJECT_EVIL = "\"; echo PWNED #";
    };
  };

  # Invalid bash identifier key must fail at eval. tryEval catches the
  # assertMsg throw; forcing .script.drvPath forces mkConfigLine.
  badName = builtins.tryEval (
    builtins.seq
      (bashBuilders.mkBashScript {
        name = "inject";
        src = ./fixtures/inject;
        description = "bad-name fixture";
        config = {
          "not-an-identifier" = "x";
        };
      }).script.drvPath
      true
  );
in
pkgs.runCommand "bash-config-injection"
  {
    evilBin = "${evil.script}/bin/inject";
    badNameSucceeded = if badName.success then "yes" else "no";
  }
  ''
    out_str="$("$evilBin")"
    printf 'script output: %s\n' "$out_str"

    # Escaped, the literal metacharacters survive verbatim in the value.
    if ! printf '%s' "$out_str" | grep -qF 'val="; echo PWNED #'; then
      echo "FAIL: value not injected verbatim — escaping missing"; exit 1
    fi
    # If the value were injected unescaped, the `; echo PWNED` would run and
    # emit a standalone PWNED line. It must not.
    if printf '%s\n' "$out_str" | grep -qx 'PWNED'; then
      echo "FAIL: injected command executed"; exit 1
    fi
    echo "OK: metacharacters injected verbatim, not executed"

    if [ "$badNameSucceeded" = yes ]; then
      echo "FAIL: invalid identifier config key did not fail eval"; exit 1
    fi
    echo "OK: invalid identifier rejected at eval"

    touch $out
  ''
