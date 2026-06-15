#!/usr/bin/env bash
# S9: missing-terminal-multi-sink
# sink-a and sink-b both depend on shared-lib; no terminal configured.
# Lock fails with missing_terminal.
set -euo pipefail

WSROOT="$PWD"

# Create shared-lib (no workspace inputs)
mkdir -p "$WSROOT/shared-lib"
cd "$WSROOT/shared-lib"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "shared-lib";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create sink-a (depends on shared-lib)
mkdir -p "$WSROOT/sink-a"
cd "$WSROOT/sink-a"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<FLAKE
{
  description = "sink-a";
  inputs = {
    shared-lib.url = "file://${WSROOT}/shared-lib";
    shared-lib.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create sink-b (depends on shared-lib)
mkdir -p "$WSROOT/sink-b"
cd "$WSROOT/sink-b"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<FLAKE
{
  description = "sink-b";
  inputs = {
    shared-lib.url = "file://${WSROOT}/shared-lib";
    shared-lib.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Write pn-workspace.toml (no terminal)
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s9"

[repos.sink-a]
url = "file://${WSROOT}/sink-a"

[repos.sink-b]
url = "file://${WSROOT}/sink-b"

[repos.shared-lib]
url = "file://${WSROOT}/shared-lib"
TOML
