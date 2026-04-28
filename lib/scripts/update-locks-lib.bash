# shellcheck shell=bash
# Shared library for update-locks.sh scripts.
# Provides isolated step execution with per-step commits and rollback.
# Sources update-cache-lib.bash for caching support.

_UL_LOCKS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

_UL_STEPS_RAN=0
_UL_STEPS_SUCCEEDED=0
_UL_STEPS_FAILED=0
_UL_STEPS_SKIPPED=0
_UL_FAILED_STEPS=()
_UL_SCRIPT_DIR=""
_UL_CHILD_PID=""
_UL_CAUGHT_SIGNAL=""

_ul_cleanup() {
  local signal="${1:-EXIT}"
  _UL_CAUGHT_SIGNAL="$signal"

  # Kill running child if any
  if [[ -n $_UL_CHILD_PID ]] && kill -0 "$_UL_CHILD_PID" 2>/dev/null; then
    kill -TERM "$_UL_CHILD_PID" 2>/dev/null
    wait "$_UL_CHILD_PID" 2>/dev/null || true
  fi
  _UL_CHILD_PID=""

  # Clean dirty git state
  if [[ -n $_UL_SCRIPT_DIR ]] && [[ -d "$_UL_SCRIPT_DIR/.git" ]]; then
    cd "$_UL_SCRIPT_DIR" 2>/dev/null || true
    if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
      git reset --hard HEAD 2>/dev/null || true
      git clean -fd 2>/dev/null || true
    fi
  fi

  # Restore fsmonitor
  if [[ ${_fsmonitor_was_active:-false} == "true" ]]; then
    git config core.fsmonitor true 2>/dev/null || true
  fi

  # Exit with 128+signum so parent sees signal-like exit status
  if [[ $signal != "EXIT" ]]; then
    trap - "$signal" EXIT
    exit $((128 + $(kill -l "$signal")))
  fi
}

