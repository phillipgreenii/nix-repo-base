#!/usr/bin/env bash
# S10: missing-flake-path
# consumer has a flake.nix and references producer by URL.
# producer does NOT have a flake.nix.
# Lock fails with missing_flake_path.
set -euo pipefail

WSROOT="$PWD"

# Create producer repo WITHOUT a flake.nix (just a README).
mkdir -p "$WSROOT/producer"
cd "$WSROOT/producer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > README.md << 'README'
This repo intentionally has no flake.nix.
README
git add README.md
git commit -m "init (no flake)"

# Create consumer repo WITH a flake.nix that declares producer as an input.
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

# Write pn-workspace.toml (terminal = consumer; producer has no flake.nix).
cd "$WSROOT"
cat > pn-workspace.toml << TOML
[workspace]
name = "smoke-s10"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "file://${WSROOT}/producer"
TOML
