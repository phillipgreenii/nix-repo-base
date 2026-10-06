#!/usr/bin/env bats
# bats file_tags=type:unit
# shellcheck disable=SC1090,SC2030,SC2031

# SCRIPTS_DIR set by mkBashScript's check derivation. Fall back to the
# source tree for local development (`bats tests/` with no nix build).
SCRIPTS_DIR="${SCRIPTS_DIR:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)}"

# Replace the shebang on $1 with one that uses an absolute bash path.
# Required in the Nix build sandbox, where /usr/bin/env is not visible.
# Avoids `sed -i`: BSD sed (macOS /usr/bin/sed, used for local `bats tests/`
# runs outside the Nix sandbox) takes a mandatory extension argument there,
# unlike GNU sed -- a portable rewrite sidesteps the flavor difference.
_fix_mock_shebang() {
  local tmp
  tmp="$(mktemp)"
  { printf '#!%s\n' "$(command -v bash)"; tail -n +2 "$1"; } >"$tmp"
  mv "$tmp" "$1"
}

# Wrapper that replicates the builder's composition order (.bash sourced
# before .sh) without going through a full nix build.
create_pg_git_check_identity_wrapper() {
  cat >"$TEST_DIR/run_pg_git_check_identity" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/pg-git-check-identity.bash"
source "${SCRIPTS_DIR}/pg-git-check-identity.sh"
WRAPPER
  _fix_mock_shebang "$TEST_DIR/run_pg_git_check_identity"
  chmod +x "$TEST_DIR/run_pg_git_check_identity"
}

setup() {
  # Hermetic git fixture (bead pg2-mf90x; rule "Tests That Need Git"): the
  # shared harness owns the fixture root, a fresh empty HOME, the
  # GIT_CEILING_DIRECTORIES boundary, GIT_CONFIG_SYSTEM=/dev/null and an
  # allowlist env rebuild. That rebuild drops every inherited GIT_DIR-family
  # variable AND any ambient GIT_AUTHOR_*/GIT_COMMITTER_* identity (which would
  # outrank repo config in `git var`, e.g. under pg-test-runner), so this suite
  # carries no GIT_*/XDG_* scrub of its own. In the nix check the harness comes
  # in as testSupport (BATS_SUPPORT_PATH); locally it is the repo's canonical copy.
  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"

  # gfh_setup rebuilds the exported environment from an allowlist, which would
  # drop SCRIPTS_DIR (exported by the nix check).
  gfh_save_env SCRIPTS_DIR
  gfh_setup test-pg-git-check-identity
  gfh_restore_env

  TEST_DIR="$GFH_WORK"
  export TEST_DIR

  create_pg_git_check_identity_wrapper
  SCRIPT="$TEST_DIR/run_pg_git_check_identity"

  # GFH_REPO carries the harness's own local fixture identity; the tests below
  # that need a different (or no) identity set/replace it explicitly.
  REPO="$GFH_REPO"
  cd "$REPO" || return 1
}

teardown() {
  cd / || true
  gfh_teardown
}

@test "--help prints usage and exits 0" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage:" ]]
}

@test "an unknown option is rejected with exit code 2" {
  run "$SCRIPT" --nope
  [ "$status" -eq 2 ]
  [[ "$output" =~ "unknown argument" ]]
}

@test "a real-looking author and committer identity passes" {
  export GIT_AUTHOR_NAME="Phillip Green"
  export GIT_AUTHOR_EMAIL="phillipg@ziprecruiter.com"
  export GIT_COMMITTER_NAME="Phillip Green"
  export GIT_COMMITTER_EMAIL="phillipg@ziprecruiter.com"

  run "$SCRIPT"
  [ "$status" -eq 0 ]
}

@test "a placeholder author email is rejected with exit code 3" {
  export GIT_AUTHOR_NAME="Real Name"
  export GIT_AUTHOR_EMAIL="real@example.com"
  export GIT_COMMITTER_NAME="Real Name"
  export GIT_COMMITTER_EMAIL="real@realcorp.com"

  run "$SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" =~ "author" ]]
}

@test "a placeholder committer name is rejected with exit code 3" {
  export GIT_AUTHOR_NAME="Real Name"
  export GIT_AUTHOR_EMAIL="real@realcorp.com"
  export GIT_COMMITTER_NAME="Test User"
  export GIT_COMMITTER_EMAIL="tu@realcorp.com"

  run "$SCRIPT"
  [ "$status" -eq 3 ]
  [[ "$output" =~ "committer" ]]
}

@test "the repo's own non-human account convention is not rejected" {
  export GIT_AUTHOR_NAME="tcagent"
  export GIT_AUTHOR_EMAIL="tcagent@non-human.invalid"
  export GIT_COMMITTER_NAME="tcagent"
  export GIT_COMMITTER_EMAIL="tcagent@non-human.invalid"

  run "$SCRIPT"
  [ "$status" -eq 0 ]
}

# `git var` resolves identity from env vars OR config; every test above only
# exercises the env-var path. These exercise the config path specifically
# (the other half of the resolution order the tool's own docs claim to
# cover).

@test "a placeholder identity set via git config (not env vars) is still rejected" {
  git config user.name "Test User"
  git config user.email "tu@example.com"

  run "$SCRIPT"
  [ "$status" -eq 3 ]
}

@test "a real identity set via git config (not env vars) passes" {
  git config user.name "Phillip Green"
  git config user.email "phillipg@ziprecruiter.com"

  run "$SCRIPT"
  [ "$status" -eq 0 ]
}

@test "a completely unresolved identity fails loudly rather than silently passing" {
  # No env vars (the harness rebuilt the environment without any), no
  # repo-local identity (--no-identity also sets user.useConfigOnly so git will
  # not guess one from the hostname), and the harness's isolated HOME has no
  # ~/.gitconfig -- git cannot resolve any identity at all. This must NOT
  # exit 0 (silently "passing" an unattributable commit) and must NOT exit 3
  # (which specifically means "identity looks like a placeholder" -- an
  # unresolved identity is a different failure than a resolved-but-fake one).
  gfh_init_repo "$TEST_DIR/no-identity" test-pg-git-check-identity --no-identity
  cd "$TEST_DIR/no-identity" || return 1

  run "$SCRIPT"
  [ "$status" -ne 0 ]
  [ "$status" -ne 3 ]
}

# Regression for pg2-6drqh: under a leaked GIT_DIR/GIT_WORK_TREE (as in a
# linked-worktree commit hook) the identity-config tests must not touch the
# pointed-at repo. Nested bats runs one config-writing test; the filter
# excludes this test.
@test "config-writing tests under a leaked GIT_DIR leave that repo's config and HEAD byte-identical (pg2-6drqh)" {
  command -v bats >/dev/null || skip "bats not on PATH"
  local canon before_config before_head after_config after_head
  canon="$TEST_DIR/decoy"
  gfh_init_repo "$canon" test-pg-git-check-identity
  before_config="$(cat "$canon/.git/config")"
  before_head="$(cat "$canon/.git/HEAD")"

  run env GIT_DIR="$canon/.git" GIT_WORK_TREE="$canon" \
    GIT_INDEX_FILE="$canon/.git/index" \
    SCRIPTS_DIR="$SCRIPTS_DIR" \
    bats --filter 'set via git config' "$BATS_TEST_FILENAME"
  [ "$status" -eq 0 ]

  after_config="$(cat "$canon/.git/config")"
  after_head="$(cat "$canon/.git/HEAD")"
  [ "$before_config" = "$after_config" ]
  [ "$before_head" = "$after_head" ]
}
