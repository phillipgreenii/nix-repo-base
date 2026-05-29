# shellcheck shell=bash
# pn-workspace-update: Update all flake dependencies

_root_arg=""
_workspace_arg=""
_override_specs=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-update: Update all flake dependencies

Purpose: Updates all flake dependencies (lock files) for every workspace repo
without applying changes. For each project in order, it pulls the latest remote
changes, runs update-locks.sh, and pushes. After all projects are updated,
regenerates the pn-workspace.lock file.

Projects without a configured upstream are still refreshed locally
(update-locks.sh runs); pull and push are skipped with an informational
message.

Usage: pn-workspace-update [OPTIONS]

Options:
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Updates run in the swapped path.
                                Also accepts PN_WORKSPACE_OVERRIDE_PATHS env var
                                with comma-separated entries.

Example:
  # Update all flake dependencies
  pn-workspace-update

  # Typically followed by pn-workspace-apply to apply the updates
  pn-workspace-update && pn-workspace-apply
HELP
    exit 0
    ;;
  --root)
    _root_arg="$2"
    shift 2
    ;;
  --root=*)
    _root_arg="${1#*=}"
    shift
    ;;
  --workspace)
    _workspace_arg="$2"
    shift 2
    ;;
  --workspace=*)
    _workspace_arg="${1#*=}"
    shift
    ;;
  --override-path)
    _override_specs+=("$2")
    shift 2
    ;;
  --override-path=*)
    _override_specs+=("${1#*=}")
    shift
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n $_root_arg && -n $_workspace_arg ]]; then
  echo "error: --root and --workspace are mutually exclusive (use --root)" >&2
  exit 1
fi

if [[ -n $_workspace_arg ]]; then
  echo "warning: --workspace is deprecated; use --root instead" >&2
  _root_arg="$_workspace_arg"
fi

PN_WORKSPACE_ROOT=$(workspace_resolve_root "$_root_arg") || exit 1

overrides_json=$(workspace_parse_overrides "${_override_specs[@]}") || exit 1

_child_pid=""
_current_project=""

_cleanup() {
  local signal="$1"
  if [[ -n $_child_pid ]] && kill -0 "$_child_pid" 2>/dev/null; then
    kill -TERM "$_child_pid" 2>/dev/null
    wait "$_child_pid" 2>/dev/null || true
  fi
  if [[ -n $_current_project ]]; then
    echo "" >&2
    echo "Interrupted during: $_current_project" >&2
  fi
  trap - "$signal" EXIT
  exit $((128 + $(kill -l "$signal")))
}
trap '_cleanup INT' INT
trap '_cleanup TERM' TERM

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

failed_projects=()
skipped_projects=()

# Run a single per-project step, tracking it as the current child so the
# existing signal traps (_cleanup) still kill it. Returns the command's exit
# code; never exits early.
_run_step() {
  local label="$1"
  shift
  "$@" &
  _child_pid=$!
  local rc=0
  wait "$_child_pid" || rc=$?
  _child_pid=""
  if [[ $rc -ne 0 ]]; then
    echo "  ✗ $label failed for $_current_project (exit $rc)" >&2
  fi
  return $rc
}

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || {
    failed_projects+=("$project_name (cd failed)")
    echo
    continue
  }

  # Skip the project entirely if the working tree has uncommitted changes.
  # We do this BEFORE git pull so the autostash dance doesn't touch the WIP.
  # Check matches ul_setup's: modified + staged only (untracked files allowed).
  if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "  ⊘ skipping $project_name — working tree has uncommitted changes" >&2
    git status --short
    skipped_projects+=("$project_name")
    echo
    continue
  fi

  pull_failed=false
  project_failed=false

  if workspace_has_upstream; then
    if ! _run_step "git pull" git pull --rebase --autostash; then
      pull_failed=true
      project_failed=true
    fi
  fi

  # Skip update-locks if pull failed: working tree is suspect.
  if ! $pull_failed; then
    if ! _run_step "update-locks" ./update-locks.sh; then
      project_failed=true
      # keep going to push the steps that committed successfully
    fi
  fi

  # Push only when pull succeeded. Push even on partial update-locks failure —
  # each ul_run_step commits atomically, so successful work should land remotely.
  if workspace_has_upstream && ! $pull_failed; then
    if ! _run_step "git push" git push; then
      project_failed=true
    fi
  elif ! workspace_has_upstream; then
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
  fi

  if $project_failed; then
    failed_projects+=("$project_name")
  fi

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

# Regenerate lock file even if some projects failed — captures whatever did update.
echo "  --== Regenerating workspace lock ==--  "
pn-discover-workspace "$PN_WORKSPACE_ROOT" >"$PN_WORKSPACE_ROOT/pn-workspace.lock"
echo

if [[ ${#skipped_projects[@]} -gt 0 ]]; then
  echo "=== Skipped projects (${#skipped_projects[@]}) — dirty working tree ==="
  for p in "${skipped_projects[@]}"; do
    echo "  ⊘ $p"
  done
fi

if [[ ${#failed_projects[@]} -gt 0 ]]; then
  echo "=== Failed projects (${#failed_projects[@]}) ==="
  for p in "${failed_projects[@]}"; do
    echo "  ✗ $p"
  done
fi

if [[ ${#skipped_projects[@]} -gt 0 || ${#failed_projects[@]} -gt 0 ]]; then
  exit 1
fi

echo "✓ All projects updated successfully"
