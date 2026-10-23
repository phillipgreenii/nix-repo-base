{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pre-land-eval";
  src = ./.;
  description = "Eval-only pre-land check: evaluate a repo's flake checks, packages and devShells at a commit without building them";
  public = true;
  inherit testSupport;
  # nix itself is NOT a dependency: it is the machine's own nix (and
  # pg-nix-log-wrapped when present), resolved from PATH at run time, so the
  # eval uses the daemon and store of the caller. git reads the enablement key
  # and resolves the ref; grep and coreutils classify nix's stderr.
  # runtimeDeps are appended with --suffix, so a caller PATH keeps its own.
  runtimeDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.gnugrep
  ];
  # The tests put a recording nix shim and a uname shim first on PATH.
  testDeps = [
    pkgs.git
    pkgs.coreutils
    pkgs.gnugrep
  ];
}
