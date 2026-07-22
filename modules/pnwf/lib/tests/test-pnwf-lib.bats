#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Test suite for pnwf-lib: guarded git/pn primitives shared by every pnwf
# subcommand. The review-critical property under test is that every
# exit-code-as-boolean git probe survives a REAL `set -euo pipefail` caller —
# see the H1 harness tests below, which run the probe inside a fresh
# `bash -euo pipefail -c '...'` subprocess rather than bats' own (non -e)
# shell. Merely sourcing the lib into bats' shell would prove nothing.
#
# Non-vacuousness note: a BARE call (not wrapped in `if`/`&&`/`||`) is the
# only harness shape that can actually observe an internal command aborting
# under set -e — wrapping a call in `if`/`&&`/`||` suspends errexit for that
# call's *entire* execution (bash semantics), so an if-wrapped "does not
# abort" assertion can catch a guard-dependent LOGIC bug (rc never gets
# captured, so a stale check is wrong) but can never catch an internal
# command actually aborting the function. And even a bare call is
# externally indistinguishable from an internal abort when the guarded
# error path does nothing but silently `return "$rc"` — bash treats an
# errexit-triggered early return and an explicit `return` with the same
# code identically to the caller. Where that applies (pnwf_working_tree_dirty,
# pnwf_ahead_of_primary, pnwf_resolve_primary_branch, pnwf_strategy,
# pnwf_topo_order), the guarded implementation also writes a first-party
# diagnostic to stderr before returning; the failure-path tests below use
# `run --separate-stderr` to assert that diagnostic is present (proving the
# guard's own error-handling code executed, not an early abort) while stdout
# stays exactly empty (proving no bogus value was printed).

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)/pnwf-lib.bash"
  fi

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Mocks live OUTSIDE the repo working tree (sibling dir), never inside it.
  MOCK_DIR="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_DIR"
  PATH="$MOCK_DIR:$PATH"
  export PATH MOCK_DIR

  # Default integrate-branch-support mock: called bare (no --json flag), it
  # emits JSON unconditionally, mirroring the real tool.
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"

  REPO="$TEST_DIR/repo"
  mkdir -p "$REPO"
  command git -C "$REPO" init -q -b main
  command git -C "$REPO" config user.email "test@example.com"
  command git -C "$REPO" config user.name "Test"
  echo one >"$REPO/file.txt"
  command git -C "$REPO" add file.txt
  command git -C "$REPO" commit -q -m "initial"
  export REPO
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- pnwf_branch_exists -------------------------------------------------

@test "pnwf_branch_exists: existing branch returns true" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_branch_exists '$REPO' feature"
  [ "$status" -eq 0 ]
}

@test "pnwf_branch_exists: missing branch (non-zero) does not abort caller" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_branch_exists '$REPO' nope; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_is_ancestor_of_primary ---------------------------------------

@test "pnwf_is_ancestor_of_primary: landed branch classifies as landed" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "landed" ]
}

@test "pnwf_is_ancestor_of_primary: not-landed branch (exit 1) does not abort" {
  command git -C "$REPO" checkout -q -b feature
  echo two >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "second"
  command git -C "$REPO" checkout -q main
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "not-landed" ]
}

@test "pnwf_is_ancestor_of_primary: absent ref (exit 128) does not abort" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' does-not-exist main"
  [ "$status" -eq 0 ]
  [ "$output" = "absent" ]
}

# --- pnwf_worktree_present ----------------------------------------------
# Plain `[ -e <setdir>/<member> ]` — no git call, so no rc-capture guard to
# strip in the first place (deliberately NOT `git worktree list`: its admin
# entries in .git/worktrees linger until an explicit prune).

@test "pnwf_worktree_present: true when the member directory exists" {
  mkdir -p "$TEST_DIR/set/member-a"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_present '$TEST_DIR/set' member-a"
  [ "$status" -eq 0 ]
}

