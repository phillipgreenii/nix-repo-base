#!/usr/bin/env bash
# S13: clone-multi-remote
# myrepo configured with two remotes (origin + upstream).
# After clone, both remotes appear in git remote -v output.
set -euo pipefail

WSROOT="$PWD"

# Create the source bare repo (simulating origin)
mkdir -p "$WSROOT/_origin_bare"
cd "$WSROOT/_origin_bare"
git init --bare -b main

# Create upstream bare repo
mkdir -p "$WSROOT/_upstream_bare"
cd "$WSROOT/_upstream_bare"
git init --bare -b main

# We need a commit in the origin bare repo so clone works.
# Create a temp working repo, commit, push to bare.
tmpwork=$(mktemp -d)
cd "$tmpwork"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "myrepo";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"
git remote add origin "file://${WSROOT}/_origin_bare"
git push origin main
rm -rf "$tmpwork"

# Write pn-workspace.toml with real file:// URLs
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s13"
terminal = "myrepo"

[[repos.myrepo.remotes]]
name = "origin"
url = "file://${WSROOT}/_origin_bare"

[[repos.myrepo.remotes]]
name = "upstream"
url = "file://${WSROOT}/_upstream_bare"
TOML
