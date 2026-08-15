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

# IMMUTABLE, path-stable fixtures are built ONCE per file here (bead pg2-nh1t3):
# LIB_PATH resolution, the fsmonitor guard, and the default
# integrate-branch-support mock TEMPLATE. The per-test MUTABLE state (TEST_DIR
# and the real git REPO that many tests branch/commit into) stays in setup().
setup_file() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)/pnwf-lib.bash"
  fi
  export LIB_PATH

  # Hermetic + fast git (same guard as test-pnwf.bats). This suite ALSO
  # `git init`s a real repo per test, so on a machine with global
  # core.fsmonitor=true each throwaway repo spawns its own fsmonitor daemon that
  # blocks every working-tree op for 2-3s -- pushing the suite to ~20min locally
  # (and, under `bats --jobs`, many daemons at once). GIT_CONFIG_COUNT works like
  # a `-c` flag, so it wins over the inherited global and is surgical; injected
  # once here (immutable across tests) so both the real-git tests and the
  # `bash -euo pipefail -c` probe subprocesses inherit it. fsmonitor/
  # untrackedcache are performance-only, so this is behavior-neutral -- the
  # nix-check sandbox (clean HOME) already ran fast without it.
  export GIT_CONFIG_COUNT=2
  export GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false
  export GIT_CONFIG_KEY_1=core.untrackedcache GIT_CONFIG_VALUE_1=false

  # Full hermeticity on top of that surgical pin (bead pg2-klyn6): the pin above
  # covers only two keys, so EVERY other key still merged in from the developer's
  # ~/.gitconfig, $XDG_CONFIG_HOME/git/config and /etc/gitconfig. /dev/null is the
  # NEUTRAL setting for both scopes, so no test outcome depends on whose machine
  # runs it. Safe here because this suite always pins what it needs explicitly —
  # `git init -q -b <branch>` (never inheriting init.defaultBranch) and repo-local
  # user.email/user.name. Mirrors the pg2-39rz2 Go fix's TestMain in
  # modules/pn/internal/workspace/realgit_test.go. Requires git >= 2.32.
  export GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_SYSTEM=/dev/null

  # Immutable mock TEMPLATE: the default integrate-branch-support mock, called
  # bare (no --json flag), emits JSON unconditionally, mirroring the real tool.
  # setup() copies it into each test's own MOCK_DIR so a test may override it in
  # isolation (required for `bats --jobs` safety).
  MOCK_TEMPLATE="$BATS_FILE_TMPDIR/mock-template"
  mkdir -p "$MOCK_TEMPLATE"
  export MOCK_TEMPLATE
  cat >"$MOCK_TEMPLATE/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_TEMPLATE/integrate-branch-support"
}

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Mocks live OUTSIDE the repo working tree (sibling dir), never inside it.
  # Seeded from the immutable per-file template (setup_file); copying rather than
  # rebuilding keeps each test able to override integrate-branch-support in
  # isolation under `bats --jobs`.
  MOCK_DIR="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_DIR"
  cp -p "$MOCK_TEMPLATE/integrate-branch-support" "$MOCK_DIR/"
  PATH="$MOCK_DIR:$PATH"
  export PATH MOCK_DIR

  # HERMETIC HOME (bead pg2-7hr6o), the same three lines the wsplan suites in this
  # module already carry — copied, not reinvented. The bash-scripting skill's
  # test-isolation rule 2 requires it and this suite lacked it, so a bare
  # `bats modules/pnwf/lib/tests` read the developer's real HOME for every non-git
  # purpose (the nix check's sandbox HOME hid that: only the gate was hermetic).
  # setup_file's GIT_CONFIG_GLOBAL=/dev/null outranks HOME for GIT alone; caches,
  # XDG defaults, tool configs and credential helpers still resolved off the real
  # one. Per-test (not per-file) so each test gets a pristine, empty HOME.
  HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export HOME

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

