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
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export REAL_HOME="${HOME:-}"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  # Overriding HOME alone is NOT enough isolation: home-manager's git module
  # writes identity to $XDG_CONFIG_HOME/git/config, and the ambient
  # environment sets XDG_CONFIG_HOME to an absolute path independent of
  # HOME -- so without this, a test relying on "no identity resolvable"
  # (below) silently resolves the REAL machine identity instead. GIT_CONFIG_
  # NOSYSTEM also guards against a populated /etc/gitconfig on some other
  # machine running this suite.
  export XDG_CONFIG_HOME="$TEST_DIR/xdg-config"
  export GIT_CONFIG_NOSYSTEM=1

  create_pg_git_check_identity_wrapper
  SCRIPT="$TEST_DIR/run_pg_git_check_identity"

  REPO="$TEST_DIR/repo"
  mkdir -p "$REPO"
  cd "$REPO" || return 1
  git init -q -b main
}

teardown() {
  cd "$TEST_DIR" || cd /
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
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
  # No env vars, no repo-local config, and setup()'s isolated HOME has no
  # ~/.gitconfig -- git cannot resolve any identity at all. This must NOT
  # exit 0 (silently "passing" an unattributable commit) and must NOT exit 3
  # (which specifically means "identity looks like a placeholder" -- an
  # unresolved identity is a different failure than a resolved-but-fake one).
  unset GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL

  run "$SCRIPT"
  [ "$status" -ne 0 ]
  [ "$status" -ne 3 ]
}