_ul_ensure_pre_commit_hooks() {
  # Tier 1: does the flake declare install-pre-commit-hooks?
  # --no-link avoids polluting the project dir; if the attribute doesn't exist,
  # nix prints an error and we silently skip via || return 0.
  local drv_path
  drv_path=$(nix build .#install-pre-commit-hooks --no-link --print-out-paths) || return 0

  # Tier 2: is the hook binary still valid (not GC'd)?
  local hooks_dir hook_file exec_target needs_install
  hooks_dir=$(git config --get core.hooksPath 2>/dev/null || echo ".git/hooks")
  hook_file="${_UL_SCRIPT_DIR}/${hooks_dir}/pre-commit"
  needs_install=false

  if [[ -f $hook_file ]]; then
    exec_target=$(grep '^exec ' "$hook_file" | sed 's/^exec \([^ ]*\).*/\1/')
    if [[ -n $exec_target && ! -x $exec_target ]]; then
      echo "==> pre-commit hook binary missing (GC'd), reinstalling..."
      needs_install=true
    fi
  else
    echo "==> pre-commit hook not found, installing..."
    needs_install=true
  fi

  # Tier 3: has the derivation changed since last install?
  if [[ $needs_install != "true" ]]; then
    local marker="$UL_STATE_DIR/$_UL_PROJECT/pre-commit-drv-path"
    if [[ -f $marker ]] && [[ "$(cat "$marker")" == "$drv_path" ]]; then
      return 0
    fi
    echo "==> pre-commit hooks config changed, reinstalling..."
    needs_install=true
  fi

  nix run .#install-pre-commit-hooks
  if ! git diff --quiet -- .pre-commit-config.yaml 2>/dev/null; then
    git add .pre-commit-config.yaml
    git commit -m "update-locks: install pre-commit hooks"
  fi
  mkdir -p "$UL_STATE_DIR/$_UL_PROJECT"
  echo "$drv_path" >"$UL_STATE_DIR/$_UL_PROJECT/pre-commit-drv-path"
}

ul_setup() {
  local project_name="$1"
  local script_dir="$2"
  _UL_SCRIPT_DIR="$script_dir"

  # shellcheck disable=SC1091
  source "${_UL_LOCKS_LIB_DIR}/update-cache-lib.bash"
  ul_init "$project_name"

  cd "$script_dir"

  if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "ERROR: Working directory is not clean. Commit or stash changes first."
    git status --short
    exit 1
  fi

  _fsmonitor_was_active="$(git config core.fsmonitor 2>/dev/null || echo false)"
  if [ "$_fsmonitor_was_active" = "true" ]; then
    git config core.fsmonitor false
    git fsmonitor--daemon stop 2>/dev/null || true
  fi
  trap '_ul_cleanup EXIT' EXIT
  trap '_ul_cleanup INT' INT
  trap '_ul_cleanup TERM' TERM

  _UL_STEPS_RAN=0
  _UL_STEPS_SUCCEEDED=0
  _UL_STEPS_FAILED=0
  _UL_STEPS_SKIPPED=0
  _UL_FAILED_STEPS=()

  ul_check_nix_daemon

  _ul_ensure_pre_commit_hooks
}

ul_run_step() {
  local step_name="$1"
  local commit_msg="$2"
  shift 2

  if [[ $# -eq 0 ]]; then
    echo "FATAL: ul_run_step '${step_name}' requires a command"
    exit 1
  fi

  if ! ul_should_run "$step_name"; then
    _UL_STEPS_SKIPPED=$((_UL_STEPS_SKIPPED + 1))
    return 0
  fi

  if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "FATAL: workspace dirty before step '${step_name}'. Stopping."
    git status --short
    exit 1
  fi

  echo "==> ${step_name}..."
  _UL_STEPS_RAN=$((_UL_STEPS_RAN + 1))

  local rc=0
  local _ul_restore_e
  if [[ -o errexit ]]; then _ul_restore_e="set -e"; else _ul_restore_e="set +e"; fi
  set +e
  (
    set -e
    "$@"
  ) &
  _UL_CHILD_PID=$!
  wait "$_UL_CHILD_PID"
  rc=$?
  _UL_CHILD_PID=""
  $_ul_restore_e

  if [[ $rc -eq 0 ]]; then
    if ! git diff --quiet || ! git diff --cached --quiet; then
      if ! nix fmt; then
        echo "  ✗ Step '${step_name}' nix fmt failed"
        git reset --hard HEAD 2>/dev/null || true
        git clean -fd 2>/dev/null || true
        _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
        _UL_FAILED_STEPS+=("$step_name")
        return 0
      fi
      if ! git add -A || ! git commit -m "$commit_msg"; then
        echo "  ✗ Step '${step_name}' commit failed"
        git reset --hard HEAD 2>/dev/null || true
        git clean -fd 2>/dev/null || true
        _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
        _UL_FAILED_STEPS+=("$step_name")
        return 0
      fi
    fi
    ul_mark_done "$step_name"
    _UL_STEPS_SUCCEEDED=$((_UL_STEPS_SUCCEEDED + 1))
  else
    echo "  ✗ Step '${step_name}' failed (exit code ${rc})"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
  fi
}

ul_finalize() {
  echo ""
  echo "=== Update Summary ==="
  echo "  Ran:     ${_UL_STEPS_RAN}"
  echo "  Passed:  ${_UL_STEPS_SUCCEEDED}"
  echo "  Failed:  ${_UL_STEPS_FAILED}"
  echo "  Skipped: ${_UL_STEPS_SKIPPED}"

  if [[ ${_UL_STEPS_FAILED} -gt 0 ]]; then
    echo ""
    echo "Failed steps:"
    for step in "${_UL_FAILED_STEPS[@]}"; do
      echo "  ✗ ${step}"
    done
    exit 1
  fi

  echo ""
  echo "✓ All steps completed successfully!"
  exit 0
}