# --- pnwf_working_tree_dirty: the tracked-only scope -------------------------
# `tracked-only` is pn's OWN isDirty (a `git diff --quiet` + `git diff --cached
# --quiet` pair in modules/pn/internal/workspace/update.go), which
# cmd_update_relock's pre-flight is pinned to. The two scopes DISAGREE on
# exactly one input -- an untracked-only tree -- and that disagreement is the
# reason both live in this one function instead of the caller re-implementing
# one (bd pg2-deonn).

@test "pnwf_working_tree_dirty tracked-only: an untracked-only tree is CLEAN (pn isDirty parity)" {
  echo extra >"$REPO/untracked.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_working_tree_dirty '$REPO' tracked-only; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
  # The DISCRIMINATING half: the same tree, same function, default scope, is
  # dirty -- so this test would pass vacuously if tracked-only silently fell
  # back to the default.
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty tracked-only: an unstaged tracked change is dirty" {
  echo changed >"$REPO/file.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO' tracked-only"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty tracked-only: a STAGED tracked change is dirty" {
  # pn's isDirty probes the index separately (`git diff --cached --quiet`), so
  # a change that is staged and matches the worktree must still count.
  echo changed >"$REPO/file.txt"
  command git -C "$REPO" add file.txt
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO' tracked-only"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty tracked-only: a non-git dir returns the probe rc, NOT clean" {
  # The tri-state's third leg: 128 is "could not tell", and it MUST NOT be
  # reachable as either 0 or 1. `pnwf_worktree_present` is a plain `-e` check,
  # so a caller genuinely can hand over a path like this.
  mkdir -p "$TEST_DIR/not-a-repo"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$TEST_DIR/not-a-repo' tracked-only"
  [ "$status" -eq 128 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_working_tree_dirty: git status failed (rc=128)"* ]]
}

@test "pnwf_working_tree_dirty: an unknown scope returns neither dirty nor clean" {
  # 2 lands in a caller's indeterminate branch, so a typo cannot be acted on as
  # an answer.
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO' tracked_only"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ "$stderr" == *"unknown scope 'tracked_only'"* ]]
}

# --- pnwf_upstream_state ---------------------------------------------------
# Contract: PRINTS one "<state><TAB><detail>" line and always returns 0 --
# has-upstream | no-upstream | indeterminate. The property under test is that
# "no upstream configured" and "could not read this as a repo" are DISTINCT,
# because `git rev-parse '@{u}'` exits 128 for both and its consumer
# (cmd_update_relock's no-remote-write guard) must fail CLOSED on the second
# (bd pg2-deonn).

# Gives $REPO's current branch a real upstream via a local bare remote.
_upstream_publish() {
  local remote="$TEST_DIR/remote.git"
  command git init -q --bare "$remote"
  command git -C "$REPO" remote add origin "$remote"
  command git -C "$REPO" push -q -u origin main
}

@test "pnwf_upstream_state: a branch with no upstream prints no-upstream" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_upstream_state '$REPO'"
  [ "$status" -eq 0 ]
  [ "${output%%$'\t'*}" = "no-upstream" ]
}

@test "pnwf_upstream_state: a configured upstream prints has-upstream and names it" {
  _upstream_publish
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_upstream_state '$REPO'"
  [ "$status" -eq 0 ]
  [ "${output%%$'\t'*}" = "has-upstream" ]
  [ "${output#*$'\t'}" = "origin/main" ]
}

@test "pnwf_upstream_state: a path that EXISTS but is not a git repo is indeterminate, NOT no-upstream" {
  # THE CRUX. Before the fix the caller read rev-parse's 128 here as "no
  # upstream configured" -- the required state -- and proceeded, so a member
  # that did have an upstream could be pushed by the relock. Both halves are
  # asserted: the state IS indeterminate, and it is NOT no-upstream.
  mkdir -p "$TEST_DIR/not-a-repo"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_upstream_state '$TEST_DIR/not-a-repo'"
  [ "$status" -eq 0 ]
  [ "${output%%$'\t'*}" = "indeterminate" ]
  [[ "$output" != *"no-upstream"* ]]
  [[ "$output" == *"$TEST_DIR/not-a-repo"* ]]
}

