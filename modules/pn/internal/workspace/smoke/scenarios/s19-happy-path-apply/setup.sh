#!/usr/bin/env bash
# S19: happy-path apply
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# apply_command = "./apply.sh" writes applied.txt in the consumer (terminal) dir.
# Runs workspace init → clone → lock before the test command (workspace apply).
# Note: pn workspace apply no longer invokes `nix fmt`; formatting is a
# separate step via `pn workspace format`.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

# ---- producer bare remote ----
PRODUCER_BARE="$REMOTES_DIR/producer.git"
git init --bare -b main "$PRODUCER_BARE"
PRODUCER_WORK="$(mktemp -d)"
git clone "file://${PRODUCER_BARE}" "$PRODUCER_WORK"
git -C "$PRODUCER_WORK" config user.email "smoke@test.invalid"
git -C "$PRODUCER_WORK" config user.name "smoke"
cat >"$PRODUCER_WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
git -C "$PRODUCER_WORK" add flake.nix
git -C "$PRODUCER_WORK" commit -m "init"
git -C "$PRODUCER_WORK" push -u origin main
rm -rf "$PRODUCER_WORK"

# ---- consumer bare remote ----
CONSUMER_BARE="$REMOTES_DIR/consumer.git"
git init --bare -b main "$CONSUMER_BARE"
CONSUMER_WORK="$(mktemp -d)"
git clone "file://${CONSUMER_BARE}" "$CONSUMER_WORK"
git -C "$CONSUMER_WORK" config user.email "smoke@test.invalid"
git -C "$CONSUMER_WORK" config user.name "smoke"

# apply.sh: writes applied.txt marker in the consumer (terminal) dir.
cat >"$CONSUMER_WORK/apply.sh" <<'SH'
#!/bin/sh
set -e
touch applied.txt
SH
chmod +x "$CONSUMER_WORK/apply.sh"

# flake.nix: no external inputs; no formatter needed (apply no longer runs nix fmt).
cat >"$CONSUMER_WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE

git -C "$CONSUMER_WORK" add flake.nix apply.sh
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s19"
terminal = "consumer"
apply_command = "./apply.sh"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
