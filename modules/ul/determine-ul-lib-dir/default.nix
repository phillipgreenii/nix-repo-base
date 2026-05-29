{
  mkBashScript,
  pkgs,
  update-locks-lib,
}:

mkBashScript {
  name = "determine-ul-lib-dir";
  src = ./.;
  description = "Resolve which copy of update-locks-lib.bash to source";
  public = true;
  runtimeDeps = [ pkgs.coreutils ];
  testDeps = [ pkgs.coreutils ];
  config = {
    UL_LIB_PACKAGE_PATH = "${update-locks-lib}/lib/scripts";
  };
}