@test "pnwf_upstream_state: a non-repo directory NESTED in a repo is indeterminate (rev-parse walks up)" {
  # Why the confirmation is `rev-parse --show-prefix` and not `--git-dir`:
  # rev-parse walks UP, so from here `--git-dir` succeeds and answers for the
  # ENCLOSING repo -- whose upstream is not this path's. Asserted here to keep a
  # later "simplification" back to `--git-dir` from passing.
  mkdir -p "$REPO/nested-not-a-repo"
  _upstream_publish
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_upstream_state '$REPO/nested-not-a-repo'"
  [ "$status" -eq 0 ]
  [ "${output%%$'\t'*}" = "indeterminate" ]
  # NOT the enclosing repo's answer, which `--git-dir` would have produced.
  [[ "$output" != *"has-upstream"* ]]
  [[ "$output" != *"origin/main"* ]]
}

@test "pnwf_upstream_state: a detached HEAD prints no-upstream (pn's hasUpstream reads it the same way)" {
  # Documented subsumption: `pn` runs the same rev-parse and treats any error
  # as "no upstream", so it would not push a detached member either -- the
  # answer that matters to the no-remote-write guard.
  command git -C "$REPO" checkout -q --detach HEAD
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_upstream_state '$REPO'"
  [ "$status" -eq 0 ]
  [ "${output%%$'\t'*}" = "no-upstream" ]
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

# --- pnwf_worktree_root_ok ------------------------------------------------

# Redirects $REPO's working tree to a decoy directory holding an IDENTICAL
# checkout, reproducing the 2026-08-14 homelab defect: a stale `core.worktree`
# left by an interrupted `pn workspace update`. The decoy must match the
# committed content, otherwise `git status` reports the index's files as
# deleted and the redirect shows up as a plain dirty tree -- which is exactly
# the cheap-to-diagnose case this defect is NOT.
_redirect_worktree_to_decoy() {
  local decoy="$TEST_DIR/decoy"
  mkdir -p "$decoy"
  cp "$REPO/file.txt" "$decoy/file.txt"
  command git -C "$REPO" config core.worktree "$decoy"
  echo "$decoy"
}

@test "pnwf_worktree_root_ok: true for a normal clone" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_root_ok '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_worktree_root_ok: false when core.worktree redirects elsewhere" {
  _redirect_worktree_to_decoy >/dev/null
  # Bare call (not if-wrapped): see the non-vacuousness note at the top of
  # this file.
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_root_ok '$REPO'"
  [ "$status" -eq 1 ]
}

@test "pnwf_worktree_root_ok: the redirect is INVISIBLE to the on-primary/clean check" {
  # Non-vacuousness for the ORDERING requirement in pnwf.sh's fork-preflight:
  # with the redirect in place the R-3 check still reports healthy, so it
  # cannot be relied on to surface this. That is why pnwf_worktree_root_ok
  # runs first and reports its own reason.
  _redirect_worktree_to_decoy >/dev/null

  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 0 ]

  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_root_ok '$REPO'"
  [ "$status" -eq 1 ]
}

@test "pnwf_worktree_root_ok: false when core.worktree names a removed directory" {
  command git -C "$REPO" config core.worktree "$TEST_DIR/gone"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_root_ok '$REPO'"
  [ "$status" -eq 1 ]
}

