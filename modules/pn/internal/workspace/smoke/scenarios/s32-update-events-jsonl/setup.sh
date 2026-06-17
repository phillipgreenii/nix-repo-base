#!/usr/bin/env bash
# S32: pn workspace update writes events.jsonl
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# Each repo has an update-locks.sh that writes updated.txt and appends to order.log.
# After `workspace update`, the harness asserts that ${XDG_STATE_HOME}/pn/events.jsonl
# exists and contains the expected event kinds (run_start, project_result, run_end).
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
cat >"$PRODUCER_WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
touch updated.txt
echo producer >> "${WORKSPACE_ROOT}/order.log"
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
cat >"$CONSUMER_WORK/flake.nix" <<FLAKE
{
  inputs.producer.url = "file://${PRODUCER_BARE}";
  outputs = { self, producer, ... }: {};
}
FLAKE
cat >"$CONSUMER_WORK/update-locks.sh" <<'SH'
#!/bin/sh
set -e
touch updated.txt
echo consumer >> "${WORKSPACE_ROOT}/order.log"
SH
chmod +x "$CONSUMER_WORK/update-locks.sh"
git -C "$CONSUMER_WORK" add flake.nix update-locks.sh
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s32"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
