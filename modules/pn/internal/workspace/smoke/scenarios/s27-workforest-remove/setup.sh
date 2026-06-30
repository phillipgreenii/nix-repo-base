#!/usr/bin/env bash
# S27: workforest remove
# Two bare-remote repos. Bootstrap, create a workforest set for "feature-x",
# then the test command (workspace workforest remove feature-x) removes it.
# The branch "feature-x" should remain in each canonical repo.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

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
git_id "$CONSUMER_WORK"
cat >"$CONSUMER_WORK/flake.nix" <<'FLAKE'
{ inputs = {}; outputs = { self, ... }: {}; }
FLAKE
git -C "$CONSUMER_WORK" add flake.nix
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s27"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