@test "pnwf_worktree_present: false (non-zero) for a missing member does not abort" {
  mkdir -p "$TEST_DIR/set"
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_worktree_present '$TEST_DIR/set' missing-member; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_working_tree_dirty ---------------------------------------------

@test "pnwf_working_tree_dirty: clean tree returns false without aborting" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_working_tree_dirty '$REPO'; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

@test "pnwf_working_tree_dirty: untracked file marks tree dirty" {
  echo extra >"$REPO/untracked.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty: modified tracked file marks tree dirty" {
  echo changed >"$REPO/file.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty: git status failure (non-git dir) does not abort" {
  mkdir -p "$TEST_DIR/not-a-repo"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$TEST_DIR/not-a-repo'"
  [ "$status" -eq 128 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_working_tree_dirty: git status failed (rc=128)"* ]]
}

# --- pnwf_ahead_of_primary ------------------------------------------------
# Contract: PRINTS the integer count (git rev-list --count <primary>..<branch>).
# Callers compare the printed value themselves. On a guarded rev-list
# failure (bad ref), nothing is printed and the captured rc is returned.

@test "pnwf_ahead_of_primary: prints the count of commits ahead" {
  command git -C "$REPO" checkout -q -b feature
  echo two >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "second"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_ahead_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "1" ]
}

@test "pnwf_ahead_of_primary: prints zero when identical to primary" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_ahead_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "0" ]
}

@test "pnwf_ahead_of_primary: bad ref (rev-list failure) does not abort and prints nothing" {
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_ahead_of_primary '$REPO' does-not-exist main"
  [ "$status" -eq 128 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_ahead_of_primary: git rev-list failed unexpectedly (rc=128)"* ]]
}

# --- pnwf_canonical_on_primary_and_clean ----------------------------------

@test "pnwf_canonical_on_primary_and_clean: true when on primary and clean" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 0 ]
}

@test "pnwf_canonical_on_primary_and_clean: false on a different branch does not abort" {
  command git -C "$REPO" checkout -q -b feature
  # Bare call (not if-wrapped): a bare call is the only shape that can
  # observe an internal command actually aborting under set -e (see the
  # non-vacuousness note at the top of this file).
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 1 ]
}

@test "pnwf_canonical_on_primary_and_clean: false when dirty does not abort" {
  echo extra >"$REPO/untracked.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 1 ]
}

@test "pnwf_canonical_on_primary_and_clean: detached HEAD (symbolic-ref rc=1) does not abort" {
  command git -C "$REPO" checkout -q --detach HEAD
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 1 ]
}

# --- pnwf_resolve_primary_branch ------------------------------------------

@test "pnwf_resolve_primary_branch: relays a non-default primary_branch (trunk)" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"trunk","strategy":null}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_resolve_primary_branch '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "trunk" ]
}

@test "pnwf_resolve_primary_branch: relays default main" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_resolve_primary_branch '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "main" ]
}

@test "pnwf_resolve_primary_branch: integrate-branch-support failure does not abort" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo "integrate-branch-support: mock failure" >&2
exit 3
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_resolve_primary_branch '$REPO'"
  [ "$status" -eq 3 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_resolve_primary_branch: integrate-branch-support failed (rc=3)"* ]]
}

# --- pnwf_strategy ---------------------------------------------------------

@test "pnwf_strategy: relays a declared strategy" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":"ff-merge-to-main"}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_strategy '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "ff-merge-to-main" ]
}

@test "pnwf_strategy: defaults to the string null when absent" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_strategy '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "null" ]
}

@test "pnwf_strategy: integrate-branch-support failure does not abort" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo "integrate-branch-support: mock failure" >&2
exit 3
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_strategy '$REPO'"
  [ "$status" -eq 3 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_strategy: integrate-branch-support failed (rc=3)"* ]]
}

# --- pnwf_topo_order -------------------------------------------------------

