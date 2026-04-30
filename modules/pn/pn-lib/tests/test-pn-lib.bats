#!/usr/bin/env bats

# Tests for pn-lib shared library

# Resolve lib path
if [[ -z ${LIB_PATH:-} ]]; then
  LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)/pn-lib.bash"
fi

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR
  export REAL_HOME="$HOME"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export XDG_CONFIG_HOME="$TEST_DIR/config"
  mkdir -p "$XDG_CONFIG_HOME/pn"
  # shellcheck disable=SC1090
  source "$LIB_PATH"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# ─── Config reading functions ─────────────────────────────────────────────────

_write_store_toml() {
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = [
  "$TEST_DIR/projects",
  "$TEST_DIR/work"
]
keep_days = 14
keep_count = 3
EOF
}

@test "discover_search_dirs reads correct paths from store.toml" {
  _write_store_toml
  run discover_search_dirs
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "$TEST_DIR/projects"
  echo "$output" | grep -q "$TEST_DIR/work"
}

@test "discover_search_dirs returns nothing when config absent" {
  # No store.toml created
  run discover_search_dirs
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "read_keep_days returns value from store.toml" {
  _write_store_toml
  run read_keep_days
  [ "$status" -eq 0 ]
  [ "$output" = "14" ]
}

@test "read_keep_days returns default 14 when config absent" {
  run read_keep_days
  [ "$status" -eq 0 ]
  [ "$output" = "14" ]
}

@test "read_keep_count returns value from store.toml" {
  _write_store_toml
  run read_keep_count
  [ "$status" -eq 0 ]
  [ "$output" = "3" ]
}

@test "read_keep_count returns default 3 when config absent" {
  run read_keep_count
  [ "$status" -eq 0 ]
  [ "$output" = "3" ]
}

@test "read_keep_days returns default 14 when key absent from toml" {
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = []
EOF
  run read_keep_days
  [ "$status" -eq 0 ]
  [ "$output" = "14" ]
}

@test "read_keep_count returns default 3 when key absent from toml" {
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = []
EOF
  run read_keep_count
  [ "$status" -eq 0 ]
  [ "$output" = "3" ]
}

@test "discover_search_dirs handles empty search_dirs array" {
  cat > "$XDG_CONFIG_HOME/pn/store.toml" <<EOF
search_dirs = []
keep_days = 14
keep_count = 3
EOF
  run discover_search_dirs
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# ─── section_header and format_size ──────────────────────────────────────────

@test "section_header formats with === markers" {
  run section_header "My Section"
  [ "$status" -eq 0 ]
  [ "$output" = "=== My Section ===" ]
}

@test "format_size converts 500 bytes to '500 B'" {
  run format_size 500
  [ "$status" -eq 0 ]
  [ "$output" = "500 B" ]
}

@test "format_size converts 1024 bytes to '1.0 KB'" {
  run format_size 1024
  [ "$status" -eq 0 ]
  [ "$output" = "1.0 KB" ]
}

@test "format_size converts 1048576 bytes to '1.0 MB'" {
  run format_size 1048576
  [ "$status" -eq 0 ]
  [ "$output" = "1.0 MB" ]
}

@test "format_size converts 1073741824 bytes to '1.0 GB'" {
  run format_size 1073741824
  [ "$status" -eq 0 ]
  [ "$output" = "1.0 GB" ]
}

# ─── format_profile_label ────────────────────────────────────────────────────

@test "format_profile_label returns category name for system" {
  run format_profile_label "/nix/var/nix/profiles/system" system
  [ "$status" -eq 0 ]
  [ "$output" = "system" ]
}

@test "format_profile_label returns category name for home-manager" {
  run format_profile_label "/nix/var/nix/profiles/per-user/me/home-manager" home-manager
  [ "$status" -eq 0 ]
  [ "$output" = "home-manager" ]
}

@test "format_profile_label returns category name for devbox-global" {
  run format_profile_label "$HOME/.local/share/devbox/global/.devbox/nix/profile/default" devbox-global
  [ "$status" -eq 0 ]
  [ "$output" = "devbox-global" ]
}

@test "format_profile_label returns category name for devbox-util" {
  run format_profile_label "$HOME/.local/share/devbox/util/.devbox/nix/profile/default" devbox-util
  [ "$status" -eq 0 ]
  [ "$output" = "devbox-util" ]
}

@test "format_profile_label returns basename for user-profiles" {
  run format_profile_label "/nix/var/nix/profiles/per-user/me/channels" user-profiles
  [ "$status" -eq 0 ]
  [ "$output" = "channels" ]
}

@test "format_profile_label returns ~-relative project dir for devbox-projects under HOME" {
  run format_profile_label "$HOME/work/repo-alpha/.devbox/nix/profile/default" devbox-projects
  [ "$status" -eq 0 ]
  [ "$output" = "~/work/repo-alpha" ]
}

@test "format_profile_label returns absolute project dir for devbox-projects outside HOME" {
  run format_profile_label "/opt/projects/repo-beta/.devbox/nix/profile/default" devbox-projects
  [ "$status" -eq 0 ]
  [ "$output" = "/opt/projects/repo-beta" ]
}

# ─── find_workspace_root ─────────────────────────────────────────────────────

@test "find_workspace_root finds pn-workspace.toml in current directory" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"
  touch "$ws/pn-workspace.toml"
  cd "$ws"
  run find_workspace_root
  [ "$status" -eq 0 ]
  [ "$output" = "$ws" ]
}

@test "find_workspace_root returns 1 when no pn-workspace.toml anywhere" {
  # Use a temp dir with no workspace toml in ancestry
  local isolated
  isolated=$(mktemp -d)
  cd "$isolated"
  run find_workspace_root
  [ "$status" -eq 1 ]
  rmdir "$isolated" 2>/dev/null || true
}

@test "find_workspace_root walks up from subdirectory to find toml" {
  local ws="$TEST_DIR/workspace"
  local subdir="$ws/src/lib/foo"
  mkdir -p "$subdir"
  touch "$ws/pn-workspace.toml"
  cd "$subdir"
  run find_workspace_root
  [ "$status" -eq 0 ]
  [ "$output" = "$ws" ]
}

@test "find_workspace_root finds toml one level up" {
  local ws="$TEST_DIR/workspace"
  local subdir="$ws/myrepo"
  mkdir -p "$subdir"
  touch "$ws/pn-workspace.toml"
  cd "$subdir"
  run find_workspace_root
  [ "$status" -eq 0 ]
  [ "$output" = "$ws" ]
}

# ─── require_workspace_root ───────────────────────────────────────────────────

@test "require_workspace_root exits 1 with error when no toml found" {
  local isolated
  isolated=$(mktemp -d)
  cd "$isolated"
  run require_workspace_root
  [ "$status" -eq 1 ]
  echo "$stderr" | grep -q "pn-workspace.toml" || echo "$output" | grep -q "pn-workspace.toml"
  rmdir "$isolated" 2>/dev/null || true
}

@test "require_workspace_root error message mentions pn-workspace-init" {
  local isolated
  isolated=$(mktemp -d)
  cd "$isolated"
  run --separate-stderr require_workspace_root
  [ "$status" -eq 1 ]
  echo "$stderr" | grep -q "pn-workspace-init"
  rmdir "$isolated" 2>/dev/null || true
}

@test "require_workspace_root outputs root path when toml found" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"
  touch "$ws/pn-workspace.toml"
  cd "$ws"
  run require_workspace_root
  [ "$status" -eq 0 ]
  [ "$output" = "$ws" ]
}

