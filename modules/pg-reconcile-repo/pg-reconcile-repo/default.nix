{
  mkBashScript,
  pkgs,
  testSupport ? null,
}:

mkBashScript {
  name = "pg-reconcile-repo";
  src = ./.;
  description = "Rebase a diverged canonical clone in a temporary worktree, then swap its primary branch by compare-and-swap";
  public = false;
  # The shared git fixture harness (outside this src), passed in by scripts.nix.
  inherit testSupport;
  runtimeDeps = [
    pkgs.git
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.git
    pkgs.coreutils
  ];
}
