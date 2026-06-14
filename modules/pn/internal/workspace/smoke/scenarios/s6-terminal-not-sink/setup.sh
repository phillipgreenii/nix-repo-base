#!/usr/bin/env bash
# S6: terminal-not-sink
# alpha is configured as terminal, but beta declares alpha as an input.
# After lock: exits non-zero with terminal_not_sink error.
# Pre-seeded lock.json is preserved (unchanged) after failed lock.
set -euo pipefail

WSROOT="$PWD"

# Create alpha repo (has flake.nix, no workspace inputs)
mkdir -p "$WSROOT/alpha"
cd "$WSROOT/alpha"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << 'FLAKE'
{
  description = "alpha";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create beta repo (depends on alpha)
cd "$WSROOT"
mkdir -p "$WSROOT/beta"
cd "$WSROOT/beta"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << FLAKE
{
  description = "beta";
  inputs = {
    alpha.url = "file://${WSROOT}/alpha";
    alpha.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Write pn-workspace.toml (terminal = alpha, but beta consumes alpha)
cd "$WSROOT"
cat > pn-workspace.toml << TOML
[workspace]
name = "smoke-s6"
terminal = "alpha"

[repos.alpha]
url = "file://${WSROOT}/alpha"

[repos.beta]
url = "file://${WSROOT}/beta"
TOML

# Write seed lock.json (to verify it's preserved after failed lock)
cat > pn-workspace.lock.json << 'LOCKJSON'
{
  "terminal": "alpha",
  "order": ["alpha", "beta"],
  "repos": {
    "alpha": {"flake_path": "flake.nix", "remote_url": "file:///PLACEHOLDER/alpha"},
    "beta": {"flake_path": "flake.nix", "remote_url": "file:///PLACEHOLDER/beta"}
  },
  "edges": [{"consumer": "beta", "alias": "alpha", "target": "alpha"}]
}
LOCKJSON
