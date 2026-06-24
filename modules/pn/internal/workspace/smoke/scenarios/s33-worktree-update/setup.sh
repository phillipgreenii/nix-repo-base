#!/usr/bin/env bash
# S33: worktree-isolated update (the new default).
# One bare-remote repo (terminal). Its update-locks.sh makes a COMMITTABLE change
# so the branch advances and pushes. Assertions are on git state (remote main
# advanced, primary main fast-forwarded, no leftover .pn-update worktree) — not
# on marker files, which the worktree flow leaves in the (removed) worktree.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

BARE="$REMOTES_DIR/solo.git"
git init --bare -b main "$BARE"
WORK="$(mktemp -d)"
git clone "file://${BARE}" "$WORK"
git -C "$WORK" config user.email "smoke@test.invalid"
git -C "$WORK" config user.name "smoke"
cat >"$WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
echo "v0" >"$WORK/locked.txt"
cat >"$WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
n=$(cat locked.txt 2>/dev/null || echo v0)
echo "${n}x" >locked.txt
git add locked.txt
git commit -m "update-locks: bump locked.txt" >/dev/null
SH
chmod +x "$WORK/update-locks.sh"
git -C "$WORK" add flake.nix update-locks.sh locked.txt
git -C "$WORK" commit -m "init"
git -C "$WORK" push -u origin main
rm -rf "$WORK"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s33"
terminal = "solo"

[repos.solo]
url = "file://${BARE}"
TOML
