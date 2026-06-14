#!/usr/bin/env bash
# S8b: both pn-workspace.lock and pn-workspace.lock.json present.
# After lock, .lock is removed with same notice.
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
name = "smoke-s8b"
terminal = "myrepo"

[repos.myrepo]
url = "file://${WSROOT}/myrepo"
TOML

# Create pn-workspace.lock.json (the new format, already present)
cat > pn-workspace.lock.json << LOCKJSON
{
  "terminal": "myrepo",
  "order": ["myrepo"],
  "repos": {"myrepo": {"flake_path": "flake.nix", "remote_url": "file:///PLACEHOLDER"}},
  "edges": []
}
LOCKJSON
# pn-workspace.lock was already copied from scenario dir
