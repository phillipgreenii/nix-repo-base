# shellcheck shell=bash
if [[ " $* " =~ " --help " ]] || [[ " $* " =~ " -h " ]]; then
  cat <<'HELP'
pn-store-audit: Audit Nix profile generations and store size

Usage: pn-store-audit [OPTIONS]

Options:
  --full         Include dead paths estimate (slow, requires sudo)
  -h, --help     Show this help message
  -v, --version  Show version information

Example:
  # Show profile generations and store usage
  pn-store-audit

  # Include reclaimable space estimate
  pn-store-audit --full
HELP
  exit 0
fi

main() {
  local full=false
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --full)
      full=true
      shift
      ;;
    -v | --version)
      echo "pn-store-audit 1.0.0"
      exit 0
      ;;
    *) shift ;;
    esac
  done

  # Helper: emit per-profile audit block (label header + path + generations + size).
  audit_profile() {
    local profile="$1"
    local category="$2"
    local sudo_cmd="${3:-}"
    echo "  $(format_profile_label "$profile" "$category"):"
    echo "    Profile: $profile"
    list_generations "$profile" "$sudo_cmd" | sed 's/^/    /'
    local size
    size=$(profile_closure_size "$profile")
    echo "    Closure size: $size"
    echo ""
  }

  # ─── System Profile ──────────────────────────────────────────────────────────
  section_header "System Profiles"
  local sys_profile
  sys_profile=$(discover_system_profile)
  audit_profile "$sys_profile" "system" sudo

  # ─── Home Manager Profile ────────────────────────────────────────────────────
  section_header "Home Manager"
  local hm_profile
  hm_profile=$(discover_home_manager_profile)
  if [[ -e $hm_profile ]]; then
    audit_profile "$hm_profile" "home-manager"
  else
    echo "  (not installed)"
    echo ""
  fi

  # ─── User Profiles ───────────────────────────────────────────────────────────
  section_header "User Profiles"
  local profile
  while IFS= read -r profile; do
    audit_profile "$profile" "user-profiles"
  done < <(discover_user_profiles)

  # ─── Devbox Global Profile ───────────────────────────────────────────────────
  section_header "Devbox Global"
  local devbox_global
  devbox_global=$(discover_devbox_global_profile)
  if [[ -n $devbox_global ]]; then
    audit_profile "$devbox_global" "devbox-global"
  else
    echo "  (not installed)"
    echo ""
  fi

  # ─── Devbox Project Profiles ─────────────────────────────────────────────────
  section_header "Devbox Projects"
  local -a search_dirs=()
  while IFS= read -r d; do
    search_dirs+=("$d")
  done < <(discover_search_dirs)

  if [[ ${#search_dirs[@]} -gt 0 ]]; then
    while IFS= read -r profile; do
      audit_profile "$profile" "devbox-projects"
    done < <(discover_devbox_projects "${search_dirs[@]}")
  else
    echo "  (no search dirs configured)"
    echo ""
  fi

  # ─── Nix Store ───────────────────────────────────────────────────────────────
  section_header "Nix Store"
  local store
  store=$(store_size)
  echo "Volume used: $store"

  if [[ $full == true ]]; then
    local dead
    dead=$(dead_paths_size)
    echo "Reclaimable (dead paths): $dead"
  fi
}

main "$@"
