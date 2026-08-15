# shellcheck shell=bash

# pnwf-lib: guarded git/pn primitives shared by every `pnwf` subcommand.
#
# Every function here is called from scripts running under the builder's
# injected `set -euo pipefail`. git (and jq) commands whose exit code IS the
# meaningful boolean/tri-state signal (not-found, not-an-ancestor, absent
# ref, ...) MUST NOT be allowed to trip `errexit` on that "expected failure".
# The shape used throughout:
#
#   rc=0
#   git ... || rc=$?
#   case "$rc" in
#     <expected-code>) ... ;;
#     *) echo "..." >&2; return "$rc" ;;   # genuine, unexpected error
#   esac
#
# `pnwf_is_ancestor_of_primary` and `pnwf_branch_state` are the two guard
# primitives this task exists to get right; the rest follow the same shape.
#
# A probe whose answer git cannot establish MUST report that as its OWN state,
# never as one of the real answers -- and every caller MUST enumerate all of
# them, with a `*)` that asserts NO cause. Where a probe needs to say WHY it
# could not tell, it PRINTS "<state><TAB><detail>" and returns 0 (see
# pnwf_worktree_root_state, pnwf_branch_state, pnwf_upstream_state); where the
# caller needs no detail, the third state is a distinct RETURN CODE
# (pnwf_working_tree_dirty). Both shapes exist to stop a caller asserting a
# cause it never established -- bd pg2-k3s0x / pg2-42kh2 / pg2-lgzcg /
# pg2-deonn / pg2-xc9b7 are five instances of exactly that.

# Prints: landed | not-landed | absent  (never aborts under set -e)
#
# stderr is discarded on the git call: unlike `rev-parse --verify --quiet`,
# `merge-base --is-ancestor` has no quiet flag, and its "fatal: Not a valid
# object name" chatter on rc=128 would otherwise leak into the caller's
# captured output alongside the clean "absent" token this function promises
# (e.g. under bats' `run`, which merges stdout+stderr by default). A truly
# unexpected rc still gets an explicit diagnostic via the `*)` branch below.
pnwf_is_ancestor_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0
  git -C "$repo_dir" merge-base --is-ancestor "$branch" "$primary" 2>/dev/null || rc=$?
  case "$rc" in
  0) echo "landed" ;;
  1) echo "not-landed" ;;
  128) echo "absent" ;;
  *)
    echo "pnwf_is_ancestor_of_primary: git merge-base failed unexpectedly (rc=$rc)" >&2
    return "$rc"
    ;;
  esac
}

