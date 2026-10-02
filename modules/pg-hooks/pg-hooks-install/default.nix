{
  mkBashScript,
  pkgs,
  pg-hooks-lib,
  stubTemplate,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-hooks-install";
  src = ./.;
  description = "Per-clone hook bundle installer: root a bundle as a generation, swap the pointer, write the hook stubs";
  # Internal: only ever executed by `install-pre-commit-hooks` (flake-modules/
  # pre-commit.nix) when bundle.enable is set. Not a PATH command.
  public = false;
  libraries = [ pg-hooks-lib ];
  inherit testSupport;
  # The static stub template rendered per stage (@STAGE@). Injected as a local
  # variable; the raw-source tests pass the same variable in the environment.
  config.STUB_TEMPLATE = "${stubTemplate}";
  # jq reads the bundle's stages.json/meta.json and writes source.json; git and
  # coreutils (mktemp, mv, date) do the rest. nix-store is deliberately NOT here:
  # it is the user's own nix (PG_HOOKS_NIX_STORE_BIN overrides it for tests).
  # runtimeDeps are appended with --suffix, so an environment that already has
  # these keeps its own.
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