@test "require_workspace_root finds root from subdirectory" {
  local ws="$TEST_DIR/workspace"
  local subdir="$ws/a/b/c"
  mkdir -p "$subdir"
  touch "$ws/pn-workspace.toml"
  cd "$subdir"
  run require_workspace_root
  [ "$status" -eq 0 ]
  [ "$output" = "$ws" ]
}

# ─── workspace_resolve_root ────────────────────────────────────────────────────

@test "workspace_resolve_root uses flag value when given" {
  mkdir -p "$TEST_DIR/ws"
  touch "$TEST_DIR/ws/pn-workspace.toml"
  run workspace_resolve_root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/ws" ]
}

@test "workspace_resolve_root uses PN_WORKSPACE_ROOT env when no flag" {
  mkdir -p "$TEST_DIR/ws"
  touch "$TEST_DIR/ws/pn-workspace.toml"
  PN_WORKSPACE_ROOT="$TEST_DIR/ws" run workspace_resolve_root ""
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/ws" ]
}

@test "workspace_resolve_root prefers flag over env" {
  mkdir -p "$TEST_DIR/ws-flag"
  mkdir -p "$TEST_DIR/ws-env"
  touch "$TEST_DIR/ws-flag/pn-workspace.toml"
  touch "$TEST_DIR/ws-env/pn-workspace.toml"
  PN_WORKSPACE_ROOT="$TEST_DIR/ws-env" run workspace_resolve_root "$TEST_DIR/ws-flag"
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/ws-flag" ]
}

