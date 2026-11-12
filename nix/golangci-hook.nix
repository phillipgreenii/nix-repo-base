# Opt-in golangci-lint pre-commit hook (see docs/hooks.md "Linters run at
# pre-commit" and its HK-2 cross-reference). Consumers enable it with
#
#   phillipgreenii.pre-commit.extraHooks = pkgs: {
#     golangci-lint = inputs.nix-repo-base.lib.mkGolangciHook { inherit pkgs; };
#   };
#
# Behaviour:
#   - runs only in the Go modules (nearest go.mod above a staged file) that the
#     hook's files touch;
#   - `golangci-lint run --new-from-rev=<merge-base>` with the module's own
#     .golangci.yml (found by golangci-lint's upward search), so only NEW
#     findings block; pre-existing ones stay red in the module's checks.*-golangci
#     derivation (that is not an exclude);
#   - golangci-lint and go come from the hook's store closure with
#     GOTOOLCHAIN=local, and PATH is reset to that closure plus coreutils, git,
#     findutils; NO nix is on it (HK-2: a git hook MUST NOT invoke nix);
#   - skips with a notice inside the nix build sandbox (checks.pre-commit has no
#     network and cannot load the package graph); the checks.*-golangci
#     derivation still enforces lint there.
#
# Base revision: $PG_GOLANGCI_BASE_REV if set, else the merge-base of HEAD with
# origin/HEAD, origin/main, origin/master, main or master (first that resolves),
# else HEAD. PG_GOLANGCI_SHOW_PATH=1 prints the reset PATH and exits (test hook).
{
  pkgs,
  go ? pkgs.go,
  golangci-lint ? pkgs.golangci-lint,
}:
let
  script = pkgs.writeShellScript "golangci-lint-new-findings" ''
    set -euo pipefail
    export PATH="${
      pkgs.lib.makeBinPath [
        go
        golangci-lint
        pkgs.git
        pkgs.coreutils
        pkgs.findutils
      ]
    }"
    if [ -n "''${PG_GOLANGCI_SHOW_PATH:-}" ]; then
      printf '%s\n' "$PATH"
      exit 0
    fi
    if [ -n "''${IN_NIX_BUILD:-}" ] || [ -n "''${NIX_BUILD_TOP:-}" ]; then
      echo "golangci-lint hook: inside the nix build sandbox (checks.pre-commit); skipping (the module's checks.*-golangci derivation enforces lint)." >&2
      exit 0
    fi
    export GOTOOLCHAIN=local

    base="''${PG_GOLANGCI_BASE_REV:-}"
    if [ -z "$base" ]; then
      for ref in origin/HEAD origin/main origin/master main master; do
        if base="$(git merge-base HEAD "$ref" 2>/dev/null)"; then break; fi
        base=""
      done
    fi
    [ -n "$base" ] || base=HEAD

    top="$(git rev-parse --show-toplevel)"
    modules=""
    for f in "$@"; do
      d="$(dirname -- "$f")"
      while :; do
        if [ -f "$top/$d/go.mod" ]; then
          modules="$modules$d"$'\n'
          break
        fi
        [ "$d" = "." ] && break
        d="$(dirname -- "$d")"
      done
    done
    modules="$(printf '%s' "$modules" | sort -u)"

    rc=0
    while IFS= read -r m; do
      [ -n "$m" ] || continue
      echo "golangci-lint hook: $m (new findings since $base)" >&2
      (cd "$top/$m" && golangci-lint run --new-from-rev="$base" ./...) || rc=1
    done <<<"$modules"
    exit "$rc"
  '';
in
{
  enable = true;
  name = "golangci-lint (new findings, touched Go modules)";
  entry = "${script}";
  files = "(\\.go$|(^|/)go\\.(mod|sum)$)";
  pass_filenames = true;
  require_serial = true;
}
