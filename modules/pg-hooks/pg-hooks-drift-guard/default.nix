{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-hooks-drift-guard";
  src = ./.;
  description = "Hook drift guard: fail when a source tree reintroduces a .githooks directory, a relative core.hooksPath, or config link code, outside an explicit allowlist";
  # Internal: only ever executed by the `hook-drift-guard` flake check that
  # flake-modules/pre-commit.nix contributes when driftGuard.enable is set. Not a
  # PATH command.
  public = false;
  inherit testSupport;
  # GNU grep and find are pinned so the check's behavior does not depend on the
  # host's BSD or GNU flavor. runtimeDeps are appended with --suffix, so an
  # environment that already has these keeps its own.
  runtimeDeps = [
    pkgs.gnugrep
    pkgs.findutils
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.gnugrep
    pkgs.findutils
    pkgs.coreutils
  ];
}
