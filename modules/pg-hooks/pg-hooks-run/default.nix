{
  mkBashScript,
  pkgs,
  pg-hooks-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-hooks-run";
  src = ./.;
  description = "Per-clone hook bundle stage runner: run prek from the bundle for one git hook stage";
  # Internal: only ever executed from a bundle's bin/, by the static stubs in
  # <git-common-dir>/hooks. Not a PATH command, so no tldr page or completions.
  public = false;
  libraries = [ pg-hooks-lib ];
  inherit testSupport;
  # jq reads meta.json/source.json (stamp inputs, built_at, overrides). git and
  # coreutils are required by the runner itself. runtimeDeps are appended with
  # --suffix, so a hook environment that already has these keeps its own.
  runtimeDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.jq
  ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.jq
  ];
}
