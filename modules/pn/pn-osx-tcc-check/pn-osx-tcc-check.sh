# shellcheck shell=bash
if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-osx-tcc-check: Check for duplicate TCC entries from Nix store path changes

Purpose: After applying nix-darwin configuration, Nix store paths change and
macOS TCC creates new permission entries while leaving old ones. This command
detects those duplicates and tells you which entries are stale.

Usage: pn-osx-tcc-check [OPTIONS]

Options:
  -h, --help     Show this help message and exit
  -v, --version  Show version information

Environment:
  TCC_DB_PATH    Override the TCC database path (for testing)

Example:
  # Check for TCC duplicates
  pn-osx-tcc-check
HELP
  exit 0
fi

TCC_DB="${TCC_DB_PATH:-$HOME/Library/Application Support/com.apple.TCC/TCC.db}"

if ! sqlite3 "$TCC_DB" "SELECT 1 FROM access LIMIT 1" &>/dev/null; then
  echo "⚠️  TCC check skipped — terminal lacks Full Disk Access"
  echo "   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]"
  exit 0
fi

duplicates=$(sqlite3 "$TCC_DB" "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;" 2>/dev/null | awk -F'|' '
BEGIN {
  # Human-readable labels for common TCC services
  service_display["kTCCServiceListenEvent"] = "kTCCServiceListenEvent (Input Monitoring)"
  service_display["kTCCServiceCamera"] = "kTCCServiceCamera (Camera)"
  service_display["kTCCServiceMicrophone"] = "kTCCServiceMicrophone (Microphone)"
  service_display["kTCCServiceAccessibility"] = "kTCCServiceAccessibility (Accessibility)"
  service_display["kTCCServiceSystemPolicyAllFiles"] = "kTCCServiceSystemPolicyAllFiles (Full Disk Access)"
}
{
  service = $1
  client = $2
  last_modified = $3

  # Group by the binary name (last path component) so version upgrades
  # (e.g. bash-5.2p37 -> bash-5.3p3) are treated as the same application
  n = split(client, path_parts, "/")
  bin_name = (n > 0) ? path_parts[n] : client

  key = service SUBSEP bin_name
  count[key]++
  clients[key, count[key]] = client
  services[key] = service
  names[key] = bin_name

  # Track newest timestamp per key
  if (!(key in newest_ts) || last_modified > newest_ts[key]) {
    newest_ts[key] = last_modified
    newest_client[key] = client
  }
}
END {
  found = 0
  # Sort keys for deterministic output
  nk = 0
  for (key in count) {
    if (count[key] <= 1) continue
    key_list[++nk] = key
  }
  for (i = 1; i <= nk; i++) {
    for (j = i + 1; j <= nk; j++) {
      k1 = key_list[i]
      k2 = key_list[j]
      if (services[k1] > services[k2] || (services[k1] == services[k2] && names[k1] > names[k2])) {
        tmp = key_list[i]
        key_list[i] = key_list[j]
        key_list[j] = tmp
      }
    }
  }
  for (ki = 1; ki <= nk; ki++) {
    key = key_list[ki]
    if (!found) {
      print "⚠️  TCC Duplicate Report"
      print "━━━━━━━━━━━━━━━━━━━━━━━━"
      print ""
      found = 1
    }

    stale = count[key] - 1
    display = (services[key] in service_display) ? service_display[services[key]] : services[key]
    printf "%s:\n", display

    printf "  %s — %d entries (%d stale)\n", names[key], count[key], stale

    # Print current first, then stales
    for (i = 1; i <= count[key]; i++) {
      c = clients[key, i]
      if (c == newest_client[key]) {
        printf "    ✓ %s (current)\n", c
      }
    }
    for (i = 1; i <= count[key]; i++) {
      c = clients[key, i]
      if (c != newest_client[key]) {
        printf "    ✗ %s (stale)\n", c
      }
    }
    print ""
  }

  if (found) {
    print "To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually."
  }
}
')

if [[ -n $duplicates ]]; then
  echo "$duplicates"
else
  echo "✅ No TCC duplicates found"
fi
exit 0