# Is repo_dir the ROOT of a git working tree git can actually read? Prints
# exactly ONE line, "<state><TAB><detail>", and always returns 0 -- both states
# are EXPECTED answers, not errors, so no caller can be aborted under set -e by
# asking:
#
#   confirmed      detail = a fixed human phrase naming the confirmed root
#   indeterminate  detail = why NO git answer for repo_dir can be trusted
#
# THIS IS THE PRECONDITION OF EVERY OTHER GIT QUESTION IN THIS FILE, and it is
# a precondition rather than a courtesy because `git -C <path>` does NOT fail on
# a path that is merely INSIDE a repository: rev-parse WALKS UP, so
# `symbolic-ref HEAD`, `status --porcelain` and `rev-parse refs/heads/<branch>`
# all answer, with exit 0 and no diagnostic, for the ENCLOSING repository.
# Verified 2026-08-14 (bd pg2-xc9b7): with the workspace root itself a clean git
# repo and a member path that is a plain directory inside it,
# `pnwf fork-preflight` printed `proceed` -- the fork stage's go-ahead -- for a
# canonical checkout it had never read, and printed `resume` naming that same
# member for a branch that existed only in the enclosing repo. Both answers were
# confident and wrong, which is why a caller cannot recover by reading rcs more
# carefully: there is no failure to read.
#
# `rev-parse --show-prefix` -- NOT `--git-dir` -- detects the walk-up, because
# `--git-dir` succeeds from that nested directory and so answers for the
# enclosing repository too. `--show-prefix` prints EMPTY at a working tree's
# ROOT and the relative sub-path otherwise, so a non-empty answer identifies
# exactly that walk-up (and every path pnwf hands over -- a set member, a
# canonical checkout -- is a worktree root, never a subdirectory). It is
# deliberately the RELATIVE form: `--show-toplevel` prints an absolute path that
# a caller would have to compare against repo_dir, and on darwin those differ by
# symlink (`/var/…` vs `/private/var/…`) for paths that are in fact the same.
#
# `--show-prefix` ALONE IS NOT ENOUGH, and the comment this replaces claimed
# otherwise: it said the flag "requires a work tree, so a bare repo lands in
# indeterminate". Verified false on git 2.54 -- in a bare repo `--show-prefix`
# exits 0 printing EMPTY, exactly like a working tree's root, so a bare path was
# CONFIRMED as a working-tree root by a probe whose header said it could not be.
# That is this defect family one level down: a comment documenting a property the
# code did not have (bd pg2-xc9b7). `--is-inside-work-tree` is the flag that does
# answer it (`false`, rc 0, in a bare repo), so it is asked EXPLICITLY. A bare
# repo at a member path is an anomaly worth refusing on its own terms rather than
# probing as a worktree: it has no index and no tracked files, so the
# cleanliness and branch questions callers ask next are not meaningful for it.
#
# THE SAME `--is-inside-work-tree` PROBE IS WHAT CATCHES A REDIRECTED WORKING
# TREE, and that is why this function REPLACED the separate
# `pnwf_worktree_root_ok` rather than running alongside it. A clone can carry a
# stale `core.worktree` naming some OTHER directory -- observed 2026-08-14 in the
# homelab canonical clone, left by an interrupted `pn workspace update`, and
# fixed once already in `feat(pnwf): stop fork-preflight when git is not acting
# on the member path`. git then answers every working-tree question about THAT
# directory while the caller believes it asked about repo_dir: `symbolic-ref`
# reports the primary branch and `status` reports clean, so BOTH halves of
# pnwf_canonical_on_primary_and_clean return a truthful "healthy" about the wrong
# tree and the anomaly survives every gate in the fork -> sync-fetch -> validate
# -> land pipeline. Verified on git 2.54: under such a redirect `--show-prefix`
# exits 0 printing EMPTY (so it is blind to this on its own) while
# `--is-inside-work-tree` prints `false`, because repo_dir is not inside the tree
# git was pointed at. The predecessor established the same fact by comparing
# `--show-toplevel` against repo_dir; the relative probe reaches it without ever
# comparing two absolute paths, which is what the `--show-toplevel` note above
# rules out on darwin. Two spellings of one question is how the definitions in
# this family drift apart, so there is one.
#
# THE DETAIL MUST NAME BOTH CAUSES, and the resolved toplevel with them. Which
# of "bare" and "redirected" occurred is not something `--is-inside-work-tree`
# distinguishes -- both print `false` -- so the message names both as
# possibilities and prints what git DID resolve, an observed fact that
# discriminates them for the reader. Asserting "bare" alone would send an
# operator holding a redirected canonical clone to inspect the wrong thing,
# which is the specific misdiagnosis the predecessor's own header called
# load-bearing.
#
# WHY TWO STATES AND NOT THREE: "the path is not readable as a working tree at
# all" and "the path sits inside an enclosing repository" are DIFFERENT causes
# but demand the IDENTICAL action from every caller -- refuse, and assert
# nothing about the path. The `detail` names which one occurred, so no
# information is lost; splitting them would only ask callers to enumerate a
# distinction they cannot act on. (Contrast pnwf_fetch_and_rebase's sentinels 3
# vs 4, which are separate codes precisely BECAUSE their recoveries differ.)
pnwf_worktree_root_state() {
  local repo_dir="$1" rc=0 prefix inside toplevel toplevel_rc=0
  prefix=$(git -C "$repo_dir" rev-parse --show-prefix 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    printf 'indeterminate\t%s\n' \
      "git could not read '$repo_dir' as a git working tree (rev-parse --show-prefix rc=$rc); the path may not be there at all, or may be there and not be a git repo"
    return 0
  fi

  rc=0
  inside=$(git -C "$repo_dir" rev-parse --is-inside-work-tree 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    printf 'indeterminate\t%s\n' \
      "git could not establish whether '$repo_dir' has a working tree (rev-parse --is-inside-work-tree rc=$rc)"
    return 0
  fi
  if [ "$inside" != "true" ]; then
    # The toplevel is reported because it DISCRIMINATES the two causes for a
    # reader (a bare repo has none; a redirect names the directory git went to),
    # and it is an observed value, so printing it asserts no cause.
    toplevel=$(git -C "$repo_dir" rev-parse --show-toplevel 2>/dev/null) || toplevel_rc=$?
    if [ "$toplevel_rc" -ne 0 ] || [ -z "$toplevel" ]; then
      toplevel="<none -- rev-parse --show-toplevel rc=$toplevel_rc>"
    fi
    printf 'indeterminate\t%s\n' \
      "git is NOT acting on a working tree at '$repo_dir' (rev-parse --is-inside-work-tree said '$inside'; it resolved the working tree to $toplevel), so it is either a repository with no working tree at all (a bare repo) or one whose working tree is REDIRECTED elsewhere (commonly a stale 'core.worktree' -- inspect with 'git -C $repo_dir config --get core.worktree'); either way the working-tree questions a caller asks next would not be about '$repo_dir'"
    return 0
  fi

  if [ -n "$prefix" ]; then
    printf 'indeterminate\t%s\n' \
      "'$repo_dir' is not the ROOT of a git working tree -- it sits at '$prefix' inside an ENCLOSING repository, so every git answer for it would be that other repository's"
    return 0
  fi
  printf 'confirmed\t%s\n' "'$repo_dir' is the root of a readable git working tree"
}

# TRI-state: does refs/heads/<branch> exist in repo_dir? Prints exactly ONE
# line, "<state><TAB><detail>", and always returns 0 -- all three states are
# EXPECTED answers, not errors:
#
#   exists         detail = the object id refs/heads/<branch> resolves to
#   absent         detail = a fixed human phrase
#   indeterminate  detail = why the answer could not be established
#
# WHY THIS IS NOT THE BOOLEAN `pnwf_branch_exists` IT REPLACES (bd pg2-xc9b7).
# That function was `rev-parse --verify --quiet refs/heads/<branch>` reduced to
# `[ "$rc" -eq 0 ]`, so "the ref is ABSENT" and "git could not read this path as
# the repo you meant" both came back as **the branch does not exist** -- an
# answer, not a refusal. Its one non-test caller DECIDES on it: cmd_fork_preflight
# adds nothing to its existing-branch list for a false absent, and with no other
# reason present prints `proceed`, which is the fork stage's go/no-go. So the
# collapse was fail-OPEN on the very check `fork-workforest` HALTS on (R-3/R-8),
# and a boolean return has no room for the third answer -- hence the state token.
#
# The repo is CONFIRMED FIRST, by pnwf_worktree_root_state, and that ordering is
# the load-bearing part: see its header for why a nested non-repo path gets the
# ENCLOSING repository's refs with exit 0. Only inside a confirmed root is
# rev-parse's rc read as the exists/absent signal.
#
# git's stderr is deliberately NOT discarded here (unlike
# pnwf_is_ancestor_of_primary, whose 128 is an expected state whose chatter would
# pollute its clean token): `--verify --quiet` already suppresses the ROUTINE
# "not a valid object name" message for an absent ref, so anything git still
# writes is a real anomaly -- `warning: ignoring broken ref refs/heads/<branch>`
# on rc 1, or the reason for rc 128 -- and that is exactly what the operator
# reading an `indeterminate` needs.
pnwf_branch_state() {
  local repo_dir="$1" branch="$2" rc=0 root_line root_state root_detail oid
  root_line=$(pnwf_worktree_root_state "$repo_dir")
  root_state=${root_line%%$'\t'*}
  root_detail=${root_line#*$'\t'}
  case "$root_state" in
  confirmed) : ;;
  indeterminate)
    printf 'indeterminate\t%s\n' "$root_detail"
    return 0
    ;;
  *)
    printf 'indeterminate\t%s\n' \
      "the working-tree-root probe returned the UNRECOGNISED state '$root_state' for '$repo_dir', so whether refs/heads/$branch exists cannot be established"
    return 0
    ;;
  esac

  rc=0
  oid=$(git -C "$repo_dir" rev-parse --verify --quiet "refs/heads/$branch") || rc=$?
  case "$rc" in
  0) printf 'exists\t%s\n' "$oid" ;;
  1) printf 'absent\t%s\n' "no refs/heads/$branch in '$repo_dir'" ;;
  *)
    # Reached only inside a root this function already CONFIRMED, so this is a
    # ref-store git cannot read there (a corrupt `packed-refs`, an unreadable
    # `refs/`) -- still indeterminate, still no cause asserted.
    printf 'indeterminate\t%s\n' \
      "git could not read refs/heads/$branch in '$repo_dir', which it DID read as a working-tree root (rev-parse --verify rc=$rc)"
    ;;
  esac
}

