# Unit tests for lib/ul-pin.nix:isUnpinnedUpdateLocks (bead pg2-o784p).
# A set of { expr; expected; } cases; run via lib.runTests and wired into the
# base flake `checks.update-locks-pin-predicate`.
{ lib }:
let
  inherit (import ./ul-pin.nix { inherit lib; }) isUnpinnedUpdateLocks;
in
{
  # (a) bare, unpinned resolver call -> flagged as unpinned.
  testBareUnpinnedDetected = {
    expr = isUnpinnedUpdateLocks ''
      UL_LIB_DIR="''${UL_LIB_DIR:-$(nix run "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir")}"
    '';
    expected = true;
  };

  # (b) pinned to the locked rev via ${NRB_REF} -> NOT flagged.
  testPinnedNotDetected = {
    expr = isUnpinnedUpdateLocks ''
      NRB_REF="github:phillipgreenii/nix-repo-base/''${NRB_REV}"
      UL_LIB_DIR="''${UL_LIB_DIR:-$(nix run "''${NRB_REF}#determine-ul-lib-dir")}"
    '';
    expected = false;
  };

  # (c) base direct-source form (no resolver call at all) -> NOT flagged.
  testDirectSourceNotDetected = {
    expr = isUnpinnedUpdateLocks ''
      UL_LIB_DIR="''${UL_LIB_DIR:-''${SCRIPT_DIR}/lib/scripts}"
      source "''${UL_LIB_DIR}/update-locks-lib.bash"
    '';
    expected = false;
  };

  # (d) the old fallback assignment (pg2-pwfqqn): rev-less NRB_REF -> flagged,
  # even though the resolver call itself goes through ${NRB_REF}.
  testOldFallbackDetected = {
    expr = isUnpinnedUpdateLocks ''
      if [ -n "$NRB_REV" ]; then
        NRB_REF="github:phillipgreenii/nix-repo-base/''${NRB_REV}"
      else
        NRB_REF="github:phillipgreenii/nix-repo-base"
      fi
      UL_LIB_DIR="''${UL_LIB_DIR:-$(nix run "''${NRB_REF}#determine-ul-lib-dir")}"
    '';
    expected = true;
  };

  # (e) rev-less ref at end of content / followed by a quote -> flagged.
  testRevlessRefAtEndDetected = {
    expr = isUnpinnedUpdateLocks "NRB_REF=github:phillipgreenii/nix-repo-base";
    expected = true;
  };

  # (f) helper-based caller (fail-closed resolve-nrb-ref) -> NOT flagged.
  testHelperCallerNotDetected = {
    expr = isUnpinnedUpdateLocks ''
      NRB_REF=$(resolve-nrb-ref --lock "''${SCRIPT_DIR}/flake.lock")
      UL_LIB_DIR="''${UL_LIB_DIR:-$(nix run "''${NRB_REF}#determine-ul-lib-dir")}"
    '';
    expected = false;
  };
}
