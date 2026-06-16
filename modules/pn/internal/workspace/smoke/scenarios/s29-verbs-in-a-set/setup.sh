#!/usr/bin/env bash
# S29: verbs-in-a-set
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# Bootstrap with init→clone→lock, create a worktree set "feature-y",
# then the test command (workspace worktree add feature-y) just bootstraps
# the set. The actual verb-chain (status→build→update→rebase main→push --set-upstream)
# is exercised from inside the set in the assertS29VerbsInASet hook.
#
# Fake build_command and update-locks.sh (no nix required).
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
# update-locks.sh: write updated.txt marker
cat >"$PRODUCER_WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
touch updated.txt
SH
chmod +x "$PRODUCER_WORK/update-locks.sh"
git -C "$PRODUCER_WORK" add flake.nix update-locks.sh
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
# build.sh: write built.txt marker (fake build)
cat >"$CONSUMER_WORK/build.sh" <<'SH'
#!/bin/sh
set -e
touch built.txt
SH
chmod +x "$CONSUMER_WORK/build.sh"
# update-locks.sh: write updated.txt marker
cat >"$CONSUMER_WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
touch updated.txt
SH
chmod +x "$CONSUMER_WORK/update-locks.sh"
git -C "$CONSUMER_WORK" add flake.nix build.sh update-locks.sh
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s29"
terminal = "consumer"
build_command = "./build.sh"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
