#!/usr/bin/env bash
# S41: foundation-ordering (ADR-0033)
# zzz is a foundation repo whose flake imports base (like phillipgreenii-x
# importing repo-base's treefmt module). It sorts last alphabetically and has an
# x->base flake edge, yet `workspace lock` MUST order it FIRST: order ==
# ["zzz","base","app"], and its flake edge stays in lock.edges.
set -euo pipefail

WSROOT="$PWD"

mk_repo() {
  local name="$1" inputs="$2"
  mkdir -p "$WSROOT/$name"
  cd "$WSROOT/$name"
  git init -b main
  git config user.email "smoke@test.invalid"
  git config user.name "smoke"
  cat >flake.nix <<FLAKE
{
  description = "$name";
  inputs = {
$inputs
  };
  outputs = _: {};
}
FLAKE
  git add flake.nix
  git commit -m "init"
  cd "$WSROOT"
}

mk_repo base ""
mk_repo zzz "    base.url = \"file://${WSROOT}/base\";
    base.flake = true;"
mk_repo app "    base.url = \"file://${WSROOT}/base\";
    base.flake = true;"

cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s41"
terminal = "app"

[repos.app]
url = "file://${WSROOT}/app"

[repos.base]
url = "file://${WSROOT}/base"

[repos.zzz]
url = "file://${WSROOT}/zzz"
foundation = true
TOML
