{
  mkBashScript,
  pkgs,
  pg-hooks-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-hooks";
  src = ./.;
  description = "Inspect and run the per-clone hook bundle: status, list, explain, run, fix";
  public = true;
  libraries = [ pg-hooks-lib ];
  inherit testSupport;
  # git and jq are required by the command itself (rev-parse, config, the
  # bundle's json files); grep applies the repo excludes in `fix` (grep -E). runtimeDeps are appended with --suffix, so a caller
  # environment that already has them keeps its own. prek is NOT here: it comes
  # from the bundle (or, for a legacy repo, from the ambient PATH).
  runtimeDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.jq
    pkgs.gnugrep
  ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.jq
    pkgs.gnugrep
  ];
}
