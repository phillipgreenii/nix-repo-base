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

  # ─── System Profile ──────────────────────────────────────────────────────────
  section_header "System Profiles"
  local sys_profile
  sys_profile=$(discover_system_profile)
  echo "Profile: $sys_profile"
  list_generations "$sys_profile" sudo
  local sys_size
  sys_size=$(profile_closure_size "$sys_profile")
  echo "Closure size: $sys_size"
  echo ""

  # ─── Home Manager Profile ────────────────────────────────────────────────────
  section_header "Home Manager"
  local hm_profile
  hm_profile=$(discover_home_manager_profile)
  echo "Profile: $hm_profile"
  if [[ -e $hm_profile ]]; then
    list_generations "$hm_profile"
    local hm_size
    hm_size=$(profile_closure_size "$hm_profile")
    echo "Closure size: $hm_size"
  else
    echo "(not installed)"
  fi
  echo ""

  # ─── User Profiles ───────────────────────────────────────────────────────────
  section_header "User Profiles"
  local profile
  while IFS= read -r profile; do
    echo "Profile: $profile"
    list_generations "$profile"
    local psize
    psize=$(profile_closure_size "$profile")
    echo "Closure size: $psize"
    echo ""
  done < <(discover_user_profiles)
  echo ""

  # ─── Devbox Global Profile ───────────────────────────────────────────────────
  section_header "Devbox Global"
  local devbox_global
  devbox_global=$(discover_devbox_global_profile)
  if [[ -n $devbox_global ]]; then
    echo "Profile: $devbox_global"
    list_generations "$devbox_global"
    local dg_size
    dg_size=$(profile_closure_size "$devbox_global")
    echo "Closure size: $dg_size"
  else
    echo "(not installed)"
  fi
  echo ""

  # ─── Devbox Project Profiles ─────────────────────────────────────────────────
  section_header "Devbox Projects"
  local -a search_dirs=()
  while IFS= read -r d; do
    search_dirs+=("$d")
  done < <(discover_search_dirs)

  if [[ ${#search_dirs[@]} -gt 0 ]]; then
    while IFS= read -r profile; do
      echo "Profile: $profile"
      list_generations "$profile"
      local dp_size
      dp_size=$(profile_closure_size "$profile")
      echo "Closure size: $dp_size"
      echo ""
    done < <(discover_devbox_projects "${search_dirs[@]}")
  else
    echo "(no search dirs configured)"
  fi
  echo ""

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
