# Build the pn-workspace-toml-enforce binary via mkGoBuilders. This is a separate,
# internal entrypoint in the SAME Go module as pn (cmd/pn-workspace-toml-enforce);
# it reuses internal/workspace's ParseConfig + orderedConfig writer to enforce the
# two nix-owned keys ([workspace].id + [hooks.apply].post) in pn-workspace.toml.
#
# Consumed by phillipg-nix-ziprecruiter's pn-workspace-toml home-manager module,
# which invokes it from a home.activation script. See docs/adr/0017 and
# phillipg-nix-ziprecruiter docs/adr/0049.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pn-workspace-toml-enforce";
  # Same module source as pn so the shared internal/workspace package builds.
  src = ./.;
  description = "Enforce the nix-owned pn-workspace.toml keys (workspace.id + hooks.apply.post)";
  gomod2nixToml = ./gomod2nix.toml;
  # Build ONLY this entrypoint (the module also carries cmd/pn).
  subPackages = [ "cmd/pn-workspace-toml-enforce" ];
  # Internal activation tool: no --help/man page, no shell completions.
  manPage = false;
  completions = {
    bash = false;
    zsh = false;
    fish = false;
  };
}
