#!/usr/bin/env bash
# S4-git-plus-ssh: consumer uses git+ssh://git@github.com/owner/repo.git URL.
set -euo pipefail
WSROOT="$PWD"
mkdir -p "$WSROOT/consumer"
cd "$WSROOT/consumer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat > flake.nix << 'FLAKE'
{
  description = "consumer";
  inputs = {
    producer.url = "git+ssh://git@github.com/smoke-test/producer-repo.git";
    producer.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"
cd "$WSROOT"
cat > pn-workspace.toml << TOML
[workspace]
name = "smoke-s4-git-plus-ssh"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "github:smoke-test/producer-repo"
TOML
