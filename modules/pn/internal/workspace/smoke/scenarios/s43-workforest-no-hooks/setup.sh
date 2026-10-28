#!/usr/bin/env bash
# S43: workforest add --no-hooks (bead pg2-wu21kf)
# Two real local git repos (producer, consumer), each declaring a plain
# post-clone hook that appends its cwd to $WSROOT/hooklog. command.txt adds a
# default set (with-hooks) and a --no-hooks set (no-hooks); assertS43 proves the
# hook ran in both members of the first set, in no member of the second, and that
# the two sets are otherwise identical.
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

# Write the real pn-workspace.toml with actual file:// URLs and the hooks. The
# hook is a plain shell command (no nix): it logs the directory it ran in.
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s43"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[[repos.consumer.hooks]]
when = ["post-clone"]
run = ['echo "\$PWD" >> "${WSROOT}/hooklog"']

[repos.producer]
url = "file://${WSROOT}/producer"

[[repos.producer.hooks]]
when = ["post-clone"]
run = ['echo "\$PWD" >> "${WSROOT}/hooklog"']
TOML
