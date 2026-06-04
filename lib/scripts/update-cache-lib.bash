# shellcheck shell=bash
# Update cache library for zn-self-upgrade
# Provides time-based caching for remote-checking steps.
# Source this file, then use ul_init, ul_should_run, ul_write_stamp.

UL_STALE_SECONDS="${UL_STALE_SECONDS:-43200}"
UL_FORCE="${NIX_UL_FORCE_UPDATE:-false}"
UL_CI_MODE="${UL_CI_MODE:-false}"
UL_STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/zn-self-upgrade"

_UL_PROJECT=""
_UL_STAMP_DIR=""

ul_init() {
  _UL_PROJECT="$1"
  _UL_STAMP_DIR="$2/.update-locks/steps"
}

# Convert an ISO-8601 UTC timestamp ("2026-06-04T12:00:00Z") to epoch seconds.
# Tries BSD date (macOS) then GNU date (Linux). Non-zero exit if unparseable.
_ul_iso_to_epoch() {
  local iso="$1"
  date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$iso" +%s 2>/dev/null ||
    date -u -d "$iso" +%s 2>/dev/null
}

# Write the current time (ISO-8601 UTC) as this step's in-repo stamp.
ul_write_stamp() {
  local step_name="$1"
  mkdir -p "$_UL_STAMP_DIR"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$_UL_STAMP_DIR/$step_name"
}

ul_should_run() {
  local step_name="$1"
  local stamp="$_UL_STAMP_DIR/$step_name"

  if [[ $UL_FORCE == "true" ]]; then
    return 0
  fi
  # NOTE: UL_CI_MODE intentionally does NOT bypass — CI respects the shared,
  # committed gate. UL_CI_MODE only governs the daemon health-check elsewhere.

  [[ -f $stamp ]] || return 0

  local stored_iso stored_epoch now age
  stored_iso=$(<"$stamp")
  stored_epoch=$(_ul_iso_to_epoch "$stored_iso") || return 0   # unparseable → run
  now=$(date +%s)
  age=$((now - stored_epoch))

  if [[ $age -ge $UL_STALE_SECONDS ]]; then
    return 0
  fi

  local remaining=$((UL_STALE_SECONDS - age))
  local hours=$((remaining / 3600))
  local minutes=$(((remaining % 3600) / 60))
  local seconds=$((remaining % 60))
  echo -e "\033[33mSkipping ${step_name}: last successful at ${stored_iso}, next eligible in ${hours}h ${minutes}m ${seconds}s\033[0m"
  return 1
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