@test "pnwf_topo_order: reads order from a fixture set lock" {
  lock_file="$TEST_DIR/set-lock.json"
  printf '{"order":["repoA","repoB","repoC"]}' >"$lock_file"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_topo_order '$lock_file'"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "repoA" ]
  [ "${lines[1]}" = "repoB" ]
  [ "${lines[2]}" = "repoC" ]
}

@test "pnwf_topo_order: missing lock file does not abort" {
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_topo_order '$TEST_DIR/does-not-exist-lock.json'"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_topo_order: failed to read .order from $TEST_DIR/does-not-exist-lock.json (rc=2)"* ]]
}

# --- pnwf_classify_member ---------------------------------------------------
# Prints "<label>\t<reason>" (one line): landed | not-started | blocked | kept.
# Backs `pnwf status`; every branch is exercised via the guarded primitives
# above, so this is itself a guarded relay (never aborts under set -e).
#
# Design note (why "landed" comes ONLY from worktree-absence here): ahead==0
# is logically EQUIVALENT to is-ancestor==true (rev-list --count primary..branch
# is zero iff branch's tip is reachable from primary) -- there is no git state
# where a PRESENT worktree is simultaneously "not landed" and "zero ahead". So
# a present worktree with zero-ahead is, by convention, "not-started" (the
# operator-facing signal that no work has been recorded yet in this repo);
# "landed" is reserved for the one unambiguous signal -- the worktree is gone
# (FF-4 completed, or a prior cleanup pass already removed it).

@test "pnwf_classify_member: absent worktree classifies as landed (FF-4 removed it)" {
  run bash -euo pipefail -c "
    source '$LIB_PATH'
    pnwf_classify_member '$TEST_DIR/set' member-a '$REPO' feature main
  "
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'landed\tworktree removed (landed)')" ]
}

@test "pnwf_classify_member: worktree present, clean, zero ahead -> not-started" {
  command git -C "$REPO" branch feature
  # A REAL worktree at the member path -- pnwf_working_tree_dirty runs `git
  # status` there, so a bare mkdir (not a git checkout) would misreport a
  # guarded git-status FAILURE (rc=128, "not a git repo") as if it were the
  # member's own dirty/blocked state.
  command git -C "$REPO" worktree add -q "$TEST_DIR/set/member-a" feature
  run bash -euo pipefail -c "
    source '$LIB_PATH'
    pnwf_classify_member '$TEST_DIR/set' member-a '$REPO' feature main
  "
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'not-started\tno commits ahead of main')" ]
}

@test "pnwf_classify_member: worktree present, clean, ahead>0 -> kept" {
  command git -C "$REPO" checkout -q -b feature
  echo two >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am second
  command git -C "$REPO" checkout -q main
  command git -C "$REPO" worktree add -q "$TEST_DIR/set/member-a" feature
  run bash -euo pipefail -c "
    source '$LIB_PATH'
    pnwf_classify_member '$TEST_DIR/set' member-a '$REPO' feature main
  "
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'kept\t1 commit(s) ahead of main, not yet landed')" ]
}

@test "pnwf_classify_member: worktree present, dirty -> blocked (dirty wins over ahead)" {
  command git -C "$REPO" branch feature
  command git -C "$REPO" worktree add -q "$TEST_DIR/set/member-a" feature
  echo untracked >"$TEST_DIR/set/member-a/extra.txt"
  run bash -euo pipefail -c "
    source '$LIB_PATH'
    pnwf_classify_member '$TEST_DIR/set' member-a '$REPO' feature main
  "
  [ "$status" -eq 0 ]
  [[ "$output" == blocked$'\t'* ]]
  [[ "$output" == *"uncommitted changes"* ]]
}