# Boolean: does a member checkout exist at <setdir>/<member>? Plain path
# existence — deliberately NOT `git worktree list` (never aborts under
# set -e either way, but `git worktree list`'s admin entries in
# .git/worktrees linger until an explicit `git worktree prune`, so
# list-based detection can report a stale "present" for a directory that
# was already removed on disk).
pnwf_worktree_present() {
  local setdir="$1" member="$2"
  [ -e "$setdir/$member" ]
}

# TRI-state: does repo_dir have uncommitted changes? Returns 0 (dirty),
# 1 (clean), or `git status`'s own rc (>1, typically 128 -- e.g. repo_dir is
# not a git repo) when the question COULD NOT BE ANSWERED. A caller MUST
# switch on all three and MUST NOT read the third as "clean" (nor as "dirty");
# the shipped callers do -- see pnwf_fetch_and_rebase, pnwf_classify_member,
# pnwf_canonical_on_primary_and_clean and cmd_update_relock's pre-flight.
#
# The optional second argument selects WHICH definition of "dirty" applies.
# THERE ARE EXACTLY TWO, THEY ARE BOTH DELIBERATE, AND BOTH LIVE HERE, in one
# function, so there is exactly one place either is spelled and no caller
# re-implements one locally (a second local `git diff --quiet` pair in
# cmd_update_relock was how the two drifted -- bd pg2-deonn):
#
#   include-untracked  (DEFAULT) an untracked file counts as dirty. This is the
#                      REPORTING / member-lifecycle definition -- the same
#                      observable FF-0b uses in the `ff-merge-to-main` skill,
#                      the one `git worktree remove` enforces at the end of that
#                      lifecycle (it refuses a worktree that "contains modified
#                      or untracked files"), and the one BOTH runner agents' R4
#                      residue probes use (`pnwf-runner.md`,
#                      `pnwf-update-runner.md`). Deliberately STRICTER than
#                      `git rebase`; see pnwf_fetch_and_rebase's header.
#   tracked-only       untracked files are IGNORED; only staged/unstaged
#                      TRACKED changes count. This is `pn`'s OWN isDirty
#                      (modules/pn/internal/workspace/update.go: a
#                      `git diff --quiet` + `git diff --cached --quiet` pair),
#                      and it is the GATE definition: `cmd_update_relock`'s
#                      pre-flight is pinned to it BECAUSE that guard exists to
#                      refuse exactly what `pn` would otherwise silently SKIP --
#                      so it MUST classify a member the same way `pn` does, no
#                      more strictly.
#
# THEIR RELATIONSHIP, which callers and consumers MUST know (bd pg2-xc9b7):
# include-untracked is a strict SUPERSET of tracked-only. A member whose only
# residue is untracked files is therefore DIRTY to the reporting probes and
# CLEAN to the gate -- so an R4 `dirty` entry does NOT imply `update-relock`'s
# pre-flight will refuse the next run, and a consumer MUST NOT infer that. The
# asymmetry is intended, not an oversight: the gate's job is to match what `pn`
# will act on, while a residue report's job is to make everything a killed job
# left READABLE to a person, and untracked files are precisely the residue no
# lock-file diff would show. Narrowing the report to the gate would trade that
# information for symmetry.
#
# `--untracked-files` is passed EXPLICITLY in BOTH scopes rather than relying on
# `status.showUntrackedFiles`, so neither answer can be changed by ambient git
# config. Without it the effective definition is whichever the operator's config
# picks -- i.e. an include-untracked probe on a machine with
# `status.showUntrackedFiles=no` silently BECOMES the tracked-only one, which is
# a third, unnamed definition rather than either of the two above.
#
# An unknown scope returns 2 -- neither "dirty" nor "clean", so a caller's
# indeterminate branch catches a typo instead of a guess being acted on.
pnwf_working_tree_dirty() {
  local repo_dir="$1" scope="${2:-include-untracked}" rc=0 status_output untracked_flag
  case "$scope" in
  include-untracked) untracked_flag=--untracked-files=normal ;;
  tracked-only) untracked_flag=--untracked-files=no ;;
  *)
    echo "pnwf_working_tree_dirty: unknown scope '$scope' (expected include-untracked or tracked-only)" >&2
    return 2
    ;;
  esac
  status_output=$(git -C "$repo_dir" status --porcelain "$untracked_flag") || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_working_tree_dirty: git status failed (rc=$rc)" >&2
    return "$rc"
  fi
  [ -n "$status_output" ]
}

