# Build the jira binary via mkGoBuilders.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "jira";
  src = ./.;
  description = "Generic Atlassian Jira access tool (library + CLI)";
  gomod2nixToml = ./gomod2nix.toml;
}
