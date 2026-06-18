# Heavy-upstream overlay module. Consumers must declare inputs.nix-vscode-extensions.
{ inputs, ... }:
{
  flake.overlays.vscode-extensions = _final: prev: {
    inherit (inputs.nix-vscode-extensions.extensions.${prev.stdenv.hostPlatform.system})
      vscode-marketplace
      open-vsx
      ;
  };

  phillipgreenii.alignment.requires = [ "nix-vscode-extensions" ];
}
