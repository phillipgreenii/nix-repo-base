# Shared bash library of guarded git/pn primitives for the `pnwf` subcommands.
{
  mkBashLibrary,
  pkgs,
}:

mkBashLibrary {
  name = "pnwf-lib";
  src = ./.;
  description = "pnwf: guarded git/pn primitives shared by every pnwf subcommand";
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