@test "pnwf_classify_member: worktree present, branch ref absent (128) does not abort -> blocked" {
  # The member worktree is present, but on an UNRELATED branch -- the
  # member branch being tested ("does-not-exist") is absent from $REPO
  # entirely, which is the scenario under test (128, guarded).
  command git -C "$REPO" worktree add -q "$TEST_DIR/set/member-a" -b member-a-unrelated
  run bash -euo pipefail -c "
    source '$LIB_PATH'
    pnwf_classify_member '$TEST_DIR/set' member-a '$REPO' does-not-exist main
  "
  [ "$status" -eq 0 ]
  [[ "$output" == blocked$'\t'* ]]
  [[ "$output" == *"not found in $REPO"* ]]
}

# --- pnwf_fetch_and_rebase ---------------------------------------------
# Backs `pnwf sync-fetch`, the one MUTATING primitive in this file (every
# other function above is a read-only probe). Real git throughout -- a real
# bare "origin" plus a real clone -- so these assertions exercise actual
# fetch/rebase mechanics rather than a stand-in; the CLI-level orchestration
# (stop-on-first-conflict across several members) is covered separately in
# test-pnwf.bats with a mocked `git`.

_setup_fetch_and_rebase_origin() {
  # A bare "origin" seeded from $REPO's own history, plus a local clone with
  # origin configured -- mirrors a real workforest member (a worktree
  # checked out from the canonical clone, canonical remote-tracking origin).
  ORIGIN="$TEST_DIR/origin.git"
  command git clone -q --bare "$REPO" "$ORIGIN"
  export ORIGIN

  CLONE="$TEST_DIR/clone"
  command git clone -q "$ORIGIN" "$CLONE"
  command git -C "$CLONE" config user.email "test@example.com"
  command git -C "$CLONE" config user.name "Test"
  export CLONE
}

@test "pnwf_fetch_and_rebase: clean fetch + rebase onto a non-conflicting advance" {
  _setup_fetch_and_rebase_origin

  command git -C "$CLONE" checkout -q -b feature
  echo feature-work >"$CLONE/feature-file.txt"
  command git -C "$CLONE" add feature-file.txt
  command git -C "$CLONE" commit -q -m "feature work"

  echo origin-advance >"$REPO/other-file.txt"
  command git -C "$REPO" add other-file.txt
  command git -C "$REPO" commit -q -m "origin advance"
  command git -C "$REPO" push -q "$ORIGIN" main

  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  [ "$status" -eq 0 ]

  run bash -c "command git -C '$CLONE' merge-base --is-ancestor origin/main feature"
  [ "$status" -eq 0 ]
}

@test "pnwf_fetch_and_rebase: already up to date is a clean no-op" {
  _setup_fetch_and_rebase_origin
  command git -C "$CLONE" checkout -q -b feature

  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  [ "$status" -eq 0 ]
}

@test "pnwf_fetch_and_rebase: rebase conflict (non-zero) does not abort the caller and leaves the rebase in progress" {
  _setup_fetch_and_rebase_origin

  command git -C "$CLONE" checkout -q -b feature
  echo clone-change >"$CLONE/file.txt"
  command git -C "$CLONE" commit -q -am "clone change"

  echo origin-change >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "origin change"
  command git -C "$REPO" push -q "$ORIGIN" main

  # Bare call (not if-wrapped): the only shape that can observe an internal
  # command actually aborting under set -e (see the non-vacuousness note at
  # the top of this file).
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  [ "$status" -ne 0 ]

  # git itself leaves the rebase mid-progress -- pnwf_fetch_and_rebase MUST
  # NOT `git rebase --abort` it; the hand-off contract is `git rebase
  # --continue` in this exact worktree.
  [ -d "$CLONE/.git/rebase-apply" ] || [ -d "$CLONE/.git/rebase-merge" ]
}

@test "pnwf_fetch_and_rebase: git fetch failure does not abort and prints a diagnostic" {
  _setup_fetch_and_rebase_origin
  command git -C "$CLONE" checkout -q -b feature
  command git -C "$CLONE" remote set-url origin "$TEST_DIR/does-not-exist.git"

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_fetch_and_rebase: git fetch origin failed in $CLONE"* ]]
}
