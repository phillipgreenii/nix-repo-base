#!/usr/bin/env bash
# s47: the NixOS probe of the built system fails, so pn workspace apply fails before any root step (ADR 0043). The built system's switch-to-configuration prints no usage and exits 3.
#
# Nothing real runs: no sudo, no nix-env, no systemd-run, no activation.
# Stand-ins in $WSROOT/fakebin (listed in path-prepend):
#   - nixos-rebuild  records its argv and its cwd, and leaves ./result -> the fake
#     system in ITS CURRENT DIRECTORY, as `nixos-rebuild build` does
#   - sudo           records its argv (sudo.N.argv) and its environment
#                    (sudo.N.env) and does NOT run the command (so no stand-in
#                    systemd-run or env is needed: the recorded argv is the evidence)
# The "system" is $WSROOT/store/<hash>-nixos-system-s47 with an executable
# activate, a regular nixos-version and a bin/switch-to-configuration. The
# harness builds pn with -tags smokeseams and PN_SMOKE_STORE_DIR=$WSROOT/store, so
# the out path validates against that fake store instead of /nix/store. The
# harness also sets PN_SMOKE_SYSTEMD (0 or 1): the NixOS platform's systemd
# decision, so the run is the same on a host with and without systemd.
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
  git -C "$work" add flake.nix
  git -C "$work" commit -m "init" >/dev/null
  git -C "$work" push -u origin main >/dev/null 2>&1
  rm -rf "$work"
}
mk_bare producer
mk_bare consumer

# The fake system. 32 characters of the nix base32 alphabet.
OUT="$WSROOT/store/0123456789abcdfghijklmnpqrsvwxyz-nixos-system-s47"
mkdir -p "$OUT/bin"
printf '#!/bin/sh\ntouch "%s/activate-ran.txt"\n' "$WSROOT" >"$OUT/activate"
printf '26.05.smoke\n' >"$OUT/nixos-version"
cat >"$OUT/bin/switch-to-configuration" <<'SH'
#!/bin/sh
# A script that does not behave like switch-to-configuration: no usage, exit 3.
echo "something else entirely" >&2
exit 3
SH
chmod +x "$OUT/activate" "$OUT/bin/switch-to-configuration"
printf '%s\n' "$OUT" >"$WSROOT/expected.out"

mkdir -p "$WSROOT/fakebin"
cat >"$WSROOT/fakebin/nixos-rebuild" <<SH
#!/bin/sh
pwd -P >"$WSROOT/build.cwd"
printf '%s\\n' "\$@" >"$WSROOT/build.argv"
echo "fake nixos-rebuild build ran"
ln -s "$OUT" result
SH
cat >"$WSROOT/fakebin/sudo" <<SH
#!/bin/sh
# Stateful like real sudo: the session forms (-n -v, -v, -k) are logged to
# sudo-session.log and keep a ticket marker; every other call is numbered
# sudo.N.argv so sudo.1 / sudo.2 are the two root steps.
case "\$*" in
"-n -v")
  echo "-n -v" >>"$WSROOT/sudo-session.log"
  [ -e "$WSROOT/sudo-ticket" ]
  exit \$?
  ;;
"-v")
  echo "-v" >>"$WSROOT/sudo-session.log"
  : >"$WSROOT/sudo-ticket"
  exit 0
  ;;
"-k")
  echo "-k" >>"$WSROOT/sudo-session.log"
  rm -f "$WSROOT/sudo-ticket"
  exit 0
  ;;
esac
n=1
while [ -e "$WSROOT/sudo.\$n.argv" ]; do n=\$((n + 1)); done
printf '%s\\n' "\$@" >"$WSROOT/sudo.\$n.argv"
env >"$WSROOT/sudo.\$n.env"
exit 0
SH
chmod +x "$WSROOT/fakebin/nixos-rebuild" "$WSROOT/fakebin/sudo"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s47"
terminal = "consumer"
build_command = "nixos-rebuild build --flake {terminal_nix_dir}#smokehost"
activate_command = "sudo {out}/bin/switch-to-configuration switch"

[repos.consumer]
url = "file://$REMOTES_DIR/consumer.git"

[repos.producer]
url = "file://$REMOTES_DIR/producer.git"
TOML