# TRI-state upstream probe for ONE repo/worktree. Prints exactly ONE line,
# "<state><TAB><detail>", and always returns 0 -- all three states are
# EXPECTED answers, not errors, so no caller can be aborted under set -e by
# asking:
#
#   has-upstream    detail = the upstream's name (e.g. "origin/feature-x")
#   no-upstream     detail = a fixed human phrase
#   indeterminate   detail = why the answer could not be established
#
# WHY THE TRI-STATE, AND WHY THE REPO CHECK COMES FIRST: `git rev-parse
# --abbrev-ref --symbolic-full-name '@{u}'` exits 128 BOTH for "this branch has
# no upstream configured" AND for "repo_dir is not a git repository at all", so
# its rc ALONE cannot separate them -- and `pnwf_worktree_present` is a plain
# `-e` existence check, so a caller CAN hand over a path that exists and is not
# a repo. A caller that read any non-zero rc as "no upstream" would therefore
# report the required state for a member whose real state it never learned; for
# cmd_update_relock, whose no-remote-write guard prevents a `git push`, that is a
# fail-OPEN (bd pg2-deonn). So the repo is CONFIRMED first, and only inside a
# confirmed working tree is the `@{u}` rc read as the has-upstream signal.
#
# The confirmation is `pnwf_worktree_root_state`, which owns the `--show-prefix`
# (never `--git-dir`) rule and the walk-up rationale for BOTH probes that need
# it -- this one and `pnwf_branch_state`. It is written there ONCE on purpose:
# a second local copy of a rule is how two spellings of one question drift
# apart, which is the defect this function family keeps being fixed for
# (bd pg2-deonn / pg2-xc9b7). Its two `indeterminate` details are relayed
# VERBATIM here, so the operator learns which of the two the path hit.
#
# Inside a confirmed working tree the `@{u}` rc mirrors `pn`'s own hasUpstream
# predicate bit for bit (modules/pn/internal/workspace/push.go runs the same
# rev-parse and treats any error as "no upstream"), which is what makes
# "no-upstream" load-bearing for a caller guarding against pn's push: a detached
# or unborn HEAD also answers non-zero here, and `pn` reads it the same way and
# would not push either.
pnwf_upstream_state() {
  local repo_dir="$1" rc=0 root_line root_state root_detail upstream
  root_line=$(pnwf_worktree_root_state "$repo_dir")
  root_state=${root_line%%$'\t'*}
  root_detail=${root_line#*$'\t'}
  case "$root_state" in
  confirmed) : ;;
  indeterminate)
    printf 'indeterminate\t%s\n' "$root_detail"
    return 0
    ;;
  *)
    printf 'indeterminate\t%s\n' \
      "the working-tree-root probe returned the UNRECOGNISED state '$root_state' for '$repo_dir', so whether its branch has an upstream cannot be established"
    return 0
    ;;
  esac

  rc=0
  upstream=$(git -C "$repo_dir" rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null) || rc=$?
  if [ "$rc" -eq 0 ]; then
    printf 'has-upstream\t%s\n' "$upstream"
  else
    printf 'no-upstream\t%s\n' "no upstream ref configured for HEAD in '$repo_dir'"
  fi
}

