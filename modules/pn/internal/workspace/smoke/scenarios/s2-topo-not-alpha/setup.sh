#!/usr/bin/env bash
# S2: topo-not-alpha
# consumer "aaa" depends on producer "zzz". After lock, order == ["zzz","aaa"].
set -euo pipefail

WSROOT="$PWD"

# Create zzz (producer) repo
mkdir -p "$WSROOT/zzz"
cd "$WSROOT/zzz"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "zzz";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create aaa (consumer) repo
cd "$WSROOT"
mkdir -p "$WSROOT/aaa"
cd "$WSROOT/aaa"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<FLAKE
{
  description = "aaa";
  inputs = {
    zzz.url = "file://${WSROOT}/zzz";
    zzz.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Write pn-workspace.toml
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s2"
terminal = "aaa"

[repos.aaa]
url = "file://${WSROOT}/aaa"

[repos.zzz]
url = "file://${WSROOT}/zzz"
TOML
