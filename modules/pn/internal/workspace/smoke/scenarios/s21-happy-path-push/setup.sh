#!/usr/bin/env bash
# S21: happy-path push
# Two bare-remote repos (producer, consumer).
# Setup: create bare remotes, clone into workspace, commit a marker file
# in each clone. The test command (workspace push) pushes them to the
# bare remotes.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

# Helper: configure git identity in a repo dir.
git_id() {
  local d="$1"
  git -C "$d" config user.email "smoke@test.invalid"
  git -C "$d" config user.name "smoke"
}

# ---- producer bare remote ----
PRODUCER_BARE="$REMOTES_DIR/producer.git"
git init --bare -b main "$PRODUCER_BARE"
PRODUCER_WORK="$(mktemp -d)"
git clone "file://${PRODUCER_BARE}" "$PRODUCER_WORK"
git_id "$PRODUCER_WORK"
cat > "$PRODUCER_WORK/flake.nix" << 'FLAKE'
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
git_id "$CONSUMER_WORK"
cat > "$CONSUMER_WORK/flake.nix" << 'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
git -C "$CONSUMER_WORK" add flake.nix
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# ---- clone repos into workspace dir ----
PRODUCER_CLONE="$WSROOT/producer"
CONSUMER_CLONE="$WSROOT/consumer"
git clone -b main "file://${PRODUCER_BARE}" "$PRODUCER_CLONE"
git clone -b main "file://${CONSUMER_BARE}" "$CONSUMER_CLONE"
git_id "$PRODUCER_CLONE"
git_id "$CONSUMER_CLONE"

# Add a new commit in each workspace clone (something to push).
echo "pushed-by-smoke" > "$PRODUCER_CLONE/pushed.txt"
git -C "$PRODUCER_CLONE" add pushed.txt
git -C "$PRODUCER_CLONE" commit -m "smoke: add pushed.txt"

echo "pushed-by-smoke" > "$CONSUMER_CLONE/pushed.txt"
git -C "$CONSUMER_CLONE" add pushed.txt
git -C "$CONSUMER_CLONE" commit -m "smoke: add pushed.txt"

# Write the real pn-workspace.toml with actual file:// URLs.
cat > "$WSROOT/pn-workspace.toml" << TOML
[workspace]
name = "smoke-s21"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
