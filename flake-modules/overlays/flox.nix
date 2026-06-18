# Heavy-upstream overlay module. Consumers must declare inputs.flox.
{ inputs, ... }:
{
  flake.overlays.flox = _final: prev: {
    floxPkgs = inputs.flox.packages.${prev.stdenv.hostPlatform.system};
  };

  phillipgreenii.alignment.requires = [ "flox" ];
}
