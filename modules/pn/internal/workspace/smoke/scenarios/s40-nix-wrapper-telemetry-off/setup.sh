#!/usr/bin/env bash
# S40: wrapper_path is valid and an endpoint is given, but --no-telemetry forces
# telemetry off: the build runs UNWRAPPED and nothing is exported (pg2-kqrrs.8).
#
# Two bare-remote repos (producer, consumer); consumer is the terminal.
# build_command = "darwin-rebuild build". Stand-ins in $WSROOT/fakebin (listed in
# path-prepend):
#   - darwin-rebuild   records the argv it receives in $WSROOT/darwin.argv
#   - pg-nix-log-wrapped  records ITS argv in $WSROOT/wrapper.argv, drops its own
#     flags up to the first "--", and execs the rest (the real wrapper's contract)
# ~/.config/pn/telemetry.toml (under the scrubbed XDG_CONFIG_HOME) names the fake
# wrapper. The endpoint is NOT in the file: only the final command passes
# --otlp-endpoint, so init/clone/lock/allow stay telemetry-free and fast.
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

mkdir -p "$WSROOT/fakebin"
cat >"$WSROOT/fakebin/darwin-rebuild" <<SH
#!/bin/sh
printf '%s\\n' "\$@" >"$WSROOT/darwin.argv"
echo "fake darwin-rebuild ran"
SH
cat >"$WSROOT/fakebin/pg-nix-log-wrapped" <<SH
#!/bin/sh
printf '%s\\n' "\$@" >"$WSROOT/wrapper.argv"
while [ "\$#" -gt 0 ] && [ "\$1" != "--" ]; do shift; done
shift
exec "\$@"
SH
chmod +x "$WSROOT/fakebin/darwin-rebuild" "$WSROOT/fakebin/pg-nix-log-wrapped"

mkdir -p "$XDG_CONFIG_HOME/pn"
cat >"$XDG_CONFIG_HOME/pn/telemetry.toml" <<TOML
wrapper_path = "$WSROOT/fakebin/pg-nix-log-wrapped"
TOML

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s40"
terminal = "consumer"
build_command = "darwin-rebuild build"

[repos.consumer]
url = "file://$REMOTES_DIR/consumer.git"

[repos.producer]
url = "file://$REMOTES_DIR/producer.git"
TOML
