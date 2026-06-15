#!/usr/bin/env bash
# S7: idempotent-rerun
# Two repos. Run init then lock; verify second run produces same output.
set -euo pipefail

WSROOT="$PWD"

mkdir -p "$WSROOT/producer"
cd "$WSROOT/producer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "producer";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

mkdir -p "$WSROOT/consumer"
cd "$WSROOT/consumer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
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
git commit -m "init"

cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s7"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "file://${WSROOT}/producer"
TOML
