#!/usr/bin/env bash
# S8: legacy-lockfile-migration
# Pre-seed pn-workspace.lock (old format). After lock, it should be gone,
# pn-workspace.lock.json should exist, stderr notices removal.
set -euo pipefail

WSROOT="$PWD"

mkdir -p "$WSROOT/myrepo"
cd "$WSROOT/myrepo"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << 'FLAKE'
{
  description = "myrepo";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

cd "$WSROOT"
cat > pn-workspace.toml << TOML
[workspace]
name = "smoke-s8"
terminal = "myrepo"

[repos.myrepo]
url = "file://${WSROOT}/myrepo"
TOML
# The pn-workspace.lock was already copied from the scenario dir.
