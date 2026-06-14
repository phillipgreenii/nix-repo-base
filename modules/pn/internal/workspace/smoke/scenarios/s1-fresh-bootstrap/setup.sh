#!/usr/bin/env bash
# S1: fresh-bootstrap
# Creates two local git repos (producer, consumer) with flake.nix,
# then writes pn-workspace.toml pointing at them via file:// URLs.
set -euo pipefail

WSROOT="$PWD"

# Create producer repo
mkdir -p "$WSROOT/producer"
cd "$WSROOT/producer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << 'FLAKE'
{
  description = "producer";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create consumer repo
cd "$WSROOT"
mkdir -p "$WSROOT/consumer"
cd "$WSROOT/consumer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << FLAKE
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

# Write the real pn-workspace.toml with actual file:// URLs
cd "$WSROOT"
cat > pn-workspace.toml << TOML
[workspace]
name = "smoke-s1"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "file://${WSROOT}/producer"
TOML
