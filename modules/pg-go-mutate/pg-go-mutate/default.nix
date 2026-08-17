{
  mkBashScript,
  pkgs,
  pg-go-mutate-lib,
}:

mkBashScript {
  name = "pg-go-mutate";
  src = ./.;
  description = "Report which assertions a Go package's tests are missing";
  public = true;
  libraries = [ pg-go-mutate-lib ];
  # NOTE: gomu is deliberately NOT a runtimeDep. runtimeDeps are appended with
  # --suffix PATH, so an ambient ~/go/bin/gomu would win and defeat the pin. The
  # engine is bound by home/pg-go-mutate via makeWrapper --set (spec W9).
  #
  # The engine BINARY still cannot be resolved here — pkgs.phillipgreenii.gomu
  # does not exist in this flake's own pkgs (W9) — but the engine VERSION can,
  # because it is a plain string, not a package reference. Baking it makes the
  # unwrapped package self-protecting: a consumer who installs
  # `pkgs.pg-go-mutate` directly instead of importing homeModules.pg-go-mutate
  # sets neither PG_GO_MUTATE_GOMU nor PG_GO_MUTATE_GOMU_VERSION, so before this
  # the E1 assertion skipped itself on precisely the install path that also lacks
  # the W9 store-path binding. Single-sourced from ./gomu-pin.nix — see that file
  # for why it sits in `src` and who else reads it.
  config = {
    PGM_PINNED_GOMU_VERSION = import ./gomu-pin.nix;
  };
  runtimeDeps = [
    pkgs.jq
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.coreutils
  ];
  batsJobs = 4;
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
