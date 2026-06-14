#!/usr/bin/env bash
# S23: happy-path format
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# consumer's flake.nix declares producer as an input so the workspace lock
# detects the topo edge (producer before consumer).
# Both repos' flake.nix declares a noop formatter so `nix fmt` succeeds.
# Runs workspace init → clone → lock before the test command (workspace format).
# Asserts: exit 0; stdout shows per-repo format banners in topo order.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

# Pre-built noop formatter derivation (already in the nix store on this host).
NOOP_FMT_DRV="/nix/store/nmlmz195lfa9p00v906g4r8mck669bnv-noop-fmt.drv"

# ---- producer bare remote ----
PRODUCER_BARE="$REMOTES_DIR/producer.git"
git init --bare -b main "$PRODUCER_BARE"
PRODUCER_WORK="$(mktemp -d)"
git clone "file://${PRODUCER_BARE}" "$PRODUCER_WORK"
git -C "$PRODUCER_WORK" config user.email "smoke@test.invalid"
git -C "$PRODUCER_WORK" config user.name "smoke"
cat > "$PRODUCER_WORK/flake.nix" << FLAKE
{
  inputs = {};
  outputs = { self, ... }:
  let noopFmt = import ${NOOP_FMT_DRV};
  in { formatter.x86_64-linux = noopFmt; };
}
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
# flake.nix: declare producer as input so workspace lock detects the edge
# (producer before consumer in topo order).
# Use git+file:// so `nix fmt` can fetch the bare-remote producer input
# (nix treats plain file:// as a tarball path, not a git repo).
cat > "$CONSUMER_WORK/flake.nix" << FLAKE
{
  inputs.producer.url = "git+file://${PRODUCER_BARE}";
  outputs = { self, producer, ... }:
  let noopFmt = import ${NOOP_FMT_DRV};
  in { formatter.x86_64-linux = noopFmt; };
}
FLAKE
git -C "$CONSUMER_WORK" add flake.nix
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat > "$WSROOT/pn-workspace.toml" << TOML
[workspace]
name = "smoke-s23"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
