#!/usr/bin/env bash
# S34: workforest subset create
# Three local repos: producer, consumer (consumer depends on producer), and
# extra (independent). `pn workspace workforest add feature-x --repos producer,consumer`
# must create a set containing ONLY producer + consumer (extra excluded), with
# the set's own pn-workspace.toml listing only those two.
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
git add flake.nix
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

# ---- extra (independent) ----
mk_repo extra
cat >flake.nix <<'FLAKE'
{
  description = "extra";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init" >/dev/null

# Write the real pn-workspace.toml with actual file:// URLs.
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s34"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "file://${WSROOT}/producer"

[repos.extra]
url = "file://${WSROOT}/extra"
TOML