# Prints the integer count of commits <branch> has that are not on
# <primary> (git rev-list --count <primary>..<branch>). Callers compare the
# printed value themselves (e.g. `[ "$(pnwf_ahead_of_primary ...)" -gt 0 ]`).
# On a guarded rev-list failure (e.g. an absent ref), nothing is printed to
# STDOUT (no bogus count) and the captured rc is returned without aborting
# under set -e; a diagnostic goes to stderr, matching every other guarded
# relay in this file (pnwf_working_tree_dirty, pnwf_resolve_primary_branch,
# pnwf_strategy, pnwf_topo_order) — needed so a caller/test can tell "the
# guard caught this and returned cleanly" apart from "the git call aborted
# the function via errexit before this point," which are NOT otherwise
# distinguishable: a bare `count=$(cmd)` failing without `|| rc=$?` also
# propagates that same rc as the function's own return value under set -e.
# git's own raw diagnostic is discarded (2>/dev/null on the git call) so
# stderr carries exactly one, first-party message.
pnwf_ahead_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0 count
  count=$(git -C "$repo_dir" rev-list --count "${primary}..${branch}" 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_ahead_of_primary: git rev-list failed unexpectedly (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$count"
}

# Boolean: is repo_dir currently on <primary> AND clean? This is the R-3
# steady-state check for the canonical clone (see repo CLAUDE.md's Git
# Worktree / Integration Discipline rules). Detached HEAD, a different
# branch, or a dirty tree all classify as false without aborting.
#
# A CALLER MUST CONFIRM THE ROOT (pnwf_worktree_root_state) BEFORE ASKING THIS,
# AND MUST REPORT A FAILED CONFIRMATION AS ITS OWN REASON. Both probes below
# accept whatever tree git decides it was pointed at, so for a path git does not
# read as its own working-tree root -- one nested inside an enclosing repo, or
# one whose `core.worktree` redirects elsewhere -- this returns a truthful
# "healthy" about a DIFFERENT tree. It cannot detect that itself, and pinning
# that is the point of test-pnwf-lib's "the redirect is INVISIBLE to the
# on-primary/clean check". Folding such a failure into this function's
# "off-primary or dirty" is what made the 2026-08-14 homelab occurrence
# undiagnosable: it sends the operator to inspect a working tree whose state is
# not the problem.
pnwf_canonical_on_primary_and_clean() {
  local repo_dir="$1" primary="$2" branch_rc=0 dirty_rc=0 current
  current=$(git -C "$repo_dir" symbolic-ref --quiet --short HEAD) || branch_rc=$?
  case "$branch_rc" in
  0) : ;;
  1) return 1 ;; # detached HEAD: not "on" any branch
  *)
    echo "pnwf_canonical_on_primary_and_clean: git symbolic-ref failed unexpectedly (rc=$branch_rc)" >&2
    return "$branch_rc"
    ;;
  esac

  [ "$current" = "$primary" ] || return 1

  pnwf_working_tree_dirty "$repo_dir" || dirty_rc=$?
  case "$dirty_rc" in
  0) return 1 ;; # dirty
  1) return 0 ;; # clean
  *)
    echo "pnwf_canonical_on_primary_and_clean: working-tree check failed unexpectedly (rc=$dirty_rc)" >&2
    return "$dirty_rc"
    ;;
  esac
}

