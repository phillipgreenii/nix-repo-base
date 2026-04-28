{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "pn-lib";
  src = ./.;
  description = "Shared library for pn store and workspace commands";
  inherit testSupport;
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.gawk
    pkgs.findutils
    pkgs.yq-go
  ];
}
