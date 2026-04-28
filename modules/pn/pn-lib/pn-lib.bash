# shellcheck shell=bash
# pn-lib.bash — Shared library for pn store and workspace commands

# ─── Config reading functions ─────────────────────────────────────────────────

_pn_store_toml() {
  echo "${XDG_CONFIG_HOME:-$HOME/.config}/pn/store.toml"
}

discover_search_dirs() {
  local config
  config=$(_pn_store_toml)
  [[ -f $config ]] || return 0
  yq -p=toml -oy '.search_dirs[]' "$config"
}

read_keep_days() {
  local config
  config=$(_pn_store_toml)
  if [[ -f $config ]]; then
    local val
    val=$(yq -p=toml -oy '.keep_days' "$config")
    if [[ -n $val && $val != "null" ]]; then
      echo "$val"
      return 0
    fi
  fi
  echo "14"
}

read_keep_count() {
  local config
  config=$(_pn_store_toml)
  if [[ -f $config ]]; then
    local val
    val=$(yq -p=toml -oy '.keep_count' "$config")
    if [[ -n $val && $val != "null" ]]; then
      echo "$val"
      return 0
    fi
  fi
  echo "3"
}

section_header() {
  echo "=== $1 ==="
}

format_size() {
  local bytes="$1"
  if ((bytes >= 1073741824)); then
    awk "BEGIN { printf \"%.1f GB\", $bytes / 1073741824 }"
  elif ((bytes >= 1048576)); then
    awk "BEGIN { printf \"%.1f MB\", $bytes / 1048576 }"
  elif ((bytes >= 1024)); then
    awk "BEGIN { printf \"%.1f KB\", $bytes / 1024 }"
  else
    echo "$bytes B"
  fi
}

# ─── Discovery functions ──────────────────────────────────────────────────────

discover_system_profile() {
  echo "/nix/var/nix/profiles/system"
}

discover_home_manager_profile() {
  echo "$HOME/.local/state/nix/profiles/home-manager"
}

# Returns profile paths under ~/.local/state/nix/profiles/ excluding
# home-manager and generation links (matching -[0-9]+-link$).
discover_user_profiles() {
  local profiles_dir="$HOME/.local/state/nix/profiles"
  [[ -d $profiles_dir ]] || return 0
  local entry
  while IFS= read -r entry; do
    local name
    name=$(basename "$entry")
    # Exclude home-manager
    [[ $name == "home-manager" ]] && continue
    # Exclude generation links: name ends with -<digits>-link
    [[ $name =~ -[0-9]+-link$ ]] && continue
    echo "$entry"
  done < <(find "$profiles_dir" -maxdepth 1 -mindepth 1)
}

# Returns the devbox global profile path if it exists.
discover_devbox_global_profile() {
  local path="$HOME/.local/share/devbox/global/default/.devbox/nix/profile/default"
  if [[ -e $path ]]; then
    echo "$path"
  fi
  return 0
}

# Returns the devbox util profile path if it exists.
discover_devbox_util_profile() {
  local path="$HOME/.local/share/devbox/util/.devbox/nix/profile/default"
  if [[ -e $path ]]; then
    echo "$path"
  fi
  return 0
}

