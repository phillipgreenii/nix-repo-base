#!/usr/bin/env bash
# S35: workforest add-repo / remove-repo round trip
# Three local repos: producer, consumer (consumer depends on producer), and
# extra (independent). command.txt creates a subset set {producer, consumer};
# the assertS35WorktreeAddRemoveRepo hook then adds `extra` to the live set and
# removes it again, asserting set membership + the extra worktree dir track the
# changes and the canonical clones are unchanged (P1).
set -euo pipefail

WSROOT="$PWD"

mk_repo() {
  local name="$1"
  mkdir -p "$WSROOT/$name"
  cd "$WSROOT/$name"
  git init -b main >/dev/null
  git config user.email "smoke@test.invalid"
  git config user.name "smoke"
}

# ---- producer ----
mk_repo producer
cat >flake.nix <<'FLAKE'
{
  description = "producer";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init" >/dev/null

# ---- consumer (depends on producer) ----
mk_repo consumer
cat >flake.nix <<FLAKE
{
  description = "consumer";
  inputs = {
    producer.url = "file://${WSROOT}/producer";
    producer.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init" >/dev/null

# ---- extra (independent) ----
mk_repo extra
cat >flake.nix <<'FLAKE'
{
  description = "extra";
  inputs = {};
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init" >/dev/null

# Write the real pn-workspace.toml with actual file:// URLs.
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s35"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "file://${WSROOT}/producer"

[repos.extra]
url = "file://${WSROOT}/extra"
TOML
