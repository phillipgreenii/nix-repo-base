# shellcheck shell=bash
# Shared library for update-locks.sh scripts.
# Provides isolated step execution with per-step commits and rollback.
# Sources update-cache-lib.bash for caching support.

_UL_LOCKS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Exit code a step returns to mean "valid attempt, no update applied" — roll
# back content but record the timestamp (so it isn't retried until the window
# expires) and keep the run passing. 75 = EX_TEMPFAIL: clear of generic 1/2 and
# of Nix's 100/101, so a real tool failure is never misread as a deferral.
UL_RC_ATTEMPTED=75
ul_attempted() { exit "$UL_RC_ATTEMPTED"; }

_UL_STEPS_RAN=0
_UL_STEPS_SUCCEEDED=0
_UL_STEPS_FAILED=0
_UL_STEPS_SKIPPED=0
_UL_STEPS_DEFERRED=0
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

# Re-exec the calling script inside its flake's devShells.default if possible.
# Safe to call from any update-locks.sh as the first thing after sourcing this lib.
# Behavior:
#   - If IN_NIX_SHELL is already set, prints a notice and returns 0 (no re-exec).
#   - Otherwise makes a SINGLE `nix develop ... --command bash` entry. A sentinel
#     file distinguishes the outcomes once nix returns:
#       * sentinel still present -> the dev shell never started (e.g. broken
#         flake); prints a warning and returns 0 so the script can still run
#         with host tooling (and the user can fix the flake).
#       * sentinel gone -> the script ran inside the shell; exits with its status.
#   - Exports UL_LIB_DIR (when set) so the in-shell re-run reuses it instead of
#     resolving determine-ul-lib-dir a second time.
ul_reexec_in_dev_shell() {
  local script="$0"
  local script_dir
  script_dir="$(cd "$(dirname "$script")" && pwd)"

  if [[ -n ${IN_NIX_SHELL:-} ]]; then
    echo "==> already in nix shell (IN_NIX_SHELL=$IN_NIX_SHELL); using current shell" >&2
    return 0
  fi

  if [[ -n ${UL_LIB_DIR:-} ]]; then
    export UL_LIB_DIR
  fi

  echo "==> entering dev shell at $script_dir..." >&2

  local sentinel
  sentinel="$(mktemp)"
  # The in-shell command removes the sentinel as its first act, so its presence
  # afterward means we never entered the shell. nix's own stderr is left visible
  # (not suppressed) so a broken flake's real error is reported.
  # shellcheck disable=SC2016  # $UL_DEVSHELL_SENTINEL and $@ are expanded by the inner shell, intentionally
  UL_DEVSHELL_SENTINEL="$sentinel" \
    nix develop "$script_dir" --command bash -c 'rm -f "$UL_DEVSHELL_SENTINEL"; exec bash "$@"' ul-reexec "$script" "$@"
  local rc=$?

  if [[ -e $sentinel ]]; then
    rm -f "$sentinel"
    echo "WARNING: nix develop failed at $script_dir — falling back to system tools" >&2
    return 0
  fi
  rm -f "$sentinel"
  exit "$rc"
}

ul_setup() {
  local project_name="$1"
  local script_dir="$2"
  _UL_SCRIPT_DIR="$script_dir"

  # shellcheck disable=SC1091
  source "${_UL_LOCKS_LIB_DIR}/update-cache-lib.bash"
  ul_init "$project_name" "$script_dir"

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
  # Remove stale daemon socket regardless of prior config — nix flake import
  # fails with "unsupported type" if the .ipc socket exists in the source tree.
  rm -f .git/fsmonitor--daemon.ipc
  trap '_ul_cleanup EXIT' EXIT
  trap '_ul_cleanup INT' INT
  trap '_ul_cleanup TERM' TERM

  _UL_STEPS_RAN=0
  _UL_STEPS_SUCCEEDED=0
  _UL_STEPS_FAILED=0
  _UL_STEPS_SKIPPED=0
  _UL_STEPS_DEFERRED=0
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
    if _ul_commit_updated "$step_name" "$commit_msg"; then
      _UL_STEPS_SUCCEEDED=$((_UL_STEPS_SUCCEEDED + 1))
    fi
  elif [[ $rc -eq $UL_RC_ATTEMPTED ]]; then
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    if _ul_commit_stamp_only "$step_name"; then
      echo "  ⊘ Step '${step_name}' attempted — no update applied (deferred)"
      _UL_STEPS_DEFERRED=$((_UL_STEPS_DEFERRED + 1))
    fi
  else
    echo "  ✗ Step '${step_name}' failed (exit code ${rc})"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
  fi
}

# Commit a successful step: format content if any changed, write the stamp,
# and commit everything in one commit (content + stamp, or stamp-only on a
# no-op success). On fmt/commit failure: roll back, record failure, return 1.
_ul_commit_updated() {
  local step_name="$1" commit_msg="$2"
  if ! git diff --quiet || ! git diff --cached --quiet; then
    if ! nix fmt; then
      echo "  ✗ Step '${step_name}' nix fmt failed"
      git reset --hard HEAD 2>/dev/null || true
      git clean -fd 2>/dev/null || true
      _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
      _UL_FAILED_STEPS+=("$step_name")
      return 1
    fi
  fi
  ul_write_stamp "$step_name"
  if ! git add -A || ! git commit -m "$commit_msg" >/dev/null; then
    echo "  ✗ Step '${step_name}' commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}

# Commit only the step's stamp (used after a deferral rolled back content).
_ul_commit_stamp_only() {
  local step_name="$1"
  ul_write_stamp "$step_name"
  if ! git add -- "$_UL_STAMP_DIR/$step_name" || \
     ! git commit -m "update-locks: ${step_name} attempted, no update applied" >/dev/null; then
    echo "  ✗ Step '${step_name}' stamp commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}

ul_finalize() {
  echo ""
  echo "=== Update Summary ==="
  echo "  Ran:     ${_UL_STEPS_RAN}"
  echo "  Passed:  ${_UL_STEPS_SUCCEEDED}"
  echo "  Deferred: ${_UL_STEPS_DEFERRED}"
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
