#!/usr/bin/env bats
# shellcheck disable=SC1090

SCRIPT_DIR="$BATS_TEST_DIRNAME"
if [[ -n ${UL_LIB_SCRIPTS_DIR:-} ]]; then
  UL_LIB_SCRIPT="$UL_LIB_SCRIPTS_DIR/update-cache-lib.bash"
else
  UL_LIB_SCRIPT="$(cd "$SCRIPT_DIR/../scripts" && pwd)/update-cache-lib.bash"
fi


setup() {
  TEST_DIR=$(mktemp -d)
  export NIX_UL_FORCE_UPDATE="false"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"   # repo dir; stamps live under it
}

# Replace the shebang on $1 with one that uses an absolute bash path.
# Required for environments where /usr/bin/env doesn't exist (e.g. the
# Nix build sandbox, where only /nix/store paths are visible).
# Uses a temp file rather than `sed -i` so it works under both GNU and
# BSD/macOS sed (BSD `sed -i` requires a backup-suffix argument), keeping
# `bats lib/tests` a usable fast local loop on macOS (bead pg2-uepg7).
_fix_mock_shebang() {
  local f="$1" tmp
  tmp=$(mktemp)
  {
    printf '#!%s\n' "$(command -v bash)"
    tail -n +2 "$f"
  } >"$tmp"
  cat "$tmp" >"$f"
  rm -f "$tmp"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "ul_init sets _UL_PROJECT and _UL_STAMP_DIR under the repo" {
  [ "$_UL_PROJECT" = "my-project" ]
  [ "$_UL_STAMP_DIR" = "$TEST_DIR/repo/.update-locks/steps" ]
}

@test "ul_write_stamp creates the stamp file with an ISO-8601 UTC value" {
  ul_write_stamp "some-step"
  local f="$TEST_DIR/repo/.update-locks/steps/some-step"
  [ -f "$f" ]
  run cat "$f"
  [[ "$output" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
}

@test "_ul_iso_to_epoch round-trips a known timestamp" {
  run _ul_iso_to_epoch "2021-01-01T00:00:00Z"
  [ "$status" -eq 0 ]
  [ "$output" = "1609459200" ]
}

@test "_ul_iso_to_epoch fails on garbage" {
  run _ul_iso_to_epoch "not-a-date"
  [ "$status" -ne 0 ]
}

@test "_ul_iso_to_epoch rejects an empty value" {
  run _ul_iso_to_epoch ""
  [ "$status" -ne 0 ]
}

@test "ul_should_run returns 0 (fail-open) when stamp is empty" {
  mkdir -p "$_UL_STAMP_DIR"
  : > "$_UL_STAMP_DIR/some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

# --- ul_should_run (in-repo, value-based) ---

@test "ul_should_run returns 0 when no stamp exists" {
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ul_should_run returns 1 when stamp is fresh" {
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 1 ]
}

@test "ul_should_run returns 0 when stamp is stale" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "2020-01-01T00:00:00Z" > "$_UL_STAMP_DIR/some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 0 (fail-open) when stamp is unparseable" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "<<<<<<< HEAD" > "$_UL_STAMP_DIR/some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 0 when UL_FORCE is true even if fresh" {
  export NIX_UL_FORCE_UPDATE="true"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run does NOT bypass on UL_CI_MODE" {
  export UL_CI_MODE="true"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 1 ]   # CI now respects the shared gate
}

@test "ul_should_run skip message includes step name, timestamp, and remaining" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "2026-06-04T12:00:00Z" > "$_UL_STAMP_DIR/brew-update"
  # Force "fresh" by setting a huge window so it's always within it.
  export UL_STALE_SECONDS=999999999
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  run ul_should_run "brew-update"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "Skipping brew-update" ]]
  [[ "$output" =~ "2026-06-04T12:00:00Z" ]]
  [[ "$output" =~ "next eligible in" ]]
}

# --- ul_check_nix_daemon ---

@test "ul_check_nix_daemon succeeds silently when nix eval works" {
  # Mock nix that succeeds immediately
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  # Mock timeout that just runs the command
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
shift  # skip timeout value
exec "$@"
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  rm -rf "$MOCK_BIN"
}

@test "ul_check_nix_daemon exits 1 on timeout (non-interactive)" {
  # Mock timeout that simulates timeout exit code 124
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
exit 124
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  # Non-interactive: stdin is not a terminal (bats run already handles this)
  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 1 ]
  [[ "$output" =~ "unresponsive" ]]
}

@test "ul_check_nix_daemon exits 1 on nix failure (non-interactive)" {
  # Mock timeout that passes through to a failing nix
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
shift
exec "$@"
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 1 ]
  [[ "$output" =~ "nix daemon" ]]
  rm -rf "$MOCK_BIN"
}
