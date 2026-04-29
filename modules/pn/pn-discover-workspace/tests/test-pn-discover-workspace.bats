#!/usr/bin/env bats

# Tests for pn-discover-workspace script

# Resolve scripts directory
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR
  export REAL_HOME="$HOME"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"

  # Add TEST_DIR to PATH for mocks
  export PATH="$TEST_DIR/bin:$PATH"
  mkdir -p "$TEST_DIR/bin"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# ─── Mock helpers ─────────────────────────────────────────────────────────────

# Create a mock git that returns configured remote URLs and real worktree list.
# Usage: setup_mock_git <repo_path>:<remote_url> [...]
# Also supports: git init, git remote add (for setup)
_setup_mock_git() {
  # Write remote URL map entries to a file
  local map_file="$TEST_DIR/git-remote-map"
  : > "$map_file"
  for entry in "$@"; do
    echo "$entry" >> "$map_file"
  done

  cat > "$TEST_DIR/bin/git" <<'MOCK'
#!/usr/bin/env bash
# Mock git for pn-discover-workspace tests

MAP_FILE="$TEST_DIR/git-remote-map"

if [[ "$1" == "-C" ]]; then
  repo="$2"
  shift 2
fi

if [[ "$1" == "remote" && "$2" == "get-url" && "$3" == "origin" ]]; then
  # Look up repo in map file
  while IFS=: read -r map_repo map_url; do
    if [[ "$map_repo" == "$repo" ]]; then
      echo "$map_url"
      exit 0
    fi
  done < "$MAP_FILE"
  exit 1
fi

# Fall through to real git for other commands
exec command git "$@"
MOCK
  chmod +x "$TEST_DIR/bin/git"
}

# Create a mock nix that returns configured inputs JSON for flake paths.
# Usage: setup_mock_nix <flake_dir>:<inputs_json> [...]
_setup_mock_nix() {
  local map_file="$TEST_DIR/nix-inputs-map"
  : > "$map_file"
  for entry in "$@"; do
    # entry format: dir|json (using | as delimiter to avoid json issues)
    echo "$entry" >> "$map_file"
  done

  cat > "$TEST_DIR/bin/nix" <<'MOCK'
#!/usr/bin/env bash
# Mock nix for pn-discover-workspace tests

MAP_FILE="$TEST_DIR/nix-inputs-map"

# nix eval --json --file <dir>/flake.nix "inputs"
if [[ "$1" == "eval" && "$2" == "--json" && "$3" == "--file" ]]; then
  flake_file="$4"
  flake_dir="$(dirname "$flake_file")"
  # Look up in map file
  while IFS='|' read -r map_dir map_json; do
    if [[ "$map_dir" == "$flake_dir" ]]; then
      echo "$map_json"
      exit 0
    fi
  done < "$MAP_FILE"
  # Not found: return empty object
  echo "{}"
  exit 0
fi

echo "Mock nix: unhandled args: $*" >&2
exit 1
MOCK
  chmod +x "$TEST_DIR/bin/nix"
}

# Create a fake flake repo at path with a flake.nix placeholder
_create_flake_repo() {
  local dir="$1"
  mkdir -p "$dir"
  touch "$dir/flake.nix"
  git -C "$dir" init --quiet 2>/dev/null || true
}

# ─── Help and argument tests ──────────────────────────────────────────────────

@test "--help flag shows usage information" {
  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" --help
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "usage"
}

@test "-h flag shows usage information" {
  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" -h
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "usage"
}

@test "fails with no arguments" {
  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh"
  [ "$status" -ne 0 ]
}

# ─── Basic discovery ──────────────────────────────────────────────────────────

@test "discovers empty workspace and returns empty JSON array" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws"

  _setup_mock_nix  # no entries needed

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "skips repos without flake.nix" {
  local ws="$TEST_DIR/workspace"
  mkdir -p "$ws/no-flake-repo"
  # no flake.nix created

  _setup_mock_git "$ws/no-flake-repo:git@github.com:owner/no-flake.git"
  _setup_mock_nix

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]
  # Valid JSON empty array (no repos found)
  echo "$output" | jq -e '. == []'
}

@test "skips repos without git remote gracefully" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/no-remote"
  # no git remote configured — mock git returns exit 1 for this path

  # Mock git that always fails remote get-url
  cat > "$TEST_DIR/bin/git" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "-C" ]]; then
  shift 2
fi
if [[ "$1" == "remote" && "$2" == "get-url" ]]; then
  exit 1
fi
exec command git "$@"
MOCK
  chmod +x "$TEST_DIR/bin/git"

  _setup_mock_nix

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '. == []'
}

