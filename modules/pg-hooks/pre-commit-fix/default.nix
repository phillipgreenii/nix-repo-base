{
  mkBashScript,
  pkgs,
  pg-hooks,
  testSupport ? null,
}:

mkBashScript {
  name = "pre-commit-fix";
  src = ./.;
  description = "Second name for `pg-hooks fix`: apply the repo's fixers to the staged files";
  public = true;
  inherit testSupport;
  # pg-hooks is a runtime FALLBACK (--suffix): a caller that already has
  # pg-hooks on PATH keeps its own. Agents and tool approvers match commands by
  # exact name, which is why this is its own wrapper and not an alias.
  runtimeDeps = [ pg-hooks ];
  testDeps = [
    pkgs.coreutils
  ];
}
