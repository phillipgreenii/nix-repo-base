# Build the pg-go-mutate-tui binary via mkGoBuilders.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pg-go-mutate-tui";
  src = ./.;
  description = "Interactive, file-granular resumable mutation-testing TUI";
  gomod2nixToml = ./gomod2nix.toml;
}
