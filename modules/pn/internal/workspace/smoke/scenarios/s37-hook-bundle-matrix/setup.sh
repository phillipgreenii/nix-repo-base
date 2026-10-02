#!/usr/bin/env bash
# S37: hook bundle acceptance matrix (bead pg2-pla9d.10; plan: per-clone hook
# bundle, "Acceptance matrix (with Task 6)").
#
# Two local repos: producer (carries `hook-version`, the producer change under
# test) and consumer (depends on producer through a flake input; declares the
# `{nix_run install-pre-commit-hooks}` hook on post-clone and post-status).
#
# No real nix, no network: `fakebin/nix` stands in for
#   nix run [--override-input A U]... <flakedir>#install-pre-commit-hooks [-- args]
# and lays out the SAME on-disk shape the real installer writes (generations,
# a regular-file `current` pointer, source.json, a marker-owned stub), with a
# stand-in bundle whose runner logs the tree, the bundle and the producer
# version it was built from. Every other `nix` call is delegated to the real
# one (`workspace lock` needs it). The assertions live in assertS37.
set -euo pipefail

WSROOT="$PWD"

mk_repo() {
  local name="$1"
  mkdir -p "$WSROOT/$name"
  cd "$WSROOT/$name"
  git init -b main >/dev/null
  git config user.email "smoke@test.invalid"
  git config user.name "smoke"
}

# ---- producer ----
mk_repo producer
cat >flake.nix <<'FLAKE'
{
  description = "producer";
  inputs = {};
  outputs = _: {};
}
FLAKE
printf 'v1\n' >hook-version
git add flake.nix hook-version
git commit -m "init" >/dev/null

# ---- consumer (depends on producer) ----
mk_repo consumer
cat >flake.nix <<FLAKE
{
  description = "consumer";
  inputs = {
    producer.url = "file://${WSROOT}/producer";
    producer.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init" >/dev/null

# ---- the stand-in nix ----
mkdir -p "$WSROOT/fakebin"
cat >"$WSROOT/fakebin/nix" <<'FAKENIX'
#!/bin/sh
here=$(cd "$(dirname "$0")" && pwd -P)
root=$(dirname "$here")

case " $* " in
*"#install-pre-commit-hooks"*) ;;
*)
  # Drop this directory from PATH (compared physically: $TMPDIR may be a
  # symlinked prefix such as /var -> /private/var) so `nix` resolves to the real one.
  newpath=""
  oldifs=$IFS
  IFS=:
  for d in $PATH; do
    [ "$(cd "$d" 2>/dev/null && pwd -P)" = "$here" ] && continue
    newpath="${newpath:+$newpath:}$d"
  done
  IFS=$oldifs
  PATH=$newpath
  exec nix "$@"
  ;;
esac

flake=""
private=0
ovs=""
after=0
shift # run
while [ $# -gt 0 ]; do
  if [ "$after" = 1 ]; then
    case $1 in
    --private) private=1 ;;
    --override)
      ovs="$ovs $2"
      shift
      ;;
    esac
  else
    case $1 in
    --override-input) shift 2 ;;
    --) after=1 ;;
    *'#install-pre-commit-hooks') flake=${1%%#*} ;;
    esac
  fi
  shift
done
printf 'FLAKE=%s PRIVATE=%s OVERRIDES=%s\n' "$flake" "$private" "$ovs" >>"$root/nix-calls.log"

cd "$flake" || exit 1
common=$(git rev-parse --path-format=absolute --git-common-dir)
gitdir=$(git rev-parse --path-format=absolute --absolute-git-dir)
linked=0
[ "$common" = "$gitdir" ] || linked=1
if [ "$linked" = 1 ] && [ "$private" = 0 ]; then
  echo "pg-hooks: install refused in a linked worktree" >&2
  exit 2
fi
if [ "$linked" = 1 ]; then pg="$gitdir/pg-hooks"; else pg="$common/pg-hooks"; fi
mkdir -p "$pg"

n=1
while [ -d "$pg/gen-$n" ]; do n=$((n + 1)); done
gen="gen-$n"
b="$pg/$gen/bundle"
mkdir -p "$b/bin"

# The producer this bundle is built from: the recorded pin, else the sibling
# canonical clone.
prod=""
for ov in $ovs; do
  case $ov in producer=*) prod=${ov#producer=} ;; esac
done
[ -n "$prod" ] || prod="$(dirname "$(dirname "$common")")/producer"
cat "$prod/hook-version" >"$b/producer-version"

printf '#!/bin/sh\nexit 0\n' >"$b/bin/prek"
cat >"$b/bin/pg-hooks-run" <<RUNNER
#!/bin/sh
printf 'stage=%s tree=%s version=%s bundle=%s\n' "\$1" "\$(pwd -P)" "\$(cat "$b/producer-version")" "$b" >>"$root/hook-runs.log"
RUNNER
chmod 755 "$b/bin/prek" "$b/bin/pg-hooks-run"
printf '{"repo":"","stampPaths":[],"stages":["pre-commit"]}\n' >"$b/meta.json"

stamp=$(git ls-files -s -- flake.lock flake.nix | git hash-object --stdin)
ovjson=""
for ov in $ovs; do
  name=${ov%%=*}
  path=${ov#*=}
  head=$(git -C "$path" rev-parse HEAD)
  dirty=false
  [ -z "$(git -C "$path" status --porcelain)" ] || dirty=true
  ovjson="${ovjson:+$ovjson,}{\"name\":\"$name\",\"path\":\"$path\",\"head\":\"$head\",\"dirty\":$dirty}"
done
printf '{"stamp":"%s","overrides":[%s],"clone_path":"%s","built_at":"%s"}\n' \
  "$stamp" "$ovjson" "$common" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$pg/$gen/source.json"

printf '%s\n' "$gen" >"$pg/.current.$$"
mv -f "$pg/.current.$$" "$pg/current"

# The marker-owned stub: written for the canonical clone; a private install
# only adds one that is absent (it never rewrites the shared stub).
hooks="$common/hooks"
mkdir -p "$hooks"
if [ "$linked" = 0 ] || [ ! -e "$hooks/pre-commit" ]; then
  cat >"$hooks/pre-commit.tmp.$$" <<'STUB'
#!/bin/sh
# managed-by: pg-hooks
gd=$(git rev-parse --path-format=absolute --absolute-git-dir) || exit 0
cd_=$(git rev-parse --path-format=absolute --git-common-dir) || exit 0
for d in "$gd/pg-hooks" "$cd_/pg-hooks"; do
  if [ -f "$d/current" ]; then
    read -r gen <"$d/current"
    exec "$d/$gen/bundle/bin/pg-hooks-run" pre-commit "$@"
  fi
done
exit 0
STUB
  chmod 755 "$hooks/pre-commit.tmp.$$"
  mv -f "$hooks/pre-commit.tmp.$$" "$hooks/pre-commit"
fi
FAKENIX
chmod 755 "$WSROOT/fakebin/nix"

# Write the real pn-workspace.toml with actual file:// URLs.
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s37"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[[repos.consumer.hooks]]
when = ["post-clone", "post-status"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.producer]
url = "file://${WSROOT}/producer"
TOML