@test "pnwf_worktree_root_ok: not a git repo propagates rc=128 with a diagnostic" {
  mkdir -p "$TEST_DIR/plain"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_root_ok '$TEST_DIR/plain'"
  [ "$status" -eq 128 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_worktree_root_ok: git rev-parse --show-toplevel failed (rc=128)"* ]]
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

# --- pnwf_rebase_in_progress -------------------------------------------
# The OBSERVABLE that separates a rebase git REFUSED TO START from one it
# started and stopped mid-way. Real git throughout, including a real LINKED
# WORKTREE: a pnwf member IS a worktree, and the two worktree layouts are the
# whole reason this probe cannot be `[ -d "$repo_dir/.git/rebase-merge" ]`.

_setup_conflicting_feature_branch() {
  # In $REPO: advance `main`, and leave a `feature` branch (checked out
  # NOWHERE, so either worktree below may claim it) whose single commit
  # rewrites the same file -- rebasing feature onto main is then a
  # guaranteed content conflict, i.e. a rebase git STARTS and cannot finish.
  echo main-side >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "main side"
  command git -C "$REPO" branch feature HEAD~1
  command git -C "$REPO" checkout -q feature
  echo feature-side >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "feature side"
  command git -C "$REPO" checkout -q main
}

@test "pnwf_rebase_in_progress: no rebase in progress returns false without aborting" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_rebase_in_progress '$REPO'; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

@test "pnwf_rebase_in_progress: a main worktree left mid-rebase is detected from an unrelated cwd" {
  _setup_conflicting_feature_branch
  command git -C "$REPO" checkout -q feature
  run bash -c "command git -C '$REPO' rebase main"
  [ "$status" -ne 0 ]

  # cwd is deliberately NOT $REPO. In a MAIN worktree `--git-path` answers
  # relative to git's own cwd (verified git 2.54), so the probe must
  # re-anchor that answer on repo_dir; anchoring it on the CALLER's cwd
  # would test $TEST_DIR/.git/rebase-merge and report "no rebase" here.
  run bash -euo pipefail -c "cd '$TEST_DIR'; source '$LIB_PATH'; pnwf_rebase_in_progress '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_rebase_in_progress: a LINKED worktree left mid-rebase is detected even though <wt>/.git holds no state dir" {
  _setup_conflicting_feature_branch
  local wt="$TEST_DIR/member-wt"
  command git -C "$REPO" worktree add -q "$wt" feature
  run bash -c "command git -C '$wt' rebase main"
  [ "$status" -ne 0 ]

  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_rebase_in_progress '$wt'"
  [ "$status" -eq 0 ]

  # What makes the assertion above discriminating rather than a restatement:
  # a linked worktree's rebase state lives under the CANONICAL clone's
  # .git/worktrees/<name>/, and <wt>/.git is a gitfile, not a directory --
  # so the hardcoded path this probe deliberately avoids cannot exist here,
  # and a `[ -d "$repo_dir/.git/rebase-merge" ]` implementation would report
  # "no rebase in progress" for a worktree that IS mid-rebase.
  [ ! -d "$wt/.git" ]
  [ ! -d "$wt/.git/rebase-merge" ]
  [ ! -d "$wt/.git/rebase-apply" ]
  [ -d "$REPO/.git/worktrees/member-wt/rebase-merge" ] ||
    [ -d "$REPO/.git/worktrees/member-wt/rebase-apply" ]
}

@test "pnwf_rebase_in_progress: an unreadable repo does not abort, returns git's rc, and prints a diagnostic" {
  # NOT reported as "no rebase in progress": the observable could not be
  # read, so the caller (pnwf_fetch_and_rebase) must be able to tell that
  # apart from a confident false and refuse to assert either recovery.
  mkdir -p "$TEST_DIR/not-a-repo"
  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_rebase_in_progress '$TEST_DIR/not-a-repo'"
  [ "$status" -ne 0 ]
  [ "$status" -ne 1 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_rebase_in_progress: git rev-parse --git-path rebase-merge failed in $TEST_DIR/not-a-repo"* ]]
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
  # 3 (not just "nonzero"): the MID-REBASE sentinel -- the caller
  # (cmd_sync_fetch) switches on this to pick "git rebase --continue"
  # wording rather than the fetch-failure wording (rc=2) or the
  # refused-rebase wording (rc=4), so the exact code is load-bearing here,
  # not incidental.
  [ "$status" -eq 3 ]

  # git itself leaves the rebase mid-progress -- pnwf_fetch_and_rebase MUST
  # NOT `git rebase --abort` it; the hand-off contract is `git rebase
  # --continue` in this exact worktree.
  [ -d "$CLONE/.git/rebase-apply" ] || [ -d "$CLONE/.git/rebase-merge" ]
}

@test "pnwf_fetch_and_rebase: a REFUSED rebase returns the NEVER-STARTED sentinel, not the mid-rebase one, and leaves no rebase state" {
  # The bd pg2-k3s0x case. `git rebase` refuses OUTRIGHT: it exits non-zero
  # having started nothing, so the mid-rebase sentinel (3) -- and the `git
  # rebase --continue` hand-off it selects -- would be actively wrong advice.
  #
  # The CAUSE modelled here is a `pre-rebase` hook veto, NOT the dirty tree
  # this test used to use (bd pg2-lgzcg): a dirty tree no longer reaches the
  # rebase at all -- the pre-check refuses it first, as 6 -- and with
  # `rebase.autoStash` on it would not have made git refuse anyway. A hook
  # veto is a REAL remaining cause of 4, verified on git 2.54 with
  # autoStash=true: exit 1, "the pre-rebase hook refused to rebase", and no
  # rebase state directory, on a CLEAN tree. It is hermetic (a repo-local
  # hook file, no git config involved).
  _setup_fetch_and_rebase_origin
  command git -C "$CLONE" checkout -q -b feature

  # origin advances, so a rebase genuinely has commits to replay: this test
  # must fail on the REFUSAL, not on there being nothing to do.
  echo origin-advance >"$REPO/other-file.txt"
  command git -C "$REPO" add other-file.txt
  command git -C "$REPO" commit -q -m "origin advance"
  command git -C "$REPO" push -q "$ORIGIN" main

  cat >"$CLONE/.git/hooks/pre-rebase" <<'HOOK'
#!/bin/sh
echo "pre-rebase hook refuses in this test" >&2
exit 1
HOOK
  chmod +x "$CLONE/.git/hooks/pre-rebase"

  # The tree is CLEAN, so the dirtiness pre-check passes it through to the
  # rebase -- which is what makes this a test of 4 rather than of 6.
  run bash -c "command git -C '$CLONE' status --porcelain"
  [ -z "$output" ]

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  [ "$status" -eq 4 ]

  # The rest of the contract is unchanged: a first-party diagnostic still
  # names the git step that failed and its real exit code.
  [[ "$stderr" == *"pnwf_fetch_and_rebase: git rebase origin/main failed in $CLONE"* ]]

  # And the observable the sentinel was derived from: NOTHING is mid-rebase,
  # so there is nothing for `git rebase --continue` to continue.
  [ ! -d "$CLONE/.git/rebase-apply" ]
  [ ! -d "$CLONE/.git/rebase-merge" ]
}

# --- pnwf_fetch_and_rebase: the dirtiness PRE-CHECK ----------------------
# bd pg2-lgzcg. `git rebase` refuses on a dirty tree only while
# `rebase.autoStash` is OFF, so relying on git to refuse makes this helper's
# behaviour depend on a setting outside the repo -- and on THIS machine
# (`~/.config/git/config`, the XDG file `git config --global --get` does not
# read) it is ON, which is the configuration where a dirty member returns 0
# from `pnwf_fetch_and_rebase` while its worktree is left conflicted.
#
# BOTH configurations are pinned below, and BOTH set `rebase.autoStash`
# EXPLICITLY in the test's own repo-local config. That is load-bearing twice
# over: setup_file exports GIT_CONFIG_GLOBAL=/dev/null (beads pg2-klyn6 /
# pg2-7hr6o), so the ambient value cannot reach these tests at all; and a
# test that DID depend on the developer's setting would be an instance of the
# very defect this pair exists to close.

_setup_dirty_member_against_advanced_origin() {
  # A real member whose worktree holds an uncommitted change to a TRACKED
  # file, with origin genuinely ahead so a rebase has commits to replay --
  # the test must turn on the DIRTINESS, not on there being nothing to do.
  # The uncommitted change touches the SAME file the origin advance rewrites,
  # which is what makes the unfixed path leave a CONFLICTED autostash rather
  # than a clean pop: the worst reachable outcome, asserted against below.
  _setup_fetch_and_rebase_origin
  command git -C "$CLONE" checkout -q -b feature

  echo origin-side >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "origin change"
  command git -C "$REPO" push -q "$ORIGIN" main

  echo uncommitted-work >"$CLONE/file.txt"

  # Recorded BEFORE the call under test, so "nothing moved" is asserted
  # against observed values rather than against an assumed shape. The
  # remote-tracking ref is deliberately STALE here (the push above landed
  # after the clone), which is what makes it evidence about the FETCH.
  FEATURE_SHA_BEFORE=$(command git -C "$CLONE" rev-parse feature)
  ORIGIN_MAIN_SHA_BEFORE=$(command git -C "$CLONE" rev-parse origin/main)
  export FEATURE_SHA_BEFORE ORIGIN_MAIN_SHA_BEFORE
}

# Asserts the whole point of the pre-check: the member was left exactly as
# found -- unfetched, unrebased, unstashed, unconflicted -- because deciding
# the fate of work pnwf did not create is not pnwf's call.
_assert_member_untouched_by_refusal() {
  # The operator's uncommitted work is still in the working tree, verbatim.
  run cat "$CLONE/file.txt"
  [ "$output" = "uncommitted-work" ]

  # NOT stashed. On the unfixed path this is where the work ends up when the
  # autostash pop conflicts ("Your changes are safe in the stash"), orphaned
  # from any command that would put it back.
  run bash -c "command git -C '$CLONE' stash list"
  [ -z "$output" ]

  # No conflict markers, and nothing mid-rebase.
  run bash -c "command git -C '$CLONE' status --porcelain"
  [[ "$output" != *"UU "* ]]
  [ ! -d "$CLONE/.git/rebase-apply" ]
  [ ! -d "$CLONE/.git/rebase-merge" ]

  # The branch tip did NOT move, so no rebase replayed anything. This is what
  # separates "refused" from "passed": under autoStash=true the unfixed path
  # DOES advance this tip (it rebases successfully; only the pop fails).
  run bash -c "command git -C '$CLONE' rev-parse feature"
  [ "$output" = "$FEATURE_SHA_BEFORE" ]

  # The stale remote-tracking ref did NOT advance either, so `git fetch` was
  # never run: the pre-check precedes the fetch, so a member that is already a
  # decided stop costs no network round trip.
  run bash -c "command git -C '$CLONE' rev-parse origin/main"
  [ "$output" = "$ORIGIN_MAIN_SHA_BEFORE" ]
}

@test "pnwf_fetch_and_rebase: a DIRTY member is refused with its own sentinel under rebase.autoStash=true, where git itself reports SUCCESS" {
  _setup_dirty_member_against_advanced_origin
  # EXPLICIT, repo-local: the harness is hermetic against the ambient value.
  command git -C "$CLONE" config rebase.autoStash true

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"

  # 6, and specifically NOT 0. Without the pre-check this call returns 0 --
  # git autostashes, rebases, fails to pop, and STILL exits 0 (verified git
  # 2.54: "Applying autostash resulted in conflicts" AND "Successfully
  # rebased" together) -- so the caller reads a clean pass and moves to the
  # next member while this worktree sits at `UU file.txt`. Neither 3 nor 4
  # can catch it either: git refused nothing and started nothing that stopped.
  [ "$status" -eq 6 ]
  [[ "$stderr" == *"pnwf_fetch_and_rebase: $CLONE has uncommitted changes"* ]]
  # The diagnostic must not offer the mid-rebase recovery: nothing started.
  [[ "$stderr" != *"rebase --continue"* ]]
  [[ "$stderr" != *"rebase --abort"* ]]

  _assert_member_untouched_by_refusal
}

@test "pnwf_fetch_and_rebase: a DIRTY member is refused with the SAME sentinel under rebase.autoStash=false, where git would have refused" {
  _setup_dirty_member_against_advanced_origin
  # EXPLICIT, repo-local: pins the other machine configuration, so the
  # outcome stops depending on which one the reader happens to have.
  command git -C "$CLONE" config rebase.autoStash false

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"

  # 6, NOT 4: git WOULD have refused this one ("cannot rebase: You have
  # unstaged changes"), but the answer must not depend on that -- the
  # sentinel, the diagnostic and the state left behind are identical to the
  # autoStash=true case above, which is the property that makes the behaviour
  # configuration-independent. 4 now means a refusal on a CLEAN tree only.
  [ "$status" -eq 6 ]
  [[ "$stderr" == *"pnwf_fetch_and_rebase: $CLONE has uncommitted changes"* ]]
  [[ "$stderr" != *"git rebase origin/main failed"* ]]

  _assert_member_untouched_by_refusal
}

@test "pnwf_fetch_and_rebase: an UNREADABLE working tree yields the INDETERMINATE-DIRTINESS sentinel and attempts nothing" {
  # The path exists (pnwf_worktree_present is a plain path check, so
  # cmd_sync_fetch can hand one over) but is not a git repo, so whether a
  # rebase is SAFE to attempt cannot be read. Same discipline as sentinel 5,
  # one step earlier: assert NO cause rather than guess one. A distinct code,
  # not the probe's own rc (128), which would reach the caller as an
  # unrecognised sentinel.
  mkdir -p "$TEST_DIR/not-a-repo"

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$TEST_DIR/not-a-repo' main"
  [ "$status" -eq 7 ]
  [[ "$stderr" == *"pnwf_fetch_and_rebase: could not determine whether $TEST_DIR/not-a-repo has uncommitted changes"* ]]
  # No recovery is asserted -- neither the dirty-tree one nor a rebase one.
  [[ "$stderr" != *"commit or stash"* ]]
  [[ "$stderr" != *"rebase --continue"* ]]
}

@test "pnwf_fetch_and_rebase: git fetch failure does not abort, returns the FETCH-step sentinel, and prints a diagnostic" {
  _setup_fetch_and_rebase_origin
  command git -C "$CLONE" checkout -q -b feature
  command git -C "$CLONE" remote set-url origin "$TEST_DIR/does-not-exist.git"

  run --separate-stderr bash -euo pipefail -c "source '$LIB_PATH'; pnwf_fetch_and_rebase '$CLONE' main"
  # 2 (not just "nonzero"): the FETCH-step sentinel, distinct from the
  # mid-rebase sentinel (3) above -- no rebase was ever started here, so
  # the caller must NOT tell the operator to `git rebase --continue`. It is
  # also distinct from the refused-rebase sentinel (4), which shares
  # "no rebase started" but NOT the recovery: this one is a
  # remote/network/auth problem, that one is a dirty tree to commit or stash.
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ "$stderr" == *"pnwf_fetch_and_rebase: git fetch origin failed in $CLONE"* ]]
}

# --- harness hermeticity guard --------------------------------------------

@test "setup() relocates HOME off the developer's own (pg2-7hr6o regression guard)" {
  # Discriminating guard for the HOME isolation setup() installs, mirroring the
  # one in lib/tests/test-update-locks-lib.bats: drop `export HOME` from setup()
  # and HOME is the developer's real one, so the equality fails; drop the whole
  # block and $TEST_DIR/home does not exist, so the -d fails. Either way it goes
  # red, which is what separates a guard from a restatement.
  #
  # The real ~ is never touched, read, or probed: the assertions look only at the
  # temp dir setup() created, and a fresh mktemp path can never BE the
  # developer's home, so proving isolation needs no reference to the real path.
  [ -n "${TEST_DIR:-}" ]
  [ "$HOME" = "$TEST_DIR/home" ]
  [ -d "$HOME" ]
  # Empty, i.e. nothing of the developer's is reachable through $HOME.
  [ -z "$(ls -A "$HOME")" ]
}
