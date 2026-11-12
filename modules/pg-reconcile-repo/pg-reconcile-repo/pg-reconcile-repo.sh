# shellcheck shell=bash

show_help() {
  cat <<'HELP'
pg-reconcile-repo: rebase a diverged canonical clone in a worktree, then swap its primary branch by compare-and-swap

Usage: pg-reconcile-repo [OPTIONS] REPO

REPO is the canonical clone. The script fetches origin and classifies the
primary branch against origin/<primary>. Only the diverged case is reconciled:

  1. create a worktree on a temporary branch at the local primary tip, rebase it
     onto origin/<primary>, and run the check command there;
  2. in REPO (which MUST be on the primary branch, clean, with no rebase, merge
     or cherry-pick in progress), move refs/heads/<primary> to the rebased tip
     with `git update-ref <ref> <new> <old>`, then a two-way `git read-tree -m -u <old> <new>` to move the index and
     working tree (a plain `git reset --keep <new>` would be a no-op once the
     ref has moved);
  3. remove the temporary worktree and branch.

If the primary branch advances by fast-forward while the check runs (a landing
by another session; routine on a busy repo, and the check can take tens of
minutes), the script does not fail: it cherry-picks the landed commits
(<old>..<new primary>) onto the rebased tip in the temporary worktree and
retries the swap, up to 20 rounds. The replayed commits are NOT re-checked
(their own lander gated them). If the primary moved any other way (history
rewritten, not a descendant of <old>) the swap is refused with exit 6; if a
landed commit does not replay cleanly the run stops with exit 4. Either way
nothing changes.

REPO is never mid-rebase. On any failure nothing in REPO changes. Behind-only is
fast-forwarded; up-to-date and ahead-only are reported and left alone. The script
never pushes.

Options:
  -h, --help          Show this help message
  --primary BRANCH    Primary branch (default: git config
                      pgii-integrate-branch.primaryBranch, then origin/HEAD,
                      then main)
  --check-cmd CMD     Command run in the rebased worktree (default:
                      "pg-hooks run pre-land"); a non-zero exit stops the run
  --no-check          Skip the check command

The last stdout line is the outcome: up-to-date, ahead-only, fast-forwarded
<old> <new>, or reconciled <old> <new>.

Exit codes:
  0  outcome printed
  2  usage error
  3  REPO is not in steady state (not on primary, dirty, or operation in progress)
  4  rebase conflict, or a commit landed during the check does not replay
     (aborted, nothing changed)
  5  check command failed on the rebased tip (nothing changed)
  6  compare-and-swap refused: the primary branch was rewritten or kept moving
     past the replay limit (nothing changed)
  7  working-tree move failed after the swap (the ref was restored)
  8  fetch failed or origin/<primary> does not resolve
HELP
}

REPO=""
# shellcheck disable=SC2034  # read by pgrr_main in pg-reconcile-repo.bash
PRIMARY=""
# shellcheck disable=SC2034  # read by pgrr_main in pg-reconcile-repo.bash
CHECK_CMD="pg-hooks run pre-land"
while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --primary)
    [[ $# -ge 2 ]] || die_usage "--primary needs a value"
    # shellcheck disable=SC2034  # read by pgrr_main in pg-reconcile-repo.bash
    PRIMARY="$2"
    shift
    ;;
  --check-cmd)
    [[ $# -ge 2 ]] || die_usage "--check-cmd needs a value"
    # shellcheck disable=SC2034  # read by pgrr_main in pg-reconcile-repo.bash
    CHECK_CMD="$2"
    shift
    ;;
  --no-check)
    # shellcheck disable=SC2034  # read by pgrr_main in pg-reconcile-repo.bash
    CHECK_CMD=""
    ;;
  --)
    shift
    break
    ;;
  -*) die_usage "unknown option: $1" ;;
  *)
    [[ -z $REPO ]] || die_usage "unexpected argument: $1"
    REPO="$1"
    ;;
  esac
  shift
done
[[ -n $REPO ]] || die_usage "REPO is required"
[[ -d $REPO ]] || die_usage "not a directory: $REPO"

pgrr_main
