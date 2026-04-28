#!/usr/bin/env bats

# Tests for pn-store-audit script

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

# pn-lib path for tests (set by nix check or resolved locally)
if [[ -z ${LIB_PATH:-} ]]; then
  LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash"
fi

# Load test support
if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

# Create mock nix-env that returns a simple generation list
create_mock_nix_env_audit() {
  local today
  today=$(date +%Y-%m-%d)
  cat > "$TEST_DIR/nix-env" <<EOF
#!/usr/bin/env bash
if [[ "\$*" == *"--list-generations"* ]]; then
  echo "   1   2024-01-01 12:00:00"
  echo "   2   ${today} 12:00:00   (current)"
  exit 0
fi
exit 0
EOF
  chmod +x "$TEST_DIR/nix-env"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock nix path-info
create_mock_nix_path_info_audit() {
  cat > "$TEST_DIR/nix" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "path-info" && "$2" == "-S" ]]; then
  echo "/nix/store/fakehash-pkg  1048576"
  exit 0
fi
exit 0
EOF
  chmod +x "$TEST_DIR/nix"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock df
create_mock_df_audit() {
  cat > "$TEST_DIR/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem 512-blocks Used Available Capacity Mounted on"
echo "/dev/disk1 999999999 2097152 500000000 50% /nix"
EOF
  chmod +x "$TEST_DIR/df"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock nix-store for dead paths
create_mock_nix_store_dead() {
  cat > "$TEST_DIR/nix-store" <<'EOF'
#!/usr/bin/env bash
if [[ "$*" == *"--print-dead"* ]]; then
  echo "/nix/store/aaa-dead"
  exit 0
fi
exit 0
EOF
  chmod +x "$TEST_DIR/nix-store"
  export PATH="$TEST_DIR:$PATH"
}

# Helper to create a wrapper that sources pn-lib then runs the script
create_pn_lib_wrapper() {
  local lib_path="${LIB_PATH%%:*}"
  cat >"$TEST_DIR/run_with_lib" <<WRAPPER
#!/usr/bin/env bash
script_path="\$1"
shift
source "${lib_path}"
source "\${script_path}"
WRAPPER
  chmod +x "$TEST_DIR/run_with_lib"
}

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR
  export REAL_HOME="$HOME"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export XDG_CONFIG_HOME="$TEST_DIR/config"
  mkdir -p "$XDG_CONFIG_HOME/pn"
  create_pn_lib_wrapper

  # Default config: empty search_dirs
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = []
keep_days = 14
keep_count = 3
EOF
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "pn-store-audit --help shows Usage" {
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh" --help
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "pn-store-audit -h shows Usage" {
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh" -h
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "pn-store-audit output includes System Profiles section" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "System"
}

@test "pn-store-audit output includes Home Manager section" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  # Create the home manager profile path so it exists
  local hm_profile="$HOME/.local/state/nix/profiles/home-manager"
  mkdir -p "$(dirname "$hm_profile")"
  touch "$hm_profile"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Home Manager"
}

@test "pn-store-audit output includes Nix Store section" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Nix Store"
}

@test "pn-store-audit reads search_dirs from XDG config" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  # Configure a non-existent search dir — script should still succeed
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = ["$TEST_DIR/nonexistent-projects"]
keep_days = 14
keep_count = 3
EOF

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
}

@test "pn-store-audit handles missing XDG config gracefully (defaults to HOME)" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  # Remove the config file entirely
  rm -f "$XDG_CONFIG_HOME/pn/store.toml"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
}

@test "pn-store-audit handles unmounted volume (non-existent search dir) gracefully" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = ["/Volumes/NonExistentDisk/projects"]
keep_days = 14
keep_count = 3
EOF

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
}

@test "pn-store-audit without --full does NOT show reclaimable" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh"
  [ "$status" -eq 0 ]
  # Verify the word "Reclaimable" does not appear
  [[ "$output" != *"Reclaimable"* ]]
}

@test "pn-store-audit --full includes dead/reclaimable output" {
  create_mock_nix_env_audit
  create_mock_nix_path_info_audit
  create_mock_df_audit
  create_mock_sudo
  create_mock_nix_store_dead

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-audit.sh" --full
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "reclaimable\|dead"
}
