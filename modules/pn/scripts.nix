# Pure script builders for PN module
# Uses mkBashBuilders from phillipgreenii-nix-base
{
  pkgs,
  bashBuilders,
  searchDirs ? [ ],
}:
let
  testSupport = ./test-support;

  pn-lib = pkgs.callPackage ./pn-lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  pn-discover-workspace = pkgs.callPackage ./pn-discover-workspace {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };

  pn-osx-tcc-check = pkgs.callPackage ./pn-osx-tcc-check {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };

  pn-workspace-init = pkgs.callPackage ./pn-workspace-init {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-discover-workspace testSupport;
  };

  pn-workspace-apply = pkgs.callPackage ./pn-workspace-apply {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      pn-lib
      pn-discover-workspace
      testSupport
      ;
  };

  pn-workspace-build = pkgs.callPackage ./pn-workspace-build {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      pn-lib
      pn-discover-workspace
      testSupport
      ;
  };

  pn-workspace-update = pkgs.callPackage ./pn-workspace-update {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      pn-lib
      pn-discover-workspace
      testSupport
      ;
  };

  pn-workspace-upgrade = pkgs.callPackage ./pn-workspace-upgrade {
    inherit (bashBuilders) mkBashScript;
    inherit pn-workspace-update pn-workspace-apply testSupport;
    pkgs = pkgs;
  };

  pn-workspace-check = pkgs.callPackage ./pn-workspace-check {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  pn-workspace-push = pkgs.callPackage ./pn-workspace-push {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  pn-workspace-rebase = pkgs.callPackage ./pn-workspace-rebase {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  pn-workspace-status = pkgs.callPackage ./pn-workspace-status {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  pn-store-audit = pkgs.callPackage ./pn-store-audit {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  pn-store-deepclean = pkgs.callPackage ./pn-store-deepclean {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };

  allScripts = [
    pn-discover-workspace
    pn-osx-tcc-check
    pn-workspace-init
    pn-workspace-apply
    pn-workspace-build
    pn-workspace-update
    pn-workspace-upgrade
    pn-workspace-check
    pn-workspace-push
    pn-workspace-rebase
    pn-workspace-status
    pn-store-audit
    pn-store-deepclean
  ];
in
{
  inherit
    pn-lib
    pn-discover-workspace
    pn-osx-tcc-check
    pn-workspace-init
    pn-workspace-apply
    pn-workspace-build
    pn-workspace-update
    pn-workspace-upgrade
    pn-workspace-check
    pn-workspace-push
    pn-workspace-rebase
    pn-workspace-status
    pn-store-audit
    pn-store-deepclean
    ;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pn-lib = pn-lib.check;
    test-pn-discover-workspace = pn-discover-workspace.check;
    test-pn-osx-tcc-check = pn-osx-tcc-check.check;
    test-pn-workspace-init = pn-workspace-init.check;
    test-pn-workspace-apply = pn-workspace-apply.check;
    test-pn-workspace-build = pn-workspace-build.check;
    test-pn-workspace-update = pn-workspace-update.check;
    test-pn-workspace-upgrade = pn-workspace-upgrade.check;
    test-pn-workspace-check = pn-workspace-check.check;
    test-pn-workspace-push = pn-workspace-push.check;
    test-pn-workspace-rebase = pn-workspace-rebase.check;
    test-pn-workspace-status = pn-workspace-status.check;
    test-pn-store-audit = pn-store-audit.check;
    test-pn-store-deepclean = pn-store-deepclean.check;
  };

  # Aggregate check that runs all script tests
  check = pkgs.runCommand "test-pn-scripts" { } ''
    echo ${pn-lib.check}
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
