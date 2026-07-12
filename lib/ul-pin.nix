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
{ lib }:
{
  # True iff `content` still contains the bare, unpinned resolver call.
  isUnpinnedUpdateLocks = content: lib.hasInfix "nix-repo-base#determine-ul-lib-dir" content;
}
