# Test helper functions for pn module tests
# Provides test isolation, command mocking, and fixture utilities

# ─── Test isolation ───────────────────────────────────────────────────────────

# Override HOME to prevent touching real system
# Sets TEST_HOME="$TEST_DIR/home", exports HOME="$TEST_HOME", creates the dir
setup_test_home() {
  export TEST_HOME="$TEST_DIR/home"
  export HOME="$TEST_HOME"
  mkdir -p "$TEST_HOME"
}

# Verify no real paths were touched (HOME was mocked)
assert_no_real_paths_touched() {
  local real_home="${REAL_HOME:-/Users}"

  # HOME must not point at the real home tree
  [[ $HOME != "$real_home"* ]] || return 1

  return 0
}

# ─── Mock commands ────────────────────────────────────────────────────────────

# Create mock nix command
create_mock_nix() {
  cat >"$TEST_DIR/nix" <<'EOF'
#!/usr/bin/env bash
echo "Mock: nix $*"
exit 0
EOF
  chmod +x "$TEST_DIR/nix"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock git command.
# Handles `git remote get-url origin` specially — returns $MOCK_GIT_REMOTE_URL
# (default: https://github.com/example/repo.git).  All other invocations echo
# "Mock: git <args>" and exit 0.
create_mock_git() {
  cat >"$TEST_DIR/git" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "remote" && "$2" == "get-url" && "$3" == "origin" ]]; then
  echo "${MOCK_GIT_REMOTE_URL:-https://github.com/example/repo.git}"
  exit 0
fi
echo "Mock: git $*"
exit 0
EOF
  chmod +x "$TEST_DIR/git"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock darwin-rebuild command
create_mock_darwin_rebuild() {
  cat >"$TEST_DIR/darwin-rebuild" <<'EOF'
#!/usr/bin/env bash
echo "Mock: darwin-rebuild $*"
exit 0
EOF
  chmod +x "$TEST_DIR/darwin-rebuild"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock sudo command - passes through to the actual command
create_mock_sudo() {
  cat >"$TEST_DIR/sudo" <<'EOF'
#!/usr/bin/env bash
exec "$@"
EOF
  chmod +x "$TEST_DIR/sudo"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock mas command
create_mock_mas() {
  cat >"$TEST_DIR/mas" <<'EOF'
#!/usr/bin/env bash
echo "Mock: mas $*"
exit 0
EOF
  chmod +x "$TEST_DIR/mas"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock yq command.
# Behaviour is configurable via MOCK_YQ_OUTPUT (default: empty string).
create_mock_yq() {
  cat >"$TEST_DIR/yq" <<'EOF'
#!/usr/bin/env bash
if [[ -n "${MOCK_YQ_OUTPUT:-}" ]]; then
  echo "$MOCK_YQ_OUTPUT"
else
  echo "Mock: yq $*"
fi
exit 0
EOF
  chmod +x "$TEST_DIR/yq"
  export PATH="$TEST_DIR:$PATH"
}

# Create mock pn-discover-workspace command.
# Outputs a JSON array configurable via PN_DISCOVER_OUTPUT env var.
# Default output contains two workspace repos.
create_mock_pn_discover_workspace() {
  cat >"$TEST_DIR/pn-discover-workspace" <<EOF
#!/usr/bin/env bash
if [[ -n "\${PN_DISCOVER_OUTPUT:-}" ]]; then
  echo "\$PN_DISCOVER_OUTPUT"
else
  echo '[{"path":"$TEST_DIR/workspace/repo-base"},{"path":"$TEST_DIR/workspace/terminal-flake"}]'
fi
exit 0
EOF
  chmod +x "$TEST_DIR/pn-discover-workspace"
  export PATH="$TEST_DIR:$PATH"
}

# ─── Workspace fixtures ───────────────────────────────────────────────────────

# Create a minimal workspace under $TEST_DIR/workspace/ with pn-workspace.toml.
# Also creates stub flake.nix files for each declared project repo.
setup_workspace() {
  mkdir -p "$TEST_DIR/workspace/terminal-flake"
  mkdir -p "$TEST_DIR/workspace/repo-base"

  cat >"$TEST_DIR/workspace/pn-workspace.toml" <<'TOML'
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
pre_apply_hooks = []
post_apply_hooks = []
use_lock = false
TOML

  touch "$TEST_DIR/workspace/terminal-flake/flake.nix"
  touch "$TEST_DIR/workspace/repo-base/flake.nix"
}
