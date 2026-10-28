# Eval-level test for flake-modules/overlays/flox.nix (bead pg2-y1z3fk): exercises the
# DEFAULT attribute (floxPkgs = flox's own packages output, so its closure matches
# cache.flox.dev) and the OPT-IN FALLBACK attribute (floxPkgsNoChecks = flox's overlay
# chain with nix's check phases disabled BEFORE flox's own overlay sees `nix`).
# The flox input is stubbed (the repo deliberately does not own that input) and the
# stub chain runs on the caller's nixpkgs, so no flox source, build or network is
# needed. Consumed by the flox-overlay-eval flake check.
{ pkgs }:
let
  system = pkgs.stdenv.hostPlatform.system;

  # Stand-in for flox/flox's own `packages` output: the cached closure.
  stubPackages = {
    flox = {
      marker = "cached-packages-output";
    };
  };

  stubFlox = {
    packages.${system} = stubPackages;
    inputs.nixpkgs.legacyPackages.${system} = pkgs;
    overlays = {
      # flox's deps overlay supplies `nix` with its check phases ON (the nixpkgs default).
      deps = _final: prev: {
        nix = prev.runCommand "stub-nix" {
          doCheck = true;
          doInstallCheck = true;
        } "touch $out";
      };
      # flox's own overlay builds `flox` from whatever `nix` it is handed; record what it saw.
      flox = _final: prev: {
        flox = {
          sawNixDoCheck = prev.nix.doCheck;
          sawNixDoInstallCheck = prev.nix.doInstallCheck;
        };
      };
      development = _final: _prev: { };
    };
  };

  module = import ../flake-modules/overlays/flox.nix { inputs.flox = stubFlox; };
  overlaid = module.flake.overlays.flox pkgs pkgs;
in
{
  # Default is flox's own packages output, untouched (same attrset, no rebuild chain).
  defaultIsFloxPackagesOutput = overlaid.floxPkgs == stubPackages;
  # Fallback is a separate set built from the rebuilt chain: its `flox` is not the
  # cached packages-output `flox` (that one carries `marker`).
  fallbackIsDistinctFromDefault =
    builtins.hasAttr "flox" overlaid.floxPkgsNoChecks
    && !(builtins.hasAttr "marker" overlaid.floxPkgsNoChecks.flox);
  # Fallback keeps the doCheck = false chain, applied before flox's overlay consumes `nix`.
  fallbackDisablesNixChecks =
    overlaid.floxPkgsNoChecks.nix.doCheck == false
    && overlaid.floxPkgsNoChecks.nix.doInstallCheck == false;
  fallbackOverrideAppliedBeforeFloxOverlay =
    overlaid.floxPkgsNoChecks.flox.sawNixDoCheck == false
    && overlaid.floxPkgsNoChecks.flox.sawNixDoInstallCheck == false;
  # Consumers must still declare inputs.flox.
  requiresFloxInput = module.phillipgreenii.alignment.requires == [ "flox" ];
}
