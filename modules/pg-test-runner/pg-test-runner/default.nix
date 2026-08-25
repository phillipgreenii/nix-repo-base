{
  mkBashScript,
  pkgs,
}:
let
  # The shared machine-wide default (spec section 2.1). Rendered here via
  # pkgs.formats.json so the base package is immediately usable on its own
  # (`nix run .#pg-test-runner`) without any home-manager wrapping; the
  # home-manager module (home/pg-test-runner) exposes the SAME nix value as
  # its `phillipgreenii.pg-test-runner.config` option default, so a machine
  # customizing the option re-renders through the identical mechanism rather
  # than a second hand-maintained copy.
  defaultConfig = import ../config.nix;
  defaultConfigJson = (pkgs.formats.json { }).generate "pg-test-runner-config.json" defaultConfig;
in
mkBashScript {
  name = "pg-test-runner";
  src = ./.;
  description = "Label-driven, nix-free-at-runtime direct test runner";
  public = true;
  # Engine-internal tool resolution ONLY (config parsing, timeout bounding).
  # Deliberately NOT `runtimeDeps`: a runtimeDep is appended to PATH via
  # `--suffix` and would ALSO satisfy the per-project `tools` gate (section
  # 2.5) for any language whose `tools` list happens to include the same
  # name (jq, for the JS probe) -- masking a genuinely absent ambient tool.
  # Baking absolute paths as scalar `config` vars keeps those two concerns
  # separate: the engine always works, while the language `tools` check still
  # sees only the ambient PATH. Both vars have a bare-name fallback
  # (`${VAR:-jq}` / `${VAR:-timeout}`) in pg-test-runner.bash for raw-source
  # runs (bats "tests MUST work without nix build").
  config = {
    PG_TEST_RUNNER_DEFAULT_CONFIG = "${defaultConfigJson}";
    PG_TEST_RUNNER_JQ_BIN = "${pkgs.jq}/bin/jq";
    PG_TEST_RUNNER_TIMEOUT_BIN = "${pkgs.coreutils}/bin/timeout";
  };
  testDeps = [
    pkgs.jq
    pkgs.coreutils
    pkgs.bats
    pkgs.parallel
    pkgs.go
  ];
  batsJobs = 4;
}
