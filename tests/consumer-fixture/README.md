# consumer-fixture

A minimal synthetic consumer of `phillipgreenii-nix-base` used to validate the
exported `flakeModules.*` API.

## Purpose

This fixture imports every flakeModule exported by the base flake and verifies
that the full module composition evaluates cleanly — no missing options, no
duplicate `follows`, no broken overlay wiring.

The parent flake (`../../flake.nix`) contributes a `consumer-fixture-eval`
check that:

1. Confirms both `flake.nix` and `flake.lock` exist in this directory.
2. Verifies the lock declares each heavy input (`nixpkgs-unstable`, `llm-agents`,
   `flox`, `nix-vscode-extensions`) as a top-level node — confirming that the
   consumer has correctly followed the heavy-upstream overlay module contract.

## Heavy-input contract

Modules in `flakeModules.{unstable,llm-agents,vscode-extensions,flox}-overlay`
read their respective inputs at consumer eval time. The consumer **must** declare
those inputs and apply `follows` so they resolve to a single lock node. Missing
`follows` causes duplicated `_N`-suffixed nodes, which the
`consumer-input-alignment` check flags as an error.

## Updating the lock

When base flake inputs change, regenerate this lock:

```
cd tests/consumer-fixture
nix flake lock --update-input phillipgreenii-nix-base
```
