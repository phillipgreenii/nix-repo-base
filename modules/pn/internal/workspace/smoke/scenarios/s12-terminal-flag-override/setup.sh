#!/usr/bin/env bash
# S12: terminal-flag-override
# Config has terminal = "wrong-terminal". Run lock --terminal real-terminal.
# Assert: exit 0; lock.terminal == "real-terminal"; toml unchanged.
set -euo pipefail

WSROOT="$PWD"

mkdir -p "$WSROOT/wrong-terminal"
cd "$WSROOT/wrong-terminal"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "wrong-terminal";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

mkdir -p "$WSROOT/real-terminal"
cd "$WSROOT/real-terminal"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "real-terminal";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s12"
terminal = "wrong-terminal"

[repos.wrong-terminal]
url = "file://${WSROOT}/wrong-terminal"

[repos.real-terminal]
url = "file://${WSROOT}/real-terminal"
TOML
