#!/usr/bin/env bash
# S31: happy-path rebase [branch]
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# Each bare remote has a "main" branch with extra commits that the workspace
# clones do not have yet. The workspace clones sit on a "feature-s31" branch
# that diverged from main's initial commit.
#
# `pn workspace rebase main` (positional arg) rebases each workspace clone's
# feature-s31 branch onto the local ref "main". Since a positional [branch] is
# given, NO fetch occurs — we verify this by checking that the bare remote's
# reflog has no new entries after the command.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

git_id() {
  local d="$1"
  git -C "$d" config user.email "smoke@test.invalid"
  git -C "$d" config user.name "smoke"
}

# ---- producer bare remote: init commit on main ----
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
PRODUCER_INIT="$(git -C "$PRODUCER_WORK" rev-parse HEAD)"
git -C "$PRODUCER_WORK" push -u origin main

# Add an extra commit to main on the bare remote (this is what we rebase onto).
echo "main-extra" >"$PRODUCER_WORK/main-extra.txt"
git -C "$PRODUCER_WORK" add main-extra.txt
git -C "$PRODUCER_WORK" commit -m "main-extra"
git -C "$PRODUCER_WORK" push origin main
rm -rf "$PRODUCER_WORK"

# ---- consumer bare remote: init commit on main ----
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
CONSUMER_INIT="$(git -C "$CONSUMER_WORK" rev-parse HEAD)"
git -C "$CONSUMER_WORK" push -u origin main

# Add an extra commit to main on the bare remote.
echo "main-extra" >"$CONSUMER_WORK/main-extra.txt"
git -C "$CONSUMER_WORK" add main-extra.txt
git -C "$CONSUMER_WORK" commit -m "main-extra"
git -C "$CONSUMER_WORK" push origin main
rm -rf "$CONSUMER_WORK"

# ---- clone repos into workspace, create feature branch from init commit ----
PRODUCER_CLONE="$WSROOT/producer"
CONSUMER_CLONE="$WSROOT/consumer"
git clone -b main "file://${PRODUCER_BARE}" "$PRODUCER_CLONE"
git clone -b main "file://${CONSUMER_BARE}" "$CONSUMER_CLONE"
git_id "$PRODUCER_CLONE"
git_id "$CONSUMER_CLONE"

# Create feature-s31 branch from the init commit (not the latest main).
# This means the workspace clones are behind main and need to rebase onto it.
git -C "$PRODUCER_CLONE" checkout -b feature-s31 "${PRODUCER_INIT}"
git -C "$CONSUMER_CLONE" checkout -b feature-s31 "${CONSUMER_INIT}"

# Add a commit on the feature branch so there is something to rebase.
echo "feature-s31-work" >"$PRODUCER_CLONE/feature.txt"
git -C "$PRODUCER_CLONE" add feature.txt
git -C "$PRODUCER_CLONE" commit -m "feature-s31 work"

echo "feature-s31-work" >"$CONSUMER_CLONE/feature.txt"
git -C "$CONSUMER_CLONE" add feature.txt
git -C "$CONSUMER_CLONE" commit -m "feature-s31 work"

# Capture bare-remote reflog lengths before the command. We write these
# to a file that the assertion hook will compare against post-command lengths
# to verify no fetch occurred.
git -C "$PRODUCER_BARE" reflog --all 2>/dev/null | wc -l >"$WSROOT/producer-reflog-before.txt" || echo 0 >"$WSROOT/producer-reflog-before.txt"
git -C "$CONSUMER_BARE" reflog --all 2>/dev/null | wc -l >"$WSROOT/consumer-reflog-before.txt" || echo 0 >"$WSROOT/consumer-reflog-before.txt"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s31"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
