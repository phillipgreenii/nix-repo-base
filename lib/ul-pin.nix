# Pure predicate backing the auto-contributed `update-locks-pinned` drift guard
# (flake-modules/checks.nix) and its unit tests (lib/ul-pin-tests.nix).
#
# Security context (bead pg2-o784p): a consumer's update-locks.sh MUST pin the
# nix-repo-base resolver to the flake.lock rev before `nix run`-ing it. Otherwise
# token-bearing CI builds and runs whatever is at nix-repo-base's default-branch
# HEAD — an arbitrary code-execution hole. The bare, unpinned form is
#   nix run github:phillipgreenii/nix-repo-base#determine-ul-lib-dir
# The pinned form inserts `/${NRB_REV}` before the `#` (via ${NRB_REF}), and
# base's own script sources its in-tree lib with no resolver call at all — so
# both compliant forms lack the literal infix below.
#
# Bead pg2-sdmk0y (ruling a+c on pg2-pwfqqn): the old consumer fallback
#   NRB_REF="github:phillipgreenii/nix-repo-base"   # unpinned HEAD
# slipped past the literal check above because the `#determine-ul-lib-dir`
# lives in a different line. The guard now also rejects ANY mention of the
# nix-repo-base flake ref that is not followed by `/<rev>`. Compliant callers
# obtain the ref from the fail-closed `resolve-nrb-ref` helper instead.
{ lib }:
let
  nrbRef = "github:phillipgreenii/nix-repo-base";
  # Chunks after each occurrence of the ref; an occurrence is pinned only when
  # the next character is `/` (the rev separator).
  hasRevlessRef =
    content:
    lib.any (chunk: !(lib.hasPrefix "/" chunk)) (builtins.tail (lib.splitString nrbRef content));
in
{
  # True iff `content` still contains an unpinned nix-repo-base reference: the
  # bare resolver call, or a github:phillipgreenii/nix-repo-base ref with no rev
  # (e.g. the old `NRB_REF=` fallback).
  isUnpinnedUpdateLocks =
    content: lib.hasInfix "nix-repo-base#determine-ul-lib-dir" content || hasRevlessRef content;
}