# Prints the resolved primary branch name. integrate-branch-support is
# called BARE (no --json flag — it emits JSON unconditionally) and already
# implements the git-config -> symbolic-ref -> "main" resolution chain;
# this is a thin, guarded relay over its `.primary_branch` field.
pnwf_resolve_primary_branch() {
  local repo_dir="$1" rc=0 primary
  primary=$(cd "$repo_dir" && integrate-branch-support | jq -r .primary_branch) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_resolve_primary_branch: integrate-branch-support failed (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$primary"
}

# Prints the resolved integration strategy (e.g. "ff-merge-to-main",
# "pull-request"), or the literal string "null" when integrate-branch-support
# has not declared one. Same guarded relay shape as pnwf_resolve_primary_branch.
pnwf_strategy() {
  local repo_dir="$1" rc=0 strategy
  strategy=$(cd "$repo_dir" && integrate-branch-support | jq -r '.strategy // "null"') || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_strategy: integrate-branch-support failed (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$strategy"
}

# Prints each repo name in the workforest set lock's topological order,
# one per line (jq -r '.order[]').
pnwf_topo_order() {
  local lock_file="$1" rc=0
  jq -r '.order[]' "$lock_file" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_topo_order: failed to read .order from $lock_file (rc=$rc)" >&2
    return "$rc"
  fi
}

# Classifies one workforest member's landing status from git state alone
# (never aborts under set -e; backs `pnwf status`). Prints ONE line:
# "<label>\t<reason>", label one of: landed | not-started | blocked | kept.
#
# Args: setdir member canonical_dir branch primary
#
# Design note: ahead==0 (git rev-list --count primary..branch) is logically
# EQUIVALENT to is-ancestor(branch, primary)==true, so a PRESENT worktree can
# never simultaneously be "not landed" and "zero ahead" -- that combination
# does not occur in real git state. "landed" is therefore derived ONLY from
# worktree-absence (the one unambiguous signal: FF-4 completed, or a prior
# cleanup pass already removed it); a present worktree with zero-ahead is
# "not-started" (no work recorded yet in this repo). This also means the
# merge-base ancestor check is unnecessary here: `pnwf_ahead_of_primary`
# alone gives the same absent-ref (128) signal as `merge-base --is-ancestor`
# (both fail identically on an unresolvable ref), one guarded git call
# instead of two.
pnwf_classify_member() {
  local setdir="$1" member="$2" canonical_dir="$3" branch="$4" primary="$5"
  local setpath="$setdir/$member"

  if ! pnwf_worktree_present "$setdir" "$member"; then
    printf '%s\t%s\n' "landed" "worktree removed (landed)"
    return 0
  fi

  local dirty_rc=0
  pnwf_working_tree_dirty "$setpath" || dirty_rc=$?
  case "$dirty_rc" in
  0)
    printf '%s\t%s\n' "blocked" "working tree has uncommitted changes"
    return 0
    ;;
  1) : ;;
  *)
    echo "pnwf_classify_member: dirty check failed unexpectedly (rc=$dirty_rc)" >&2
    return "$dirty_rc"
    ;;
  esac

  # stderr discarded on this call: 128 (absent ref) is an EXPECTED case here
  # (reported below as "blocked", not a genuine error), so
  # pnwf_ahead_of_primary's own "failed unexpectedly" diagnostic for that rc
  # would otherwise leak into a caller capturing combined stdout+stderr
  # (e.g. bats' `run`) ahead of the clean "blocked\t…" line this function
  # promises — same rationale as pnwf_is_ancestor_of_primary discarding raw
  # git stderr on its own 128 path. A truly unexpected rc still gets an
  # explicit diagnostic via the `*)` branch below.
  local ahead ahead_rc=0
  ahead=$(pnwf_ahead_of_primary "$canonical_dir" "$branch" "$primary" 2>/dev/null) || ahead_rc=$?
  case "$ahead_rc" in
  0)
    if [ "$ahead" -eq 0 ]; then
      printf '%s\t%s\n' "not-started" "no commits ahead of $primary"
    else
      printf '%s\t%s\n' "kept" "$ahead commit(s) ahead of $primary, not yet landed"
    fi
    ;;
  128)
    printf '%s\t%s\n' "blocked" "member branch '$branch' not found in $canonical_dir although its worktree is present"
    ;;
  *)
    echo "pnwf_classify_member: ahead-of-primary check failed unexpectedly (rc=$ahead_rc)" >&2
    return "$ahead_rc"
    ;;
  esac
}

