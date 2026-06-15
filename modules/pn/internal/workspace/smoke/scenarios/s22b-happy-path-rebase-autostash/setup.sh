#!/usr/bin/env bash
# S22b: happy-path rebase with autostash round-trip
# Same topology as S22 but the workspace clone has an uncommitted file
# (dirty.txt) before the rebase. After rebase with --autostash, the file
# should survive (autostash re-applied) and the stash list should be empty.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

# ---- consumer bare remote: commits A then B ----
CONSUMER_BARE="$REMOTES_DIR/consumer.git"
git init --bare -b main "$CONSUMER_BARE"

CONSUMER_WORK="$(mktemp -d)"
git clone "file://${CONSUMER_BARE}" "$CONSUMER_WORK"
git -C "$CONSUMER_WORK" config user.email "smoke@test.invalid"
git -C "$CONSUMER_WORK" config user.name "smoke"

# Commit A: initial.
cat >"$CONSUMER_WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
git -C "$CONSUMER_WORK" add flake.nix
git -C "$CONSUMER_WORK" commit -m "commit-A"
COMMIT_A="$(git -C "$CONSUMER_WORK" rev-parse HEAD)"
git -C "$CONSUMER_WORK" push -u origin main

# Commit B: add a file.
echo "commit-b" >"$CONSUMER_WORK/commit-b.txt"
git -C "$CONSUMER_WORK" add commit-b.txt
git -C "$CONSUMER_WORK" commit -m "commit-B"
git -C "$CONSUMER_WORK" push origin main
rm -rf "$CONSUMER_WORK"

# ---- clone into workspace and reset to A ----
CONSUMER_CLONE="$WSROOT/consumer"
git clone -b main "file://${CONSUMER_BARE}" "$CONSUMER_CLONE"
git -C "$CONSUMER_CLONE" config user.email "smoke@test.invalid"
git -C "$CONSUMER_CLONE" config user.name "smoke"
# Force-reset workspace to commit A (one behind the remote).
git -C "$CONSUMER_CLONE" reset --hard "${COMMIT_A}"

# Add an uncommitted (modified tracked or untracked) file to exercise autostash.
# Use a modification to a tracked file so git recognizes it as dirty.
echo "dirty-content" >>"$CONSUMER_CLONE/flake.nix"

# Write the real pn-workspace.toml with actual file:// URL.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s22b"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"
TOML
