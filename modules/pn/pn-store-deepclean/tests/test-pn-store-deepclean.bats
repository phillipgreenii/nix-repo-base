#!/usr/bin/env bats

# Tests for pn-store-deepclean script

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

# Create mock nix-env that logs deletes and returns some generations to prune
create_mock_nix_env_clean() {
  local today
  today=$(date +%Y-%m-%d)
  local d50
  d50=$(date -d "50 days ago" +%Y-%m-%d 2>/dev/null || date -v-50d +%Y-%m-%d)
  cat > "$TEST_DIR/nix-env" <<EOF
#!/usr/bin/env bash
if [[ "\$*" == *"--list-generations"* ]]; then
  echo "   1   ${d50} 12:00:00"
  echo "   2   ${today} 12:00:00   (current)"
  exit 0
fi
if [[ "\$*" == *"--delete-generations"* ]]; then
  echo "DELETED: \$*" >> "$TEST_DIR/nix-env-deletes.log"
  exit 0
fi
exit 0
EOF
  chmod +x "$TEST_DIR/nix-env"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock nix path-info
create_mock_nix_path_info_clean() {
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
create_mock_df_clean() {
  cat > "$TEST_DIR/df" <<'EOF'
#!/usr/bin/env bash
echo "Filesystem 512-blocks Used Available Capacity Mounted on"
echo "/dev/disk1 999999999 2097152 500000000 50% /nix"
EOF
  chmod +x "$TEST_DIR/df"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock nix-store that logs gc calls
create_mock_nix_store_gc() {
  cat > "$TEST_DIR/nix-store" <<EOF
#!/usr/bin/env bash
if [[ "\$*" == *"--gc"* && "\$*" != *"--print-dead"* ]]; then
  echo "GC_CALLED" >> "$TEST_DIR/nix-store-gc.log"
  exit 0
fi
if [[ "\$*" == *"--print-dead"* ]]; then
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

@test "pn-store-deepclean --help shows Usage" {
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --help
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "pn-store-deepclean -h shows Usage" {
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" -h
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "pn-store-deepclean --dry-run does NOT call nix-env --delete-generations" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --dry-run --keep-since 0d --keep 0
  [ "$status" -eq 0 ]
  # delete-generations log should NOT exist
  [ ! -f "$TEST_DIR/nix-env-deletes.log" ]
}

@test "pn-store-deepclean --dry-run does NOT call nix-store --gc" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --dry-run --keep-since 0d --keep 0
  [ "$status" -eq 0 ]
  # gc log should NOT exist
  [ ! -f "$TEST_DIR/nix-store-gc.log" ]
}

@test "pn-store-deepclean live run calls sudo nix-store --gc" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --keep-since 0d --keep 1
  [ "$status" -eq 0 ]
  [ -f "$TEST_DIR/nix-store-gc.log" ]
  grep -q "GC_CALLED" "$TEST_DIR/nix-store-gc.log"
}

@test "pn-store-deepclean --keep-since 7d overrides config" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  # With 7d keep-since and keep=1, gen1 (50 days old) should be pruned
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --keep-since 7d --keep 1
  [ "$status" -eq 0 ]
  # GC should have been called (live run)
  [ -f "$TEST_DIR/nix-store-gc.log" ]
}

@test "pn-store-deepclean --keep 0 overrides config" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --keep-since 0d --keep 0
  [ "$status" -eq 0 ]
  # With keep=0 and keep-since=0d, gen1 (non-current) should be pruned
  [ -f "$TEST_DIR/nix-env-deletes.log" ]
  grep -q "1" "$TEST_DIR/nix-env-deletes.log"
}

@test "pn-store-deepclean --keep-since 0d --keep 1 prunes aggressively" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --keep-since 0d --keep 1
  [ "$status" -eq 0 ]
  # gen1 should have been deleted (only current gen2 is protected by keep=1)
  [ -f "$TEST_DIR/nix-env-deletes.log" ]
  grep -q "1" "$TEST_DIR/nix-env-deletes.log"
}

@test "pn-store-deepclean without overrides uses config defaults" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  # Config has keep_days=14, keep_count=3
  # gen1 is 50 days old — outside time protection, but within count=3 from 2 total gens
  # So gen1 is protected by count (top 3 of 2 = all) → nothing deleted
  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  # GC still called since we ran live
  [ -f "$TEST_DIR/nix-store-gc.log" ]
}

