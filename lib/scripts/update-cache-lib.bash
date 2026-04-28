# shellcheck shell=bash
# Update cache library for zn-self-upgrade
# Provides time-based caching for remote-checking steps.
# Source this file, then use ul_init, ul_should_run, ul_mark_done.

UL_STALE_SECONDS="${UL_STALE_SECONDS:-43200}"
UL_FORCE="${NIX_UL_FORCE_UPDATE:-false}"
UL_CI_MODE="${UL_CI_MODE:-false}"
UL_STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/zn-self-upgrade"

_UL_PROJECT=""

ul_init() {
  _UL_PROJECT="$1"
  mkdir -p "$UL_STATE_DIR/$_UL_PROJECT/steps"
}

ul_should_run() {
  local step_name="$1"
  local marker="$UL_STATE_DIR/$_UL_PROJECT/steps/$step_name"

  if [[ $UL_FORCE == "true" || $UL_CI_MODE == "true" ]]; then
    return 0
  fi

  if [[ ! -f $marker ]]; then
    return 0
  fi

  local now marker_mtime age remaining
  now=$(date +%s)
  marker_mtime=$(stat -c %Y "$marker" 2>/dev/null || stat -f %m "$marker")
  age=$((now - marker_mtime))

  if [[ $age -ge $UL_STALE_SECONDS ]]; then
    return 0
  fi

  remaining=$((UL_STALE_SECONDS - age))
  local hours minutes seconds
  hours=$((remaining / 3600))
  minutes=$(((remaining % 3600) / 60))
  seconds=$((remaining % 60))

  local last_run
  last_run=$(date -r "$marker_mtime" "+%Y-%m-%d %H:%M:%S" 2>/dev/null ||
    date -d "@$marker_mtime" "+%Y-%m-%d %H:%M:%S")

  echo -e "\033[33mSkipping ${step_name}: last successful at ${last_run}, next eligible in ${hours}h ${minutes}m ${seconds}s\033[0m"
  return 1
}

ul_mark_done() {
  local step_name="$1"
  local marker="$UL_STATE_DIR/$_UL_PROJECT/steps/$step_name"
  mkdir -p "$(dirname "$marker")"
  touch "$marker"
}

ul_needs_rebuild() {
  if [[ $UL_FORCE == "true" ]]; then
    return 0
  fi

  local project_path
  for project_path in "$@"; do
    local project_name
    project_name=$(basename "$project_path")
    local hash_file="$UL_STATE_DIR/apply/applied-hash/$project_name"
    local current_hash
    current_hash=$(cd "$project_path" && git rev-parse HEAD)

    if [[ ! -f $hash_file ]]; then
      return 0
    fi

    local stored_hash
    stored_hash=$(cat "$hash_file")
    if [[ $current_hash != "$stored_hash" ]]; then
      return 0
    fi
  done

  echo -e "\033[33mSkipping rebuild: all project HEADs match last successful apply\033[0m"
  return 1
}

ul_mark_applied() {
  mkdir -p "$UL_STATE_DIR/apply/applied-hash"
  local project_path
  for project_path in "$@"; do
    local project_name
    project_name=$(basename "$project_path")
    local hash_file="$UL_STATE_DIR/apply/applied-hash/$project_name"
    (cd "$project_path" && git rev-parse HEAD) >"$hash_file"
  done
}

ul_check_nix_daemon() {
  if [[ $UL_CI_MODE == "true" ]]; then
    return 0
  fi

  local rc=0
  timeout 10 nix eval --expr 'true' >/dev/null 2>&1 || rc=$?

  if [[ $rc -eq 0 ]]; then
    return 0
  fi

  if [[ $rc -eq 124 ]]; then
    # timeout — daemon is wedged
    if [[ -t 0 ]]; then
      echo -e "\033[33mNix daemon appears unresponsive (timed out after 10s).\033[0m" >&2
      read -r -p "Restart nix daemon? (requires sudo) [Y/n] " answer </dev/tty
      if [[ ${answer:-Y} =~ ^[Yy]$ ]]; then
        echo "Restarting nix daemon..." >&2
        sudo launchctl kickstart -k system/org.nixos.nix-daemon
        sleep 2
        # Re-check after restart
        if timeout 10 nix eval --expr 'true' >/dev/null 2>&1; then
          echo "Nix daemon restarted successfully." >&2
          return 0
        fi
        echo "ERROR: Nix daemon still unresponsive after restart." >&2
        return 1
      fi
    fi
    echo "ERROR: Nix daemon is unresponsive. Fix manually:" >&2
    echo "  sudo launchctl kickstart -k system/org.nixos.nix-daemon" >&2
    return 1
  fi

  # Other failure (daemon not running, nix broken, etc.)
  echo "ERROR: nix daemon health check failed (exit $rc)." >&2
  echo "  Try: sudo launchctl kickstart -k system/org.nixos.nix-daemon" >&2
  return 1
}
