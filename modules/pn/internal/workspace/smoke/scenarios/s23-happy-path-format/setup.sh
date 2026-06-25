#!/usr/bin/env bash
# S23: happy-path format
# Two bare-remote repos (producer, consumer). consumer is the terminal.
# consumer's flake.nix declares producer as an input so the workspace lock
# detects the topo edge (producer before consumer).
# Both repos' flake.nix declare a noop formatter (writeShellScriptBin from
# nixpkgs) so `nix fmt` succeeds without any host-specific /nix/store path.
# The formatter is defined for every default system (via nixpkgs.lib.genAttrs)
# so `nix fmt` resolves formatter.${currentSystem} on whatever host runs the
# test — a single hardcoded system fails with "platform mismatch" elsewhere.
# Each repo is pre-built (for the host system) and its flake.lock committed so
# that `nix fmt` in the workspace clones reuses the lock + already-realized
# derivation, keeping test stdout free of build progress.
# Runs workspace init → clone → lock before the test command (workspace format).
# Asserts: exit 0; stdout shows per-repo format banners in topo order.
#
# Note: this scenario requires a working `nix` (see the requires-nix marker in
# this directory). runScenario skips it when nix is unavailable.
set -euo pipefail

WSROOT="$PWD"
REMOTES_DIR="$WSROOT/remotes"
mkdir -p "$REMOTES_DIR"

# Resolve the host's nix system (e.g. x86_64-linux, aarch64-darwin) so the
# `nix build` pre-realization targets the platform `nix fmt` evaluates at test
# time. The formatter attr itself is defined for all default systems below.
HOST_SYSTEM="$(nix eval --raw --impure --expr builtins.currentSystem)"

# ---- producer bare remote ----
PRODUCER_BARE="$REMOTES_DIR/producer.git"
git init --bare -b main "$PRODUCER_BARE"
PRODUCER_WORK="$(mktemp -d)"
git clone "file://${PRODUCER_BARE}" "$PRODUCER_WORK"
git -C "$PRODUCER_WORK" config user.email "smoke@test.invalid"
git -C "$PRODUCER_WORK" config user.name "smoke"
cat >"$PRODUCER_WORK/flake.nix" <<'FLAKE'
{
  inputs.nixpkgs.url = "nixpkgs";
  outputs = { self, nixpkgs }: {
    formatter = nixpkgs.lib.genAttrs nixpkgs.lib.systems.flakeExposed (system:
      nixpkgs.legacyPackages.${system}.writeShellScriptBin "noop-fmt" "exit 0");
  };
}
FLAKE
# nix flake commands require flake.nix to be tracked by git when invoked
# inside a git working tree, so stage it before the pre-build.
git -C "$PRODUCER_WORK" add flake.nix
# Pre-build: realize the formatter (for the host system) and write flake.lock.
# Both are committed so the workspace clone of producer can run `nix fmt`
# without re-resolving inputs or building the formatter (output is already in
# the local nix store).
nix build --no-link "$PRODUCER_WORK#formatter.${HOST_SYSTEM}" >/dev/null
git -C "$PRODUCER_WORK" add flake.lock
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
# Consumer declares producer as an input so the workspace lock detects the
# topo edge (producer before consumer). Use git+file:// because nix treats
# plain file:// as a tarball path, not a git repo.
cat >"$CONSUMER_WORK/flake.nix" <<FLAKE
{
  inputs.nixpkgs.url = "nixpkgs";
  inputs.producer.url = "git+file://${PRODUCER_BARE}";
  inputs.producer.inputs.nixpkgs.follows = "nixpkgs";
  outputs = { self, nixpkgs, producer }: {
    formatter = nixpkgs.lib.genAttrs nixpkgs.lib.systems.flakeExposed (system:
      nixpkgs.legacyPackages.\${system}.writeShellScriptBin "noop-fmt" "exit 0");
  };
}
FLAKE
git -C "$CONSUMER_WORK" add flake.nix
nix build --no-link "$CONSUMER_WORK#formatter.${HOST_SYSTEM}" >/dev/null
git -C "$CONSUMER_WORK" add flake.lock
git -C "$CONSUMER_WORK" commit -m "init"
git -C "$CONSUMER_WORK" push -u origin main
rm -rf "$CONSUMER_WORK"

# Write the real pn-workspace.toml with actual file:// URLs.
cat >"$WSROOT/pn-workspace.toml" <<TOML
[workspace]
name = "smoke-s23"
terminal = "consumer"

[repos.consumer]
url = "file://${CONSUMER_BARE}"

[repos.producer]
url = "file://${PRODUCER_BARE}"
TOML
