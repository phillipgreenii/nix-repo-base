# shellcheck shell=bash
# Update cache library for update-locks.
# Provides time-based caching for remote-checking steps.
# Source this file, then use ul_init, ul_should_run, ul_write_stamp.

UL_STALE_SECONDS="${UL_STALE_SECONDS:-43200}"
UL_FORCE="${NIX_UL_FORCE_UPDATE:-false}"
UL_CI_MODE="${UL_CI_MODE:-false}"
# State dir for the pre-commit-drv-path marker (consumed by sibling
# update-locks-lib.bash). Named for update-locks; the legacy "zn-self-upgrade"
# name was dropped (pg2-k8a6i) — an existing legacy marker is simply orphaned,
# triggering one harmless pre-commit reinstall.
# shellcheck disable=SC2034  # consumed by sibling update-locks-lib.bash
UL_STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/update-locks"

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
  # Reject anything that isn't a strict ISO-8601 UTC instant up front: GNU
  # `date -d` is lenient (parses "" and "now"), which would otherwise let an
  # empty/corrupt stamp read as "fresh" and skip the step. The strict match
  # keeps ul_should_run fail-open on bad input across both BSD and GNU date.
  [[ $iso =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
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
  stored_epoch=$(_ul_iso_to_epoch "$stored_iso") || return 0 # unparseable → run
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

# Run "$@" under a wall-clock timeout, tolerating hosts where GNU `timeout` is
# not on PATH (macOS has no `timeout` by default — coreutils ships it as
# `gtimeout`, and it may be absent entirely). Falls back to running without a
# timeout so the health check still executes instead of failing with
# command-not-found (pg2-k8a6i). Note: the 124 "wedged" exit is only observable
# when a timeout tool is present.
_ul_timeout() {
  local secs="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$secs" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$secs" "$@"
  else
    "$@"
  fi
}

ul_check_nix_daemon() {
  if [[ $UL_CI_MODE == "true" ]]; then
    return 0
  fi

  local rc=0
  _ul_timeout 10 nix eval --expr 'true' >/dev/null 2>&1 || rc=$?

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
        if _ul_timeout 10 nix eval --expr 'true' >/dev/null 2>&1; then
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
