# Build the pn binary via mkGoBuilders.
{
  pkgs,
  self,
}:

let
  version = self.lib.mkVersion self;
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pn";
  src = ./.;
  inherit version;
  description = "pn-workspace multi-repo Nix workflow tool";
  vendorHash = "sha256-18WMBXrf57u/nU/mfFzZusfEgOYaxnx8/9vBzdnrVKU=";
  runtimeDeps = [
    pkgs.nix
    pkgs.git
  ];
  testDeps = [
    pkgs.git
    pkgs.nix
  ];
}
