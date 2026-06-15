#!/usr/bin/env bash
# S3: subdir-flake
# terminal-repo has flake.nix at nix/flake.nix (non-default path).
# lib-repo has flake.nix at root (default path).
# After lock: terminal-repo.flake_path == "nix/flake.nix", lib-repo has no flake_path.
set -euo pipefail

WSROOT="$PWD"

# Create lib-repo (default flake path at root)
mkdir -p "$WSROOT/lib-repo"
cd "$WSROOT/lib-repo"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "lib-repo";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Create terminal-repo (flake at nix/flake.nix)
mkdir -p "$WSROOT/terminal-repo/nix"
cd "$WSROOT/terminal-repo"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >nix/flake.nix <<FLAKE
{
  description = "terminal-repo";
  inputs = {
    lib-repo.url = "file://${WSROOT}/lib-repo";
    lib-repo.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add nix/flake.nix
git commit -m "init"

# Write pn-workspace.toml
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s3"
terminal = "terminal-repo"

[repos.terminal-repo]
url = "file://${WSROOT}/terminal-repo"
flake_path = "nix/flake.nix"

[repos.lib-repo]
url = "file://${WSROOT}/lib-repo"
TOML
