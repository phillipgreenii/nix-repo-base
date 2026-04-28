# shellcheck shell=bash
if [[ " $* " =~ " --help " ]] || [[ " $* " =~ " -h " ]]; then
  cat <<'HELP'
pn-store-deepclean: Clean old Nix profile generations, stale GC roots, and garbage collect the store

Usage: pn-store-deepclean [OPTIONS]

Options:
  --dry-run              Show what would be cleaned without deleting
  --keep-since <period>  Keep generations newer than this (default: from config, 14d)
                         Accepts: <number>d or <number>w. 0d disables time protection.
                         Also used for stale nix-profiles mtime threshold.
  --keep <count>         Keep N most recent generations (default: from config, 3)
                         0 disables count protection. Current always kept.
  -h, --help             Show this help message
  -v, --version          Show version information

Cleans:
  - System, home-manager, user, devbox profile generations
  - Result symlinks (nix build outputs) in search dirs
  - Stale ~/.nix-profiles/ entries (mtime older than --keep-since)
  - NH temp roots in TMPDIR

After cleanup, shows runtime roots summary (store paths held by running
processes that could be freed by restarting applications).

Example:
  # Dry run to see what would be cleaned
  pn-store-deepclean --dry-run

  # Aggressive clean keeping only 1 generation
  pn-store-deepclean --keep-since 0d --keep 1
HELP
  exit 0
fi

