# Build the pn binary via mkGoBuilders.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pn";
  src = ./.;
  description = "pn-workspace multi-repo Nix workflow tool";
  gomod2nixToml = ./gomod2nix.toml;
  # Build ONLY the pn entrypoint. The module now also carries
  # cmd/pn-workspace-toml-enforce (a separate tiny binary built by
  # ./enforce-toml.nix); without this, buildGoApplication would build every
  # cmd/* main and this package would ship the enforcer too. Pinning keeps the
  # pn derivation's bin/ to just `pn` (and its man page / completions).
  subPackages = [ "cmd/pn" ];
  runtimeDeps = [
    pkgs.nix
    pkgs.git
    # `pn workspace apply` runs `nvd diff <old> <new>` to show the generation
    # upgrade comparison, but only when nvd is on PATH (apply.go gates on
    # commandExists("nvd")). mkGoBinary wraps pn with `--suffix PATH` over
    # runtimeDeps, so nvd is reachable at runtime (a user's ambient nix/git
    # still win; nvd, which isn't ambient, is supplied as a fallback).
    pkgs.nvd
    # `pn osx tcc-check` shells out to `sqlite3` to probe Full Disk Access and
    # query the TCC database. Supplied here so the binary works on a host
    # without an ambient sqlite3 on PATH. Tests use a fake Runner and never
    # invoke the real binary, so this is a runtime-only dep (not testDeps).
    pkgs.sqlite
  ];
  testDeps = [
    pkgs.git
    pkgs.nix
  ];
}
