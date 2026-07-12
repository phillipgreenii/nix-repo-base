# Forces a fixture's `.check` to build so `nix flake check` exercises the
# assembled-artifact coverage added for bead pg2-28wwb: (1) the mandatory floor
# smoke (`demo --version`/`-v` against ${script}/bin/${name}) and (2) the
# SCRIPT_UNDER_TEST bats suite, which drives the shipped artifact and asserts the
# injected config line ran. Regressing the injected version handler, the wrapper,
# the config injection, or the SCRIPT_UNDER_TEST wiring fails this check.
{ pkgs }:
let
  inherit (pkgs) lib;
  mk =
    (import ./bash-builders.nix {
      inherit pkgs lib;
      self = {
        rev = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        lastModifiedDate = "20260101000000";
        narHash = "sha256-AAA";
      };
    }).mkBashScript;
  demo = mk {
    name = "demo";
    src = ./fixtures/demo;
    description = "demo script for the assembled-artifact smoke test";
    config = {
      GREETING = "howdy";
    };
  };
in
demo.check
