# shellcheck shell=bash
# pn-discover-workspace — Discover workspace repos and their dependency order

set -euo pipefail

# ─── Usage ────────────────────────────────────────────────────────────────────

usage() {
  cat <<'EOF'
Usage: pn-discover-workspace <workspace_root>

Scan <workspace_root>/*/  for directories containing flake.nix, determine
their dependency order via flake inputs, and output a JSON array in topological
order (dependencies first, terminal flake last).

Output format:
  [
    { "path": "/workspace/repo-a", "inputName": "repo-a-input" },
    { "path": "/workspace/repo-b" }
  ]

The terminal flake (the one no other local flake depends on) has no inputName.

Arguments:
  workspace_root    Directory to scan for flake repos

Options:
  -h, --help        Show this help message
EOF
}

# ─── Argument parsing ─────────────────────────────────────────────────────────

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  usage
  exit 0
fi

workspace_root="${1:?Usage: pn-discover-workspace <workspace_root>}"

# ─── URL helpers ──────────────────────────────────────────────────────────────

# Extract "owner/repo" slug from various git remote URL forms.
# Returns empty string if no match.
extract_github_slug() {
  local url="$1"

  # github:owner/repo or github:owner/repo/subdir
  if [[ $url =~ ^github:([^/]+/[^/]+)(/.*)?$ ]]; then
    echo "${BASH_REMATCH[1]}"
    return 0
  fi

  # https://github.com/owner/repo or https://github.com/owner/repo.git
  if [[ $url =~ ^https://github\.com/([^/]+/[^/]+)(\.git)?(/.*)?$ ]]; then
    echo "${BASH_REMATCH[1]%.git}"
    return 0
  fi

  # git@github.com:owner/repo.git
  if [[ $url =~ ^git@github\.com:([^/]+/[^.]+)(\.git)?$ ]]; then
    echo "${BASH_REMATCH[1]}"
    return 0
  fi

  # ssh://git@github.com/owner/repo
  if [[ $url =~ ^ssh://git@github\.com/([^/]+/[^/]+)(\.git)?(/.*)?$ ]]; then
    echo "${BASH_REMATCH[1]%.git}"
    return 0
  fi

  echo ""
}

# ─── Workspace scanning ───────────────────────────────────────────────────────

# Arrays indexed by position (parallel arrays keyed by index)
declare -a repo_paths=()
declare -a repo_slugs=()
declare -a repo_inputs_json=()

while IFS= read -r -d '' dir; do
  # dir is workspace_root/somerepo — check for flake.nix
  [[ -f "$dir/flake.nix" ]] || continue

  # Get git remote URL; skip if git remote fails
  remote_url=$(git -C "$dir" remote get-url origin 2>/dev/null) || continue
  [[ -n $remote_url ]] || continue

  slug=$(extract_github_slug "$remote_url")

  # Get declared inputs from flake.
  # NOTE: `nix eval --json` can be slow (~300ms per flake). A future optimisation
  # could read inputs directly from flake.lock to avoid spawning nix per repo.
  inputs_json=$(nix eval --json --file "$dir/flake.nix" "inputs" 2>/dev/null || echo "{}")

  repo_paths+=("$dir")
  repo_slugs+=("$slug")
  repo_inputs_json+=("$inputs_json")
done < <(find "$workspace_root" -mindepth 1 -maxdepth 1 -type d -print0 | sort -z)

total=${#repo_paths[@]}

if ((total == 0)); then
  echo "[]"
  exit 0
fi

# ─── Build dependency graph ───────────────────────────────────────────────────

# depends_on[i] = space-separated list of indices that repo i depends on
declare -a depends_on=()
# rdep_count[i] = number of repos that depend on repo i
declare -a rdep_count=()

for ((i = 0; i < total; i++)); do
  depends_on[i]=""
  rdep_count[i]=0
done

for ((i = 0; i < total; i++)); do
  inputs="${repo_inputs_json[$i]}"
  # Extract all URL values from the inputs JSON
  # Input structure: { "inputName": { "url": "github:owner/repo", ... }, ... }
  while IFS= read -r input_url; do
    [[ -z $input_url ]] && continue
    input_slug=$(extract_github_slug "$input_url")
    [[ -z $input_slug ]] && continue
    # Check if input_slug matches any local repo
    for ((j = 0; j < total; j++)); do
      [[ $i == "$j" ]] && continue
      if [[ ${repo_slugs[$j]} == "$input_slug" ]]; then
        # repo i depends on repo j
        depends_on[i]="${depends_on[i]} $j"
        rdep_count[j]=$((rdep_count[j] + 1))
      fi
    done
  done < <(echo "$inputs" | jq -r '.. | strings | select(startswith("github:") or startswith("https://github.com") or startswith("git@github.com"))' 2>/dev/null)
done

# ─── Find terminal repo ───────────────────────────────────────────────────────

# Terminal repo: the one no other local repo depends on (rdep_count == 0).
# If multiple, take the last alphabetically.
terminal_idx=-1
terminal_path=""
for ((i = 0; i < total; i++)); do
  if ((rdep_count[i] == 0)); then
    if [[ -z $terminal_path || ${repo_paths[$i]} > $terminal_path ]]; then
      terminal_path="${repo_paths[$i]}"
      terminal_idx=$i
    fi
  fi
done

# ─── Assign inputName for non-terminal repos ─────────────────────────────────

declare -a input_names=()
for ((i = 0; i < total; i++)); do
  input_names[i]=""
done

if ((terminal_idx >= 0)); then
  terminal_inputs="${repo_inputs_json[$terminal_idx]}"
  # For each non-terminal repo, find the input key in the terminal flake that
  # matches the repo's slug
  for ((i = 0; i < total; i++)); do
    [[ $i == "$terminal_idx" ]] && continue
    slug="${repo_slugs[$i]}"
    [[ -z $slug ]] && continue
    # Iterate over keys in terminal_inputs; find one whose url matches slug
    while IFS=$'\t' read -r key url; do
      [[ -z $url ]] && continue
      url_slug=$(extract_github_slug "$url")
      if [[ $url_slug == "$slug" ]]; then
        input_names[i]="$key"
        break
      fi
    done < <(echo "$terminal_inputs" | jq -r 'to_entries[] | [.key, ([.value | .. | strings | select(startswith("github:") or startswith("https://github.com") or startswith("git@github.com"))] | first // "")] | @tsv' 2>/dev/null)
  done
fi

# ─── Topological sort (Kahn's algorithm) ─────────────────────────────────────

declare -a in_degree=()
declare -a adj=() # adj[j] = space-separated list of repos that depend on j

for ((i = 0; i < total; i++)); do
  local_dep_count=0
  for dep_idx in ${depends_on[$i]}; do
    [[ -n $dep_idx ]] || continue
    ((local_dep_count++)) || true
  done
  in_degree[i]=$local_dep_count
  adj[i]=""
done

# Build adjacency list (reverse direction: dep -> dependents)
for ((i = 0; i < total; i++)); do
  for dep_idx in ${depends_on[$i]}; do
    [[ -n $dep_idx ]] || continue
    adj[dep_idx]="${adj[dep_idx]} $i"
  done
done

# Queue: nodes with in_degree == 0
declare -a queue=()
for ((i = 0; i < total; i++)); do
  if ((in_degree[i] == 0)); then
    queue+=("$i")
  fi
done

declare -a topo_order=()
while ((${#queue[@]} > 0)); do
  # Pop front
  node="${queue[0]}"
  queue=("${queue[@]:1}")
  topo_order+=("$node")

  # Decrement in_degree of dependents
  for dep in ${adj[$node]}; do
    [[ -n $dep ]] || continue
    in_degree[dep]=$((in_degree[dep] - 1))
    if ((in_degree[dep] == 0)); then
      queue+=("$dep")
    fi
  done
done

# If topo_order doesn't include all nodes (cycle), append remaining
for ((i = 0; i < total; i++)); do
  found=0
  for n in "${topo_order[@]}"; do
    [[ $n == "$i" ]] && found=1 && break
  done
  ((found == 0)) && topo_order+=("$i")
done

# ─── Emit JSON ────────────────────────────────────────────────────────────────

# Build path and name arrays for jq (indexed parallel to repo_paths/input_names)
paths_json=$(printf '%s\n' "${repo_paths[@]}" | jq -R . | jq -s .)
names_json=$(printf '%s\n' "${input_names[@]}" | jq -R . | jq -s .)
order_json=$(printf '%s\n' "${topo_order[@]}" | jq -s .)

jq -n \
  --argjson paths "$paths_json" \
  --argjson names "$names_json" \
  --argjson order "$order_json" \
  '
  [ $order[] as $idx |
    if $names[$idx] != "" then
      {"path": $paths[$idx], "inputName": $names[$idx]}
    else
      {"path": $paths[$idx]}
    end
  ]
  '
