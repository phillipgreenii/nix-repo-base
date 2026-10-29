# Pure script builders for the ul (update-locks) module.
# Mirrors modules/pn/scripts.nix.
{
  pkgs,
  bashBuilders,
  update-locks-lib,
}:
let
  determine-ul-lib-dir = pkgs.callPackage ./determine-ul-lib-dir {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs update-locks-lib;
  };

  resolve-nrb-ref = pkgs.callPackage ./resolve-nrb-ref {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    determine-ul-lib-dir
    resolve-nrb-ref
  ];
in
{
  inherit determine-ul-lib-dir resolve-nrb-ref;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-determine-ul-lib-dir = determine-ul-lib-dir.check;
    test-resolve-nrb-ref = resolve-nrb-ref.check;
  };

  check = pkgs.runCommand "test-ul-scripts" { } ''
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