@test "workspace_resolve_root falls back to walk-up when no flag and no env" {
  mkdir -p "$TEST_DIR/ws/sub"
  touch "$TEST_DIR/ws/pn-workspace.toml"
  cd "$TEST_DIR/ws/sub"
  unset PN_WORKSPACE_ROOT
  run workspace_resolve_root ""
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/ws" ]
}

@test "workspace_resolve_root errors when flag dir does not exist" {
  run --separate-stderr workspace_resolve_root "$TEST_DIR/nonexistent"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "workspace directory not found"
}

@test "workspace_resolve_root errors when env dir does not exist" {
  PN_WORKSPACE_ROOT="$TEST_DIR/nonexistent" run --separate-stderr workspace_resolve_root ""
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "PN_WORKSPACE_ROOT"
}

@test "workspace_resolve_root returns non-zero on walk-up failure (does not exit)" {
  cd "$TEST_DIR"
  unset PN_WORKSPACE_ROOT
  _wrap() {
    workspace_resolve_root "" 2>/dev/null || true
    echo "MARKER"
  }
  run _wrap
  unset -f _wrap
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "MARKER"
}

# ─── workspace_get_projects ───────────────────────────────────────────────────

@test "workspace_get_projects reads lock file when use_lock=true and lock exists" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/repo-a" "$ws/repo-b"
  touch "$ws/repo-a/flake.nix" "$ws/repo-b/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[
  {"path": "repo-a", "inputName": "repo-a-input"},
  {"path": "repo-b"}
]
EOF
  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  # Paths should be absolute (prefixed with workspace root)
  echo "$output" | grep -q "\"$ws/repo-a\""
  echo "$output" | grep -q "\"$ws/repo-b\""
}

@test "workspace_get_projects converts relative paths to absolute in lock file" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/my-repo"
  touch "$ws/my-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[
  {"path": "my-repo", "inputName": "my-input"}
]
EOF
  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  # Path must be absolute
  echo "$output" | grep -q "\"path\": \"$ws/my-repo\""
}

@test "workspace_get_projects passes through absolute paths in lock file unchanged" {
  local ws="$TEST_DIR/workspace"
  local other="$TEST_DIR/other-repo"
  mkdir -p "$ws" "$other"
  touch "$other/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<EOF
[
  {"path": "$other", "inputName": "other-input"}
]
EOF
  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  # Absolute path must not be double-prefixed
  echo "$output" | jq -e '.[0].path == "'"$other"'"'
}

@test "workspace_get_projects calls pn-discover-workspace when use_lock=false" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws" "$TEST_DIR/mock-discovered"
  touch "$TEST_DIR/mock-discovered/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = false
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "should-not-be-used"}]
EOF

  # Create a mock pn-discover-workspace returning absolute path
  cat > "$TEST_DIR/pn-discover-workspace" <<MOCK
#!/usr/bin/env bash
echo '[{"path": "$TEST_DIR/mock-discovered", "inputName": "mock-input"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "mock-discovered"
  echo "$output" | grep -qv "should-not-be-used"
}

