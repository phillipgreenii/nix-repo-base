#!/usr/bin/env bash
# S4-github-colon: consumer flake.nix references producer via github: URL.
# Producer is config'd with github:smoke-test/producer-repo.
# Consumer uses same github: form in its flake input.
set -euo pipefail

WSROOT="$PWD"

# producer: no flake.nix needed (it's a non-local repo; only needs toml URL)
# But gatherInputURLs needs the repo dir to have a flake.nix to resolve flake_path
# We create a minimal local git repo for the consumer; producer has no local dir.

# Create consumer repo with flake.nix that references producer via github: URL
mkdir -p "$WSROOT/consumer"
cd "$WSROOT/consumer"
git init -b main
git config user.email "smoke@test.invalid"
git config user.name "smoke"
cat >flake.nix <<'FLAKE'
{
  description = "consumer";
  inputs = {
    producer.url = "github:smoke-test/producer-repo";
    producer.flake = true;
  };
  outputs = _: {};
}
FLAKE
git add flake.nix
git commit -m "init"

# Write pn-workspace.toml
cd "$WSROOT"
cat >pn-workspace.toml <<TOML
[workspace]
name = "smoke-s4-github-colon"
terminal = "consumer"

[repos.consumer]
url = "file://${WSROOT}/consumer"

[repos.producer]
url = "github:smoke-test/producer-repo"
TOML
