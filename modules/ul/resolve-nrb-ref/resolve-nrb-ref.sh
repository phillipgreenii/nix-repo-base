# shellcheck shell=bash
# resolve-nrb-ref: print the REV-PINNED nix-repo-base flake ref from a flake.lock.
#
# Shared by every consumer update-locks.sh (bead pg2-sdmk0y, ruling a+c on
# pg2-pwfqqn). FAIL CLOSED: there is deliberately no unpinned fallback. If the
# lock has no nix-repo-base node (or it carries no rev) this exits non-zero
# naming the node, because running `nix run github:phillipgreenii/nix-repo-base#...`
# unpinned would execute whatever is at the default branch in token-bearing CI
# (pg2-o784p).

DEFAULT_NODE="phillipgreenii-nix-base"

show_help() {
  cat <<'HELP'
resolve-nrb-ref: Print the rev-pinned nix-repo-base flake ref from a flake.lock.

Usage: resolve-nrb-ref [--lock FILE] [--node NAME] [-h|--help]

  --lock FILE  flake.lock to read (default: ./flake.lock)
  --node NAME  lock node holding nix-repo-base (default: phillipgreenii-nix-base)

Prints github:phillipgreenii/nix-repo-base/<rev> on stdout. Exits 1 with a
message naming the missing node if the lock has no such node or no rev; there is
no unpinned fallback. Fix by running `nix flake lock`.
HELP
}

lock="flake.lock"
node="$DEFAULT_NODE"
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --lock)
    [[ $# -ge 2 ]] || {
      echo "error: --lock needs a value" >&2
      exit 1
    }
    lock="$2"
    shift 2
    ;;
  --node)
    [[ $# -ge 2 ]] || {
      echo "error: --node needs a value" >&2
      exit 1
    }
    node="$2"
    shift 2
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ ! -f $lock ]]; then
  echo "error: flake lock not found: $lock; run 'nix flake lock'" >&2
  exit 1
fi

rev=$(jq -r --arg n "$node" '.nodes[$n].locked.rev // empty' "$lock") || {
  echo "error: could not parse $lock" >&2
  exit 1
}

if [[ -z $rev ]]; then
  echo "error: $lock has no locked nix-repo-base node '$node' (no .nodes.$node.locked.rev); refusing to fall back to unpinned HEAD. Run 'nix flake lock' to add it." >&2
  exit 1
fi

echo "github:phillipgreenii/nix-repo-base/${rev}"