@test "workspace_get_projects calls pn-discover-workspace when lock file absent" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws" "$TEST_DIR/mock-repo"
  touch "$TEST_DIR/mock-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  # No lock file created

  # Create a mock pn-discover-workspace returning absolute path
  cat > "$TEST_DIR/pn-discover-workspace" <<MOCK
#!/usr/bin/env bash
echo '[{"path": "$TEST_DIR/mock-repo", "inputName": "mock-repo"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "mock-repo"
}

@test "workspace_get_projects converts relative paths from pn-discover-workspace to absolute" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/repo-base" "$ws/terminal-flake"
  touch "$ws/repo-base/flake.nix" "$ws/terminal-flake/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  # No lock file — forces pn-discover-workspace to be called

  # Mock returns RELATIVE paths (matching real pn-discover-workspace behavior)
  cat > "$TEST_DIR/pn-discover-workspace" <<'MOCK'
#!/usr/bin/env bash
echo '[{"path": "repo-base", "inputName": "repo-base-input"}, {"path": "terminal-flake"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  # Relative paths must be converted to absolute (prefixed with workspace root)
  echo "$output" | grep -q "\"$ws/repo-base\""
  echo "$output" | grep -q "\"$ws/terminal-flake\""
  # Must not contain bare relative paths
  ! (echo "$output" | grep -q '"path": "repo-base"') || false
}

@test "workspace_get_projects defaults to use_lock=true when key absent from toml" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/locked-repo"
  touch "$ws/locked-repo/flake.nix"
  # toml with no use_lock key
  cat > "$ws/pn-workspace.toml" <<'EOF'
[workspace]
name = "test"
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "locked-repo"}]
EOF

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "locked-repo"
}

@test "workspace_get_projects regenerates lockfile when use_lock=true and lock absent" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws" "$TEST_DIR/discovered-repo"
  touch "$TEST_DIR/discovered-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  # No lockfile initially

  cat > "$TEST_DIR/pn-discover-workspace" <<MOCK
#!/usr/bin/env bash
echo '[{"path": "$TEST_DIR/discovered-repo", "inputName": "discovered"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  [ -f "$ws/pn-workspace.lock" ]
  echo "$output" | grep -q "generated pn-workspace.lock"
}

@test "workspace_get_projects does not regenerate lockfile when use_lock=false" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws" "$TEST_DIR/discovered-repo"
  touch "$TEST_DIR/discovered-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = false
EOF
  # No lockfile initially

  cat > "$TEST_DIR/pn-discover-workspace" <<MOCK
#!/usr/bin/env bash
echo '[{"path": "$TEST_DIR/discovered-repo", "inputName": "discovered"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  [ ! -f "$ws/pn-workspace.lock" ]
}

@test "workspace_get_projects warns when lockfile exists but use_lock=false" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws" "$TEST_DIR/discovered-repo"
  touch "$TEST_DIR/discovered-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = false
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "should-not-be-used"}]
EOF

  cat > "$TEST_DIR/pn-discover-workspace" <<MOCK
#!/usr/bin/env bash
echo '[{"path": "$TEST_DIR/discovered-repo", "inputName": "discovered"}]'
MOCK
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"

  run workspace_get_projects "$ws"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "lockfile is being ignored"
}

@test "workspace_get_projects errors when project path does not exist" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "nonexistent-repo", "inputName": "some-input"}]
EOF
  run --separate-stderr workspace_get_projects "$ws"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "does not exist"
}

@test "workspace_get_projects errors when project path lacks flake.nix" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/no-flake-repo"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "no-flake-repo", "inputName": "some-input"}]
EOF
  run --separate-stderr workspace_get_projects "$ws"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "is not a flake"
}

@test "workspace_get_projects errors when project has empty inputName" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/my-repo"
  touch "$ws/my-repo/flake.nix"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = true
EOF
  cat > "$ws/pn-workspace.lock" <<'EOF'
[{"path": "my-repo", "inputName": ""}]
EOF
  run --separate-stderr workspace_get_projects "$ws"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "inputName is empty"
}

