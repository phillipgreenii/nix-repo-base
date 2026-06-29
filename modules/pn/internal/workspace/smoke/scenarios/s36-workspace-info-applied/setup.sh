#!/usr/bin/env bash
# S36: workspace info reflects applied-state after apply.
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# apply_command = "./apply.sh" writes applied.txt in the consumer dir; pn records
# each repo's applied_ref on success, surfaced by `pn workspace info --json`.
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
cat >"$CONSUMER_WORK/apply.sh" <<'SH'
#!/bin/sh
set -e
touch applied.txt
SH
chmod +x "$CONSUMER_WORK/apply.sh"
cat >"$CONSUMER_WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
git -C "$CONSUMER_WORK" add flake.nix apply.sh
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs and the wsid.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s36"
id = "smoke-s36"
terminal = "consumer"
apply_command = "./apply.sh"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
