#!/usr/bin/env bash
# S42: pn workspace apply runs the build-as-user flow when
# [workspace].activate_command is set (pg2-sdbhh4.9, ADR 0038).
#
# Nothing real runs: no sudo, no nix-env, no activation. Stand-ins in
# $WSROOT/fakebin (listed in path-prepend):
#   - darwin-rebuild  records its argv and its cwd, and leaves ./result -> the fake
#     system in ITS CURRENT DIRECTORY, as `darwin-rebuild build` does
#   - sudo            records its argv (sudo.N.argv) and its environment
#                     (sudo.N.env) and does NOT run the command
# The "system" is $WSROOT/store/<hash>-darwin-system-s42 with an executable
# activate and a sw/bin/darwin-rebuild that prints the real usage on stderr and
# exits 1 (what the version probe expects). The harness builds pn with
# -tags smokeseams and PN_SMOKE_STORE_DIR=$WSROOT/store, so the out path
# validates against that fake store instead of /nix/store.
# apply_command is ./apply.sh, which would leave apply-ran.txt: it MUST NOT run
# (no fallback).
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

mk_bare() {
  local name="$1"
  local bare="$REMOTES_DIR/$name.git"
  git init --bare -b main "$bare" >/dev/null
  local work
  work="$(mktemp -d)"
  git clone "file://${bare}" "$work" >/dev/null 2>&1
  git -C "$work" config user.email "smoke@test.invalid"
  git -C "$work" config user.name "smoke"
  cat >"$work/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
  if [ "$name" = consumer ]; then
    printf '#!/bin/sh\ntouch apply-ran.txt\n' >"$work/apply.sh"
    chmod +x "$work/apply.sh"
    git -C "$work" add apply.sh
  fi
  git -C "$work" add flake.nix
  git -C "$work" commit -m "init" >/dev/null
  git -C "$work" push -u origin main >/dev/null 2>&1
  rm -rf "$work"
}
mk_bare producer
mk_bare consumer

# The fake system. 32 characters of the nix base32 alphabet.
OUT="$WSROOT/store/0123456789abcdfghijklmnpqrsvwxyz-darwin-system-s42"
mkdir -p "$OUT/sw/bin"
printf '#!/bin/sh\nexit 0\n' >"$OUT/activate"
cat >"$OUT/sw/bin/darwin-rebuild" <<'SH'
#!/bin/sh
echo "darwin-rebuild [--help] {edit | switch | activate | build | check | changelog}" >&2
exit 1
SH
chmod +x "$OUT/activate" "$OUT/sw/bin/darwin-rebuild"
printf '%s\n' "$OUT" >"$WSROOT/expected.out"

mkdir -p "$WSROOT/fakebin"
cat >"$WSROOT/fakebin/darwin-rebuild" <<SH
#!/bin/sh
pwd -P >"$WSROOT/build.cwd"
printf '%s\\n' "\$@" >"$WSROOT/build.argv"
echo "fake darwin-rebuild build ran"
ln -s "$OUT" result
SH
cat >"$WSROOT/fakebin/sudo" <<SH
#!/bin/sh
n=1
while [ -e "$WSROOT/sudo.\$n.argv" ]; do n=\$((n + 1)); done
printf '%s\\n' "\$@" >"$WSROOT/sudo.\$n.argv"
env >"$WSROOT/sudo.\$n.env"
exit 0
SH
chmod +x "$WSROOT/fakebin/darwin-rebuild" "$WSROOT/fakebin/sudo"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s42"
terminal = "consumer"
build_command = "darwin-rebuild build"
apply_command = "./apply.sh"
activate_command = "sudo {out}/sw/bin/darwin-rebuild activate"

[repos.consumer]
url = "file://$REMOTES_DIR/consumer.git"

[repos.producer]
url = "file://$REMOTES_DIR/producer.git"
TOML