# Boolean: is a rebase IN PROGRESS in repo_dir? (never aborts under set -e)
#
# This is the OBSERVABLE that separates a rebase git REFUSED TO START (e.g. a
# dirty working tree -- git exits non-zero having done nothing) from one it
# STARTED AND STOPPED MID-WAY (a conflict). It MUST NOT be replaced by a match
# on git's message text: that wording is localized and changes between git
# versions, whereas the on-disk state directory is git's own documented
# rebase-in-progress marker (the same one `git rebase --continue`/`--abort`
# require).
#
# `rev-parse --git-path <name>` -- NOT a hardcoded "$repo_dir/.git/<name>" --
# is required because a `pnwf` member is a git WORKTREE: a linked worktree's
# rebase state lives in the canonical clone's `.git/worktrees/<name>/`, so the
# hardcoded path would never exist there and every refused rebase would still
# be misreported as a mid-rebase conflict. The same probe, and the same
# rationale, is written into the pnwf-runner agent's R4 residue probe.
#
# Verified with git 2.54: `--git-path` prints an ABSOLUTE path in a linked
# worktree and a path RELATIVE TO GIT'S OWN CWD in a main worktree, so a
# relative answer is re-anchored on repo_dir (that IS what `-C "$repo_dir"`
# made git's cwd) and never on this caller's cwd, which is arbitrary here.
# Both backends are checked: `rebase-merge` for the merge backend
# (interactive, and the default since git 2.26) and `rebase-apply` for the
# older apply/am backend, which `--apply`/`--whitespace` still select.
pnwf_rebase_in_progress() {
  local repo_dir="$1" name path rc
  for name in rebase-merge rebase-apply; do
    rc=0
    path=$(git -C "$repo_dir" rev-parse --git-path "$name") || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "pnwf_rebase_in_progress: git rev-parse --git-path $name failed in $repo_dir (rc=$rc)" >&2
      return "$rc"
    fi
    case "$path" in
    /*) ;;
    *) path="$repo_dir/$path" ;;
    esac
    if [ -d "$path" ]; then
      return 0
    fi
  done
  return 1
}

# Fetches origin then attempts to rebase repo_dir's current branch onto
# origin/<primary>. Backs `pnwf sync-fetch` -- the one MUTATING WORK-recipe
# helper in this file (every other function above is a read-only probe).
#
# Returns 0 on a clean pass (already up to date counts as clean). A nonzero
# return is the FIRST stopping point the caller must hand off on, and the
# CODE itself tells the caller WHICH step stopped it AND WHAT STATE it left
# repo_dir in -- each case needs DIFFERENT recovery advice, and advice from
# the wrong case is not merely unhelpful but actively wrong:
#   2  `git fetch origin` failed (network/remote/auth). No rebase was
#      started, so "git rebase --continue" would be wrong here.
#   3  `git rebase origin/<primary>` STARTED and STOPPED MID-WAY (a
#      conflict): `pnwf_rebase_in_progress` observes git's own
#      rebase-in-progress state directory (`rebase-merge`/`rebase-apply`
#      under this worktree's git dir) still present. It is deliberately NOT
#      cleaned up here (no `git rebase --abort`), so the caller's hand-off
#      message ("resolve here, then `git rebase --continue`") points at
#      exactly the state this function stopped in.
#   4  `git rebase origin/<primary>` was REFUSED OUTRIGHT and never started.
#      `pnwf_rebase_in_progress` observes NO rebase state directory, so there
#      is nothing to resolve and nothing to continue, and
#      "git rebase --continue" would be wrong here too. A DIRTY working tree
#      is NOT a cause of this code -- the pre-check below stops that earlier,
#      as 6. What reaches 4 is a refusal on a CLEAN tree, verified on git
#      2.54 with `rebase.autoStash=true`: an `origin/<primary>` that does not
#      resolve (rc 128, "fatal: invalid upstream" -- an unborn HEAD reads the
#      same way, having no commit for the ref to name), or a `pre-rebase`
#      hook veto (rc 1, "the pre-rebase hook refused to rebase"). An index
#      git cannot autostash (unmerged paths left by a merge/cherry-pick) also
#      refuses, but `git status --porcelain` reports those paths, so the
#      pre-check classifies it as 6 before the rebase is ever attempted.
#   5  `git rebase origin/<primary>` failed AND the rebase-in-progress
#      observable itself could not be read, so 3 vs 4 is INDETERMINATE. The
#      caller MUST NOT assert either recovery; a distinct code (rather than
#      relaying the probe's own rc) keeps this case from ever colliding with
#      the 2/3/4 sentinels above.
#   6  repo_dir's working tree is DIRTY, so NOTHING was attempted -- not the
#      fetch and not the rebase. See the pre-check rationale below; the
#      recovery is to commit or stash in repo_dir, and as with 4 there is
#      nothing mid-rebase to continue.
#   7  the DIRTINESS observable itself could not be read (e.g. repo_dir
#      exists but is not a git repo -- `pnwf_worktree_present` is a plain
#      path check, so the caller can hand one over), so whether a rebase is
#      SAFE to attempt is INDETERMINATE and none is attempted. Same
#      reasoning as 5, one step earlier: assert no cause. A distinct code
#      rather than relaying the probe's rc, which for a non-repo is 128 and
#      would otherwise reach the caller as an unrecognised sentinel.
#
# THE DIRTINESS PRE-CHECK IS LOAD-BEARING, NOT A CONVENIENCE, and it is why
# codes 6/7 exist at all (bd pg2-lgzcg). `git rebase` refuses on a dirty tree
# only while `rebase.autoStash` is OFF. With it ON -- and it IS on for this
# repo's operator, set in the XDG file `~/.config/git/config`, which
# `git config --global --get rebase.autoStash` does NOT see -- git stashes,
# rebases, pops, and reports exit 0 EVEN WHEN THAT POP CONFLICTS. Verified on
# git 2.54: the rebase printed BOTH "Applying autostash resulted in
# conflicts" AND "Successfully rebased", exited 0, and left the tree at
# `UU <file>` with the autostash still in `git stash list`. So without this
# pre-check a dirty member returns 0 from here -- an apparent CLEAN PASS --
# while its worktree holds unresolved conflict markers and the operator's
# work sits in an orphaned autostash, and the caller moves on to the next
# member. Code 4's "REFUSED" path can never catch that: git never refused.
#
# The check lives HERE, in the library, and not in `cmd_sync_fetch`, because
# it is what makes THIS function's return 0 mean what it says. Placed in the
# caller it would fix one call site and leave the next caller inheriting the
# same false clean pass. It also cannot be bypassed from the caller side:
# this function owns the only `git rebase` invocation in the pnwf codebase,
# the check precedes it unconditionally, and `cmd_sync_fetch` enumerates
# every sentinel explicitly with a `*)` that asserts NO cause -- so a code it
# has not been taught surfaces as an honest "unrecognised", never as silence.
#
# It runs BEFORE the fetch, not between fetch and rebase: it is purely local,
# a dirty member stops the whole run (the caller's stop-on-first-member
# convention), and the fetch is a network round trip that would be spent to
# reach a stop already decided. That ordering also means a member directory
# that is not a git repo is reported as 7 rather than as 2 ("fetch failed --
# check the remote/network/auth"), which was never the right advice for it.
#
# UNTRACKED FILES COUNT AS DIRTY, deliberately stricter than `git rebase`
# (verified git 2.54: an untracked-only tree is not autostashed and rebases
# clean). `pnwf_working_tree_dirty` is `git status --porcelain` non-empty,
# which is the same observable FF-0b uses in the `ff-merge-to-main` skill,
# and the same one `pnwf_classify_member` already reports as "blocked" --
# reusing it keeps one definition of "dirty" for the member lifecycle. It is
# also the definition the END of that lifecycle enforces: `git worktree
# remove` refuses a worktree that "contains modified or untracked files", so
# untracked residue blocks the landing step regardless. (Note this is a
# DIFFERENT, stricter definition than `cmd_update_relock`'s pre-flight, which
# ignores untracked to match `pn`'s own isDirty -- that guard exists to
# refuse exactly what `pn` would silently skip, so it is pinned to pn's
# definition, not to this one. That is the `tracked-only` SCOPE of this same
# function: both definitions are spelled in one place, and this call takes
# the default one.)
#
# What this function MUST NOT do on 6 is act on the caller's behalf: no
# stash, no commit, no `git rebase --abort`. Deciding the fate of work the
# tool did not create is not the tool's call -- the same rule FF-0b states
# for the `ff-merge-to-main` handler, and the reason 3 is likewise left
# mid-rebase rather than aborted.
#
# Never aborts the caller under set -e; a first-party diagnostic naming
# which git step failed AND its real exit code goes to stderr, while git's
# own chatter (e.g. the conflicting paths, or "cannot rebase: You have
# unstaged changes") is left on its normal stdout/stderr for whoever
# resolves it.
pnwf_fetch_and_rebase() {
  local repo_dir="$1" primary="$2" rc=0

  # Guarded (never a bare call): the probe is a TRI-state here, and its
  # "clean" answer is a nonzero return, which an unguarded call would let
  # errexit turn into an abort of this whole function -- discarding the pass
  # that is the common case. `pnwf_working_tree_dirty` writes its own
  # first-party diagnostic on rc>1, so nothing is suppressed here: an
  # unreadable working tree is NOT an expected case (unlike the 128s that
  # pnwf_classify_member silences), and its detail is what the operator needs.
  local dirty_rc=0
  pnwf_working_tree_dirty "$repo_dir" || dirty_rc=$?
  case "$dirty_rc" in
  0)
    echo "pnwf_fetch_and_rebase: $repo_dir has uncommitted changes; refusing to fetch or rebase it (nothing attempted)" >&2
    return 6
    ;;
  1) : ;;
  *)
    echo "pnwf_fetch_and_rebase: could not determine whether $repo_dir has uncommitted changes (probe rc=$dirty_rc); refusing to fetch or rebase it (nothing attempted)" >&2
    return 7
    ;;
  esac

  rc=0
  git -C "$repo_dir" fetch origin || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_fetch_and_rebase: git fetch origin failed in $repo_dir (rc=$rc)" >&2
    return 2
  fi

  rc=0
  git -C "$repo_dir" rebase "origin/$primary" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_fetch_and_rebase: git rebase origin/$primary failed in $repo_dir (rc=$rc)" >&2
    # Guarded (never a bare call): the probe is a tri-state here, and an
    # errexit abort on its "no rebase in progress" answer would discard the
    # distinction this whole branch exists to make.
    local probe_rc=0
    pnwf_rebase_in_progress "$repo_dir" || probe_rc=$?
    case "$probe_rc" in
    0) return 3 ;;
    1) return 4 ;;
    *)
      echo "pnwf_fetch_and_rebase: could not determine whether a rebase is in progress in $repo_dir (probe rc=$probe_rc)" >&2
      return 5
      ;;
    esac
  fi
}