@test "discovers single repo and returns it as JSON array" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-a"

  _setup_mock_git "$ws/repo-a:git@github.com:owner/repo-a.git"
  _setup_mock_nix "$ws/repo-a|{}"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]
  # Valid JSON array with one entry
  local count
  count=$(echo "$output" | jq 'length')
  [ "$count" -eq 1 ]
  # Path should be the repo name (relative to workspace)
  echo "$output" | jq -e '.[0].path == "repo-a"'
}

@test "discovers two repos with dependency order: base first, terminal last" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-base"
  _create_flake_repo "$ws/repo-app"

  # repo-app depends on repo-base via flake input
  local base_inputs='{}'
  local app_inputs='{"base": {"url": "github:owner/repo-base", "flake": true}}'

  _setup_mock_git \
    "$ws/repo-base:git@github.com:owner/repo-base.git" \
    "$ws/repo-app:git@github.com:owner/repo-app.git"

  _setup_mock_nix \
    "$ws/repo-base|$base_inputs" \
    "$ws/repo-app|$app_inputs"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]

  local count
  count=$(echo "$output" | jq 'length')
  [ "$count" -eq 2 ]

  # repo-base should appear before repo-app
  local base_idx app_idx
  base_idx=$(echo "$output" | jq '[.[].path] | index("repo-base")')
  app_idx=$(echo "$output" | jq '[.[].path] | index("repo-app")')
  [ "$base_idx" -lt "$app_idx" ]
}

@test "terminal repo has no inputName field" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-base"
  _create_flake_repo "$ws/repo-app"

  local base_inputs='{}'
  local app_inputs='{"base-input": {"url": "github:owner/repo-base", "flake": true}}'

  _setup_mock_git \
    "$ws/repo-base:git@github.com:owner/repo-base.git" \
    "$ws/repo-app:git@github.com:owner/repo-app.git"

  _setup_mock_nix \
    "$ws/repo-base|$base_inputs" \
    "$ws/repo-app|$app_inputs"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]

  # Terminal (repo-app) should have no inputName
  local terminal_entry
  terminal_entry=$(echo "$output" | jq '.[-1]')
  echo "$terminal_entry" | jq -e 'has("inputName") | not'
}

@test "non-terminal repo has inputName matching terminal flake's input key" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-base"
  _create_flake_repo "$ws/repo-app"

  local base_inputs='{}'
  local app_inputs='{"my-base-input": {"url": "github:owner/repo-base", "flake": true}}'

  _setup_mock_git \
    "$ws/repo-base:git@github.com:owner/repo-base.git" \
    "$ws/repo-app:git@github.com:owner/repo-app.git"

  _setup_mock_nix \
    "$ws/repo-base|$base_inputs" \
    "$ws/repo-app|$app_inputs"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]

  # repo-base should have inputName = "my-base-input"
  local base_entry
  base_entry=$(echo "$output" | jq '.[] | select(.path == "repo-base")')
  echo "$base_entry" | jq -e '.inputName == "my-base-input"'
}

@test "output is valid JSON" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-a"

  _setup_mock_git "$ws/repo-a:https://github.com/owner/repo-a.git"
  _setup_mock_nix "$ws/repo-a|{}"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]
  # jq parse check
  echo "$output" | jq . > /dev/null
}

@test "three-repo chain produces correct topo order" {
  local ws="$TEST_DIR/workspace"
  _create_flake_repo "$ws/repo-a"
  _create_flake_repo "$ws/repo-b"
  _create_flake_repo "$ws/repo-c"

  # c depends on b, b depends on a
  local a_inputs='{}'
  local b_inputs='{"a-input": {"url": "github:owner/repo-a", "flake": true}}'
  local c_inputs='{"b-input": {"url": "github:owner/repo-b", "flake": true}}'

  _setup_mock_git \
    "$ws/repo-a:github:owner/repo-a" \
    "$ws/repo-b:github:owner/repo-b" \
    "$ws/repo-c:github:owner/repo-c"

  _setup_mock_nix \
    "$ws/repo-a|$a_inputs" \
    "$ws/repo-b|$b_inputs" \
    "$ws/repo-c|$c_inputs"

  run bash "$SCRIPTS_DIR/pn-discover-workspace.sh" "$ws"
  [ "$status" -eq 0 ]

  local count
  count=$(echo "$output" | jq 'length')
  [ "$count" -eq 3 ]

  local a_idx b_idx c_idx
  a_idx=$(echo "$output" | jq '[.[].path] | index("repo-a")')
  b_idx=$(echo "$output" | jq '[.[].path] | index("repo-b")')
  c_idx=$(echo "$output" | jq '[.[].path] | index("repo-c")')

  # a before b, b before c
  [ "$a_idx" -lt "$b_idx" ]
  [ "$b_idx" -lt "$c_idx" ]
}