# ─── workspace_read_toml ─────────────────────────────────────────────────────

@test "workspace_read_toml reads a simple string key" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"
  cat > "$ws/pn-workspace.toml" <<'EOF'
name = "my-workspace"
EOF
  run workspace_read_toml "$ws" "name"
  [ "$status" -eq 0 ]
  [ "$output" = "my-workspace" ]
}

@test "workspace_read_toml reads a boolean key" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"
  cat > "$ws/pn-workspace.toml" <<'EOF'
use_lock = false
EOF
  run workspace_read_toml "$ws" "use_lock"
  [ "$status" -eq 0 ]
  [ "$output" = "false" ]
}

# ─── workspace_parse_overrides ────────────────────────────────────────────────

@test "workspace_parse_overrides returns empty object when no flags and no env" {
  unset PN_WORKSPACE_OVERRIDE_PATHS
  run workspace_parse_overrides
  [ "$status" -eq 0 ]
  [ "$output" = "{}" ]
}

@test "workspace_parse_overrides parses single env entry" {
  PN_WORKSPACE_OVERRIDE_PATHS="repo-base=/tmp/foo" run workspace_parse_overrides
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '."repo-base" == "/tmp/foo"'
}

@test "workspace_parse_overrides parses multiple env entries" {
  PN_WORKSPACE_OVERRIDE_PATHS="a=/x,b=/y" run workspace_parse_overrides
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.a == "/x" and .b == "/y"'
}

@test "workspace_parse_overrides tolerates whitespace and trailing commas" {
  PN_WORKSPACE_OVERRIDE_PATHS=" a = /x , b = /y , " run workspace_parse_overrides
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.a == "/x" and .b == "/y"'
}

@test "workspace_parse_overrides parses single flag entry" {
  unset PN_WORKSPACE_OVERRIDE_PATHS
  run workspace_parse_overrides "repo-base=/tmp/foo"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '."repo-base" == "/tmp/foo"'
}

@test "workspace_parse_overrides parses multiple flag entries" {
  unset PN_WORKSPACE_OVERRIDE_PATHS
  run workspace_parse_overrides "a=/x" "b=/y"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.a == "/x" and .b == "/y"'
}

@test "workspace_parse_overrides flag wins over env for same key" {
  PN_WORKSPACE_OVERRIDE_PATHS="a=/env" run workspace_parse_overrides "a=/flag"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.a == "/flag"'
}

@test "workspace_parse_overrides duplicate flag keys: last wins" {
  unset PN_WORKSPACE_OVERRIDE_PATHS
  run workspace_parse_overrides "a=/first" "a=/second"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.a == "/second"'
}

@test "workspace_parse_overrides does not glob-expand env values" {
  cd "$TEST_DIR"
  mkdir -p decoy
  touch decoy/a decoy/b
  PN_WORKSPACE_OVERRIDE_PATHS="x=/tmp/decoy/*" run workspace_parse_overrides
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.x == "/tmp/decoy/*"'
}

@test "workspace_parse_overrides errors on missing equals" {
  run --separate-stderr workspace_parse_overrides "no-equals"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "expected name=path"
}

@test "workspace_parse_overrides errors on empty name" {
  run --separate-stderr workspace_parse_overrides "=/path"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "empty name"
}

@test "workspace_parse_overrides errors on relative path" {
  run --separate-stderr workspace_parse_overrides "a=relative/path"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "must be absolute"
}

# ─── workspace_get_projects with overrides ────────────────────────────────────

# Test fixture: build a fake lockfile workspace
_setup_lockfile_workspace() {
  mkdir -p "$TEST_DIR/ws/repo-a" "$TEST_DIR/ws/repo-b"
  touch "$TEST_DIR/ws/repo-a/flake.nix" "$TEST_DIR/ws/repo-b/flake.nix"
  cat > "$TEST_DIR/ws/pn-workspace.toml" <<'TOML'
use_lock = true
TOML
  cat > "$TEST_DIR/ws/pn-workspace.lock" <<'JSON'
[
  {"path": "repo-a", "inputName": "input-a"},
  {"path": "repo-b"}
]
JSON
}

