# shellcheck shell=bash
# pn-workspace-init: Initialize a pn workspace directory

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-init: Initialize a pn workspace directory

Purpose: Creates pn-workspace.toml with default settings in the given directory
(or the current working directory if no directory is specified).

Usage: pn-workspace-init [OPTIONS] [<directory>]

Arguments:
  <directory>    Directory to initialize (default: current working directory)

Options:
  -h, --help     Show this help message and exit
  -f, --force    Overwrite existing pn-workspace.toml

Example:
  # Initialize workspace in current directory
  pn-workspace-init

  # Initialize workspace in a specific directory
  pn-workspace-init ~/my-workspace

  # Overwrite an existing workspace config
  pn-workspace-init --force ~/my-workspace
HELP
  exit 0
fi

force=0
dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -f | --force)
    force=1
    shift
    ;;
  -*)
    echo "pn-workspace-init: unknown option: $1" >&2
    echo "Run 'pn-workspace-init --help' for usage." >&2
    exit 1
    ;;
  *)
    if [[ -n $dir ]]; then
      echo "pn-workspace-init: unexpected argument: $1" >&2
      echo "Run 'pn-workspace-init --help' for usage." >&2
      exit 1
    fi
    dir="$1"
    shift
    ;;
  esac
done

# Default to CWD
dir="${dir:-$PWD}"

# Resolve to absolute path
dir="$(cd "$dir" && pwd)" || {
  echo "pn-workspace-init: directory not found: $dir" >&2
  exit 1
}

toml="$dir/pn-workspace.toml"

if [[ -f $toml && $force -eq 0 ]]; then
  echo "pn-workspace-init: $toml already exists (use --force to overwrite)" >&2
  exit 1
fi

cat >"$toml" <<'TOML'
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
pre_apply_hooks = []
post_apply_hooks = ["mas upgrade", "pn-osx-tcc-check"]
use_lock = true
TOML

echo "Created $toml"

# Generate initial lock file via pn-discover-workspace
if command -v pn-discover-workspace &>/dev/null; then
  if pn-discover-workspace "$dir" >"$dir/pn-workspace.lock" 2>/dev/null; then
    echo "Created $dir/pn-workspace.lock"
  else
    echo "Warning: pn-discover-workspace failed — lock file not generated" >&2
    rm -f "$dir/pn-workspace.lock"
  fi
else
  echo "Warning: pn-discover-workspace not found — lock file not generated" >&2
fi