# Finds .devbox/nix/profile/default under given dirs with -maxdepth 5.
# Follows git worktrees. Deduplicates. Skips non-existent dirs with warning.
discover_devbox_projects() {
  local -A seen=()
  local dirs=("$@")

  # Collect all search dirs including git worktrees
  local -a all_dirs=()
  local dir
  for dir in "${dirs[@]}"; do
    if [[ ! -d $dir ]]; then
      echo "WARNING: search dir does not exist: $dir" >&2
      continue
    fi
    all_dirs+=("$dir")

    # Find git repos under this dir (up to depth 4 to allow for project subdirs)
    local repo
    while IFS= read -r repo; do
      # Get worktrees via git porcelain
      local wt_path
      while IFS= read -r wt_path; do
        [[ -d $wt_path ]] || continue
        # Avoid adding the repo dir itself again (already in all_dirs)
        [[ $wt_path == "$repo" ]] && continue
        all_dirs+=("$wt_path")
      done < <(git -C "$repo" worktree list --porcelain 2>/dev/null |
        awk '/^worktree / { print substr($0, 10) }')
    done < <(find "$dir" -maxdepth 4 \
      -name ".git" -type d -prune 2>/dev/null |
      sed 's|/\.git$||')
  done

  # Now search each dir for devbox profiles
  local search_dir
  for search_dir in "${all_dirs[@]}"; do
    [[ -d $search_dir ]] || continue
    local profile
    while IFS= read -r profile; do
      # Deduplicate by profile path
      if [[ -z ${seen[$profile]+x} ]]; then
        seen[$profile]=1
        echo "$profile"
      fi
    done < <(find "$search_dir" -maxdepth 5 \
      -path "*/.devbox/nix/profile/default" 2>/dev/null)
  done
}

