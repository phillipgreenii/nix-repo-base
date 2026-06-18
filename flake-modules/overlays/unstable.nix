# Heavy-upstream overlay module. Reads consumer's inputs.nixpkgs-unstable
# at evaluation time. Consumers must declare nixpkgs-unstable.url = ...;
# in their flake.nix; without it, evaluation fails with a clear error.
{ inputs, ... }:
{
  flake.overlays.unstable = _final: prev: {
    unstable = import inputs.nixpkgs-unstable {
      inherit (prev.stdenv.hostPlatform) system;
      config.allowUnfree = true;
    };
  };

  # Plumb requirement for the consumer-input-alignment check.
  phillipgreenii.alignment.requires = [ "nixpkgs-unstable" ];
}
