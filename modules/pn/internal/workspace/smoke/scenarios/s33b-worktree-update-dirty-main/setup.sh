#!/usr/bin/env bash
# S33b: worktree-isolated update with a DIRTY primary main.
# Same topology as S33 (one bare-remote terminal repo with a committable
# update-locks.sh) but the primary clone carries an uncommitted modification to
# flake.nix before `workspace update`. The dirty file does NOT collide with the
# relocked path (locked.txt), so the ff-first integration fast-forwards on the
# FIRST ff attempt — no autostash round-trip occurs — leaving primary main
# fast-forwarded to the relock with the dirty change intact and an empty stash
# list. (The collision path that DOES autostash + retry + restore is covered by
# the TestUpdateViaWorktree_DirtyMainCollidesAutostashes unit test, not here.)
# Pre-cloning `solo` here mirrors S22b;
# `workspace clone` in command.txt is idempotent and skips the existing clone.
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

# Pre-clone the primary checkout and leave it on a DIRTY main: an uncommitted
# modification to flake.nix (a tracked path the relock never touches). git sees
# this as dirty, so step 8 classifies primaryOnDirtyMain.
PRIMARY="$WSROOT/solo"
git clone -b main "file://${BARE}" "$PRIMARY"
git -C "$PRIMARY" config user.email "smoke@test.invalid"
git -C "$PRIMARY" config user.name "smoke"
echo "dirty-content" >>"$PRIMARY/flake.nix"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s33b"
terminal = "solo"

[repos.solo]
url = "file://${BARE}"
TOML