main() {
  local dry_run=false
  local keep_since_override=""
  local keep_override=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --dry-run)
      dry_run=true
      shift
      ;;
    --keep-since)
      keep_since_override="$2"
      shift 2
      ;;
    --keep)
      keep_override="$2"
      shift 2
      ;;
    -v | --version)
      echo "pn-store-deepclean 1.0.0"
      exit 0
      ;;
    *) shift ;;
    esac
  done

  # Read config defaults
  local cfg_keep_days cfg_keep_count
  cfg_keep_days=$(read_keep_days)
  cfg_keep_count=$(read_keep_count)

  # Resolve keep_days: CLI --keep-since overrides config
  local keep_days="$cfg_keep_days"
  if [[ -n $keep_since_override ]]; then
    if [[ $keep_since_override =~ ^([0-9]+)w$ ]]; then
      keep_days=$((BASH_REMATCH[1] * 7))
    elif [[ $keep_since_override =~ ^([0-9]+)d$ ]]; then
      keep_days="${BASH_REMATCH[1]}"
    else
      echo "ERROR: --keep-since must be <N>d or <N>w (e.g. 14d, 2w)" >&2
      exit 1
    fi
  fi

  # Resolve keep_count: CLI --keep overrides config
  local keep_count="$cfg_keep_count"
  if [[ -n $keep_override ]]; then
    keep_count="$keep_override"
  fi

  # Track pruned counts per category
  declare -A pruned_counts

  # Helper: process a single profile
  # Usage: process_profile <label> <profile_path> <category> [sudo]
  process_profile() {
    local label="$1"
    local profile="$2"
    local category="$3"
    local sudo_cmd="${4:-}"

    [[ -e $profile ]] || return 0

    local -a to_delete=()
    while IFS= read -r gen_num; do
      to_delete+=("$gen_num")
    done < <(generations_to_prune "$profile" "$keep_days" "$keep_count" "$sudo_cmd")

    local count=${#to_delete[@]}
    pruned_counts["$category"]=$((${pruned_counts[$category]:-0} + count))

    if [[ $count -eq 0 ]]; then
      echo "  $label: nothing to prune"
      return 0
    fi

    echo "  $label: $count generation(s) to prune (${to_delete[*]})"

    if [[ $dry_run == false ]]; then
      $sudo_cmd nix-env --profile "$profile" --delete-generations "${to_delete[@]}"
    fi
  }

  # ─── Collect store size before ───────────────────────────────────────────────
  local store_before
  store_before=$(store_size)

  # ─── System Profile ──────────────────────────────────────────────────────────
  section_header "System Profiles"
  local sys_profile
  sys_profile=$(discover_system_profile)
  process_profile "system" "$sys_profile" "system" sudo
  echo ""

  # ─── Home Manager Profile ────────────────────────────────────────────────────
  section_header "Home Manager"
  local hm_profile
  hm_profile=$(discover_home_manager_profile)
  process_profile "home-manager" "$hm_profile" "home-manager"
  echo ""

  # ─── User Profiles ───────────────────────────────────────────────────────────
  section_header "User Profiles"
  local profile
  while IFS= read -r profile; do
    local name
    name=$(basename "$profile")
    process_profile "$name" "$profile" "user-profiles"
  done < <(discover_user_profiles)
  echo ""

  # ─── Devbox Global Profile ───────────────────────────────────────────────────
  section_header "Devbox Global"
  local devbox_global
  devbox_global=$(discover_devbox_global_profile)
  if [[ -n $devbox_global ]]; then
    process_profile "devbox-global" "$devbox_global" "devbox-global"
  else
    echo "  (not installed)"
  fi
  echo ""

  # ─── Devbox Util Profile ─────────────────────────────────────────────────────
  section_header "Devbox Util"
  local devbox_util
  devbox_util=$(discover_devbox_util_profile)
  if [[ -n $devbox_util ]]; then
    process_profile "devbox-util" "$devbox_util" "devbox-util"
  else
    echo "  (not installed)"
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
      local proj_name
      proj_name=$(dirname "$(dirname "$(dirname "$profile")")")
      proj_name=$(basename "$proj_name")
      process_profile "$proj_name" "$profile" "devbox-projects"
    done < <(discover_devbox_projects "${search_dirs[@]}")
  else
    echo "  (no search dirs configured)"
  fi
  echo ""

  # ─── Result Symlinks ─────────────────────────────────────────────────────────
  section_header "Result Symlinks"
  local -a result_links=()
  local -a result_search_dirs=()
  while IFS= read -r d; do
    result_search_dirs+=("$d")
  done < <(discover_search_dirs)
  while IFS= read -r link; do
    result_links+=("$link")
  done < <(discover_result_symlinks "${result_search_dirs[@]}")

  if [[ ${#result_links[@]} -eq 0 ]]; then
    echo "  nothing to clean"
  else
    echo "  ${#result_links[@]} result symlink(s) to remove:"
    local link
    for link in "${result_links[@]}"; do
      echo "    $link"
    done
    if [[ $dry_run == false ]]; then
      for link in "${result_links[@]}"; do
        rm "$link"
      done
    fi
  fi
  pruned_counts["result-symlinks"]=${#result_links[@]}
  echo ""

  # ─── Stale Nix Profiles ─────────────────────────────────────────────────────
  section_header "Stale Nix Profiles"
  local -a stale_profiles=()
  while IFS= read -r entry; do
    stale_profiles+=("$entry")
  done < <(discover_stale_nix_profiles "$keep_days")

  if [[ ${#stale_profiles[@]} -eq 0 ]]; then
    echo "  nothing to clean"
  else
    echo "  ${#stale_profiles[@]} stale profile(s) to remove:"
    local entry
    for entry in "${stale_profiles[@]}"; do
      echo "    $(basename "$entry")"
    done
    if [[ $dry_run == false ]]; then
      for entry in "${stale_profiles[@]}"; do
        rm "$entry"
      done
    fi
  fi
  pruned_counts["stale-nix-profiles"]=${#stale_profiles[@]}
  echo ""

  # ─── NH Temp Roots ──────────────────────────────────────────────────────────
  section_header "NH Temp Roots"
  local -a nh_roots=()
  while IFS= read -r entry; do
    nh_roots+=("$entry")
  done < <(discover_nh_temp_roots)

  if [[ ${#nh_roots[@]} -eq 0 ]]; then
    echo "  nothing to clean"
  else
    echo "  ${#nh_roots[@]} temp root(s) to remove:"
    local entry
    for entry in "${nh_roots[@]}"; do
      echo "    $entry"
    done
    if [[ $dry_run == false ]]; then
      for entry in "${nh_roots[@]}"; do
        rm "$entry"
      done
    fi
  fi
  pruned_counts["nh-temp-roots"]=${#nh_roots[@]}
  echo ""

  # ─── Summary ─────────────────────────────────────────────────────────────────
  section_header "Summary"

  if [[ $dry_run == true ]]; then
    echo "DRY RUN — no changes made"
    echo ""
    echo "Would prune:"
    local category
    for category in system home-manager user-profiles devbox-global devbox-util devbox-projects result-symlinks stale-nix-profiles nh-temp-roots; do
      local cnt=${pruned_counts[$category]:-0}
      echo "  $category: $cnt generation(s)"
    done
    echo ""
    echo "Reclaimable estimate (dead paths):"
    dead_paths_size
  else
    echo "Store before: $store_before"
    # Run GC
    sudo nix-store --gc
    local store_after
    store_after=$(store_size)
    echo "Store after:  $store_after"
    echo ""
    echo "Pruned generations:"
    local category
    for category in system home-manager user-profiles devbox-global devbox-util devbox-projects result-symlinks stale-nix-profiles nh-temp-roots; do
      local cnt=${pruned_counts[$category]:-0}
      echo "  $category: $cnt generation(s)"
    done
    echo ""
    section_header "Runtime Roots"
    runtime_roots_summary
  fi
}

main "$@"