@test "workspace_get_projects without overrides returns absolute paths" {
  _setup_lockfile_workspace
  run workspace_get_projects "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.[0].path == "'"$TEST_DIR/ws/repo-a"'"'
  echo "$output" | jq -e '.[1].path == "'"$TEST_DIR/ws/repo-b"'"'
}

@test "workspace_get_projects with empty overrides leaves paths unchanged" {
  _setup_lockfile_workspace
  run workspace_get_projects "$TEST_DIR/ws" "{}"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.[0].path == "'"$TEST_DIR/ws/repo-a"'"'
}

@test "workspace_get_projects swaps path for matching override (non-terminal)" {
  _setup_lockfile_workspace
  mkdir -p "$TEST_DIR/wt-a"
  touch "$TEST_DIR/wt-a/flake.nix"
  local overrides
  overrides=$(jq -n --arg p "$TEST_DIR/wt-a" '{"repo-a": $p}')
  run workspace_get_projects "$TEST_DIR/ws" "$overrides"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.[0].path == "'"$TEST_DIR/wt-a"'"'
  echo "$output" | jq -e '.[0].inputName == "input-a"'
  echo "$output" | jq -e '.[1].path == "'"$TEST_DIR/ws/repo-b"'"'
}

@test "workspace_get_projects swaps path for terminal entry too" {
  _setup_lockfile_workspace
  mkdir -p "$TEST_DIR/wt-b"
  touch "$TEST_DIR/wt-b/flake.nix"
  local overrides
  overrides=$(jq -n --arg p "$TEST_DIR/wt-b" '{"repo-b": $p}')
  run workspace_get_projects "$TEST_DIR/ws" "$overrides"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.[1].path == "'"$TEST_DIR/wt-b"'"'
  echo "$output" | jq -e '.[1].inputName == null'
}

@test "workspace_get_projects errors on unknown override key" {
  _setup_lockfile_workspace
  local overrides='{"bogus": "/tmp"}'
  run --separate-stderr workspace_get_projects "$TEST_DIR/ws" "$overrides"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q 'unknown project "bogus"'
  echo "$stderr" | grep -q "valid projects"
}

@test "workspace_get_projects errors when override path does not exist" {
  _setup_lockfile_workspace
  local overrides='{"repo-a": "/nonexistent/path"}'
  run --separate-stderr workspace_get_projects "$TEST_DIR/ws" "$overrides"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "does not exist"
}

@test "workspace_get_projects errors when override path lacks flake.nix" {
  _setup_lockfile_workspace
  mkdir -p "$TEST_DIR/no-flake"
  local overrides
  overrides=$(jq -n --arg p "$TEST_DIR/no-flake" '{"repo-a": $p}')
  run --separate-stderr workspace_get_projects "$TEST_DIR/ws" "$overrides"
  [ "$status" -ne 0 ]
  echo "$stderr" | grep -q "is not a flake"
}

# ─── workspace_has_upstream ───────────────────────────────────────────────────

@test "workspace_has_upstream returns 1 when no remotes configured" {
  local repo="$TEST_DIR/repo-no-remote"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}

@test "workspace_has_upstream returns 1 when remote exists but no tracking branch" {
  local repo="$TEST_DIR/repo-no-upstream"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  git -C "$repo" remote add origin /nonexistent
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}

@test "workspace_has_upstream returns 0 when remote and tracking branch both present" {
  local upstream="$TEST_DIR/repo-upstream.git"
  local repo="$TEST_DIR/repo-tracked"
  git init -q --bare "$upstream"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  git -C "$repo" remote add origin "$upstream"
  git -C "$repo" push -q -u origin main
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 0 ]
}

@test "workspace_has_upstream returns 1 in detached HEAD state" {
  local repo="$TEST_DIR/repo-detached"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m c1
  local sha
  sha=$(git -C "$repo" rev-parse HEAD)
  git -C "$repo" checkout -q "$sha"
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}