@test "pn-store-deepclean reads keep_days from XDG config" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  # Set keep_days=7 in config; gen1 is 50 days old, keep_count=1 → gen1 pruned
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = []
keep_days = 7
keep_count = 1
EOF

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  [ -f "$TEST_DIR/nix-env-deletes.log" ]
  grep -q "1" "$TEST_DIR/nix-env-deletes.log"
}

@test "pn-store-deepclean handles missing XDG config gracefully" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  # Remove the config file entirely — defaults kick in
  rm -f "$XDG_CONFIG_HOME/pn/store.toml"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
}

@test "pn-store-deepclean removes result symlinks in search dirs" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  # Set up search dirs in config
  local proj="$TEST_DIR/projects/myrepo"
  mkdir -p "$proj"
  ln -s /nix/store/fakehash-pkg "$proj/result"
  ln -s /nix/store/fakehash-check "$proj/result-1"

  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = ["$TEST_DIR/projects"]
keep_days = 14
keep_count = 3
EOF

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  # Symlinks should be deleted
  [ ! -L "$proj/result" ]
  [ ! -L "$proj/result-1" ]
}

@test "pn-store-deepclean --dry-run does NOT remove result symlinks" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  local proj="$TEST_DIR/projects/myrepo"
  mkdir -p "$proj"
  ln -s /nix/store/fakehash-pkg "$proj/result"

  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = ["$TEST_DIR/projects"]
keep_days = 14
keep_count = 3
EOF

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --dry-run
  [ "$status" -eq 0 ]
  # Symlink should still exist
  [ -L "$proj/result" ]
}

@test "pn-store-deepclean removes stale nix-profiles entries" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  local profiles_dir="$HOME/.nix-profiles"
  mkdir -p "$profiles_dir"
  ln -s /nix/store/fakehash-old "$profiles_dir/old-env-1-link"
  # -h modifies the symlink itself, not the target (critical on macOS)
  touch -h -t "$(date -d '30 days ago' +%Y%m%d%H%M 2>/dev/null || date -v-30d +%Y%m%d%H%M)" "$profiles_dir/old-env-1-link"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  [ ! -L "$profiles_dir/old-env-1-link" ]
}

@test "pn-store-deepclean removes nh-darwin temp roots" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  local nh_dir="$TEST_DIR/tmp/nh-darwinABCDEF"
  mkdir -p "$nh_dir"
  ln -s /nix/store/fakehash-system "$nh_dir/result"
  export TMPDIR="$TEST_DIR/tmp"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  [ ! -L "$nh_dir/result" ]
}

@test "pn-store-deepclean --dry-run does NOT remove stale nix-profiles entries" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo
  create_mock_nix_store_gc

  local profiles_dir="$HOME/.nix-profiles"
  mkdir -p "$profiles_dir"
  ln -s /nix/store/fakehash-old "$profiles_dir/old-env-1-link"
  # -h modifies the symlink itself, not the target (critical on macOS)
  touch -h -t "$(date -d '30 days ago' +%Y%m%d%H%M 2>/dev/null || date -v-30d +%Y%m%d%H%M)" "$profiles_dir/old-env-1-link"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh" --dry-run
  [ "$status" -eq 0 ]
  [ -L "$profiles_dir/old-env-1-link" ]
}

@test "pn-store-deepclean shows runtime roots summary after GC" {
  create_mock_nix_env_clean
  create_mock_nix_path_info_clean
  create_mock_df_clean
  create_mock_sudo

  # Mock nix-store that supports --gc, --print-roots, and --print-dead
  cat > "$TEST_DIR/nix-store" <<MOCK
#!/usr/bin/env bash
if [[ "\$*" == *"--print-roots"* ]]; then
  echo "{lsof} -> /nix/store/aaa-pkg"
  echo "/some/file -> /nix/store/bbb-pkg"
  exit 0
fi
if [[ "\$*" == *"--gc"* && "\$*" != *"--print-dead"* ]]; then
  echo "GC_CALLED" >> "$TEST_DIR/nix-store-gc.log"
  exit 0
fi
if [[ "\$*" == *"--print-dead"* ]]; then
  echo "/nix/store/aaa-dead"
  exit 0
fi
exit 0
MOCK
  chmod +x "$TEST_DIR/nix-store"
  export PATH="$TEST_DIR:$PATH"

  run "$TEST_DIR/run_with_lib" "$SCRIPTS_DIR/pn-store-deepclean.sh"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "store path.*held only by running processes"
}
