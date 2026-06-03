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
    # `pn workspace apply` runs `nvd diff <old> <new>` to show the generation
    # upgrade comparison, but only when nvd is on PATH (apply.go gates on
    # commandExists("nvd")). Ship it as a runtime dep so the diff actually
    # renders; like git, it surfaces into the profile via propagatedBuildInputs.
    pkgs.nvd
  ];
  testDeps = [
    pkgs.git
    pkgs.nix
  ];
}
