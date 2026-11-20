#!/usr/bin/env bash
# S33: worktree-isolated update (the new default).
# Two bare-remote repos: producer, and solo (terminal), which consumes producer
# through git+file:// with a committed flake.lock so the trailing flake relock
# has a real move to make (producer gains a second commit after the lock).
# solo's update-locks.sh makes a COMMITTABLE change
# so the branch advances and pushes. Assertions are on git state (remote main
# advanced, primary main fast-forwarded, no leftover .pn-update worktree) — not
# on marker files, which the worktree flow leaves in the (removed) worktree.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

git_id() {
  git -C "$1" config user.email "smoke@test.invalid"
  git -C "$1" config user.name "smoke"
}

# ---- producer bare remote (commit 1) ----
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

BARE="$REMOTES_DIR/solo.git"
git init --bare -b main "$BARE"
WORK="$(mktemp -d)"
git clone "file://${BARE}" "$WORK"
git -C "$WORK" config user.email "smoke@test.invalid"
git -C "$WORK" config user.name "smoke"
cat >"$WORK/flake.nix" <<FLAKE
{
  inputs.producer.url = "git+file://${PRODUCER_BARE}";
  outputs = { self, producer, ... }: {};
}
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
# Lock against producer commit 1 (only input is the local producer: no network).
# Skipped inside a nix build sandbox (NIX_BUILD_TOP set): a sandboxed builder
# cannot reach the nix daemon, so the relock half is proven by a host run.
if [ -z "${NIX_BUILD_TOP:-}" ]; then
  (cd "$WORK" && nix flake lock)
  git -C "$WORK" add flake.lock
fi
git -C "$WORK" commit -m "init"
git -C "$WORK" push -u origin main
rm -rf "$WORK"

# Producer commit 2: gives the relock a real move (skipped in a nix build
# sandbox together with the flake.lock above).
if [ -z "${NIX_BUILD_TOP:-}" ]; then
  echo "second" >"$PRODUCER_WORK/second.txt"
  git -C "$PRODUCER_WORK" add second.txt
  git -C "$PRODUCER_WORK" commit -m "second"
  git -C "$PRODUCER_WORK" push origin main
fi
rm -rf "$PRODUCER_WORK"

cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s33"
terminal = "solo"

[repos.solo]
url = "file://${BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