# Finds result and result-* symlinks pointing to /nix/store/ under given dirs.
# These are leftover nix build outputs that act as GC roots.
# Usage: discover_result_symlinks <dir> [dir...]
discover_result_symlinks() {
  local dirs=("$@")
  local dir
  for dir in "${dirs[@]}"; do
    [[ -d $dir ]] || continue
    local entry
    while IFS= read -r entry; do
      # Only include symlinks pointing to /nix/store/
      if [[ $(readlink "$entry") == /nix/store/* ]]; then
        echo "$entry"
      fi
    done < <(find "$dir" -maxdepth 3 \( -name "result" -o -name "result-*" \) -type l 2>/dev/null)
  done
}

# Finds symlinks in ~/.nix-profiles/ whose mtime is older than keep_days.
# These are manual GC roots (e.g. nix-shell environments) that may be stale.
# keep_days=0 treats all entries as stale.
# Usage: discover_stale_nix_profiles <keep_days>
discover_stale_nix_profiles() {
  local keep_days="$1"
  local profiles_dir="$HOME/.nix-profiles"
  [[ -d $profiles_dir ]] || return 0

  if ((keep_days == 0)); then
    find "$profiles_dir" -maxdepth 1 -mindepth 1 -type l 2>/dev/null
  else
    find "$profiles_dir" -maxdepth 1 -mindepth 1 -type l -mtime +"$keep_days" 2>/dev/null
  fi
}

# Finds nh-darwin*/result symlinks in TMPDIR left by the nh (nix-darwin helper) tool.
# These are stale build outputs that act as GC roots.
# Usage: discover_nh_temp_roots
discover_nh_temp_roots() {
  local tmpdir="${TMPDIR:-/tmp}"
  [[ -d $tmpdir ]] || return 0
  local entry
  while IFS= read -r entry; do
    [[ -L $entry ]] && echo "$entry"
  done < <(find "$tmpdir" -maxdepth 2 -path "*/nh-darwin*/result" 2>/dev/null)
}

# ─── Generation functions ─────────────────────────────────────────────────────

# Wraps nix-env --list-generations and outputs: <gen_number> <date> <current|>
# Usage: list_generations <profile_path> [sudo]
list_generations() {
  local profile="$1"
  local sudo_cmd="${2:-}"

  $sudo_cmd nix-env --profile "$profile" --list-generations |
    awk '{
        gen=$1; date=$2;
        current="";
        for(i=3;i<=NF;i++){
          if($i ~ /\(current\)/) current="current";
        }
        print gen, date, current
      }'
}

# Returns generation numbers to delete using union semantics:
# keep if protected by EITHER time (younger than keep_days) OR count (within top keep_count).
# Current generation always kept. keep_count=0 disables count protection. keep_days=0 disables time.
# Usage: generations_to_prune <profile_path> <keep_days> <keep_count> [sudo]
generations_to_prune() {
  local profile="$1"
  local keep_days="$2"
  local keep_count="$3"
  local sudo_cmd="${4:-}"

  # Read all generations into arrays
  local -a gen_nums=()
  local -a gen_dates=()
  local -a gen_currents=()

  while read -r gen_num gen_date current; do
    gen_nums+=("$gen_num")
    gen_dates+=("$gen_date")
    gen_currents+=("$current")
  done < <(list_generations "$profile" "$sudo_cmd")

  local total=${#gen_nums[@]}
  ((total == 0)) && return 0

  # Compute cutoff timestamp for time protection
  local cutoff_ts=0
  if ((keep_days > 0)); then
    cutoff_ts=$(date -d "$keep_days days ago" +%s 2>/dev/null ||
      date -v-"${keep_days}d" +%s)
  fi

  # Determine which indices are protected by count (the top keep_count)
  local -A count_protected=()
  if ((keep_count > 0)); then
    local start=$((total - keep_count))
    ((start < 0)) && start=0
    local i
    for ((i = start; i < total; i++)); do
      count_protected[${gen_nums[$i]}]=1
    done
  fi

  # Emit gen numbers to delete
  local i
  for ((i = 0; i < total; i++)); do
    local num="${gen_nums[$i]}"
    local gdate="${gen_dates[$i]}"
    local is_current="${gen_currents[$i]}"

    # Never prune current
    [[ $is_current == "current" ]] && continue

    # Check count protection
    if [[ -n ${count_protected[$num]+x} ]]; then
      continue
    fi

    # Check time protection
    if ((keep_days > 0)); then
      local gen_ts
      gen_ts=$(date -d "$gdate" +%s 2>/dev/null || date -j -f "%Y-%m-%d" "$gdate" +%s)
      if ((gen_ts >= cutoff_ts)); then
        continue
      fi
    fi

    echo "$num"
  done
}

# Prunes generations by calling nix-env --delete-generations with explicit gen numbers.
# Usage: prune_generations <profile_path> <keep_days> <keep_count> [sudo]
prune_generations() {
  local profile="$1"
  local keep_days="$2"
  local keep_count="$3"
  local sudo_cmd="${4:-}"

  local -a to_delete=()
  while read -r gen_num; do
    to_delete+=("$gen_num")
  done < <(generations_to_prune "$profile" "$keep_days" "$keep_count" "$sudo_cmd")

  ((${#to_delete[@]} == 0)) && return 0

  $sudo_cmd nix-env --profile "$profile" --delete-generations "${to_delete[@]}"
}

# ─── Size functions ───────────────────────────────────────────────────────────

# Returns human-readable closure size for a profile path.
# Uses nix path-info -S on the resolved profile path.
# Returns "unknown" on failure.
profile_closure_size() {
  local profile="$1"
  local resolved
  resolved=$(readlink -f "$profile" 2>/dev/null || echo "$profile")

  local size_output
  if ! size_output=$(nix path-info -S "$resolved" 2>/dev/null); then
    echo "unknown"
    return 0
  fi

  local bytes
  bytes=$(echo "$size_output" | awk '{print $2}')
  if [[ -z $bytes || $bytes == "0" ]]; then
    echo "unknown"
    return 0
  fi

  format_size "$bytes"
}

# Returns human-readable volume usage for /nix/store.
# Uses df /nix/store and parses 512-byte blocks.
store_size() {
  local blocks
  blocks=$(df /nix/store | awk 'NR==2 {print $3}')
  local bytes=$((blocks * 512))
  format_size "$bytes"
}

# Returns human-readable total size of dead/unreachable store paths.
# Uses sudo nix-store --gc --print-dead then bulk nix path-info -S via xargs.
dead_paths_size() {
  local paths
  paths=$(sudo nix-store --gc --print-dead 2>/dev/null)

  if [[ -z $paths ]]; then
    format_size 0
    return 0
  fi

  local total_bytes
  total_bytes=$(echo "$paths" |
    xargs nix path-info -S 2>/dev/null |
    awk '{sum += $2} END {print sum+0}')

  format_size "$total_bytes"
}

# Prints a summary of store paths held only by running processes ({lsof} roots).
# Shows count and upper-bound reclaimable size. Informational only — no deletion.
# Usage: runtime_roots_summary
runtime_roots_summary() {
  local all_roots
  all_roots=$(nix-store --gc --print-roots 2>/dev/null) || return 0

  local lsof_paths file_paths lsof_only
  lsof_paths=$(echo "$all_roots" | grep '{lsof}' | awk '{print $3}' | sort -u)
  [[ -z $lsof_paths ]] && return 0

  file_paths=$(echo "$all_roots" | grep -v '{lsof}' | awk '{print $3}' | sort -u)
  if [[ -z $file_paths ]]; then
    lsof_only=$lsof_paths
  else
    lsof_only=$(comm -23 <(echo "$lsof_paths") <(echo "$file_paths"))
  fi
  [[ -z $lsof_only ]] && return 0

  local count
  count=$(echo "$lsof_only" | wc -l | tr -d ' ')

  local total_bytes
  total_bytes=$(echo "$lsof_only" |
    xargs nix path-info -S 2>/dev/null |
    awk '{sum += $2} END {print sum+0}')

  local size
  size=$(format_size "$total_bytes")

  local path_word="paths"
  ((count == 1)) && path_word="path"

  echo "$count store $path_word held only by running processes (up to $size reclaimable)"
  echo "  Tip: Restarting applications and re-running may free additional space"
}

# ─── Workspace functions ──────────────────────────────────────────────────────

# Walk up from CWD until finding a directory containing pn-workspace.toml.
# Echoes the path on success; returns 1 on failure.
find_workspace_root() {
  local dir
  dir="$(pwd)"
  while true; do
    if [[ -f "$dir/pn-workspace.toml" ]]; then
      echo "$dir"
      return 0
    fi
    local parent
    parent="$(dirname "$dir")"
    # Reached filesystem root
    if [[ $parent == "$dir" ]]; then
      return 1
    fi
    dir="$parent"
  done
}

# Like find_workspace_root but exits 1 with an error message if not found.
require_workspace_root() {
  local root
  if ! root=$(find_workspace_root); then
    printf "error: no pn-workspace.toml found in %s or any ancestor directory\n  Run 'pn-workspace-init <dir>' to initialize a workspace.\n" "$(pwd)" >&2
    exit 1
  fi
  echo "$root"
}

# Returns the list of workspace projects as JSON array with absolute paths.
# If use_lock is true (default) and pn-workspace.lock exists, reads from the lock file.
# Otherwise runs pn-discover-workspace to discover projects dynamically.
# Usage: workspace_get_projects <workspace_root>
workspace_get_projects() {
  local workspace_root="$1"
  local toml="$workspace_root/pn-workspace.toml"
  local lockfile="$workspace_root/pn-workspace.lock"

  # Read use_lock from toml (default true)
  local use_lock="true"
  if [[ -f $toml ]]; then
    local val
    val=$(yq -p=toml -oy '.use_lock' "$toml")
    if [[ $val == "false" ]]; then
      use_lock="false"
    fi
  fi

  if [[ $use_lock == "true" && -f $lockfile ]]; then
    # Convert relative paths to absolute paths
    jq --arg root "$workspace_root" '[.[] | . + {path: ($root + "/" + .path)}]' "$lockfile"
  else
    pn-discover-workspace "$workspace_root"
  fi
}

# Read a value from pn-workspace.toml using yq.
# Usage: workspace_read_toml <workspace_root> <key>
workspace_read_toml() {
  local workspace_root="$1"
  local key="$2"
  local toml="$workspace_root/pn-workspace.toml"
  yq -p=toml -oy ".$key" "$toml"
}
