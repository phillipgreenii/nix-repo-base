# Purpose: Shared Nix Infrastructure Repository

**Status**: Accepted
**Date**: 2026-04-28
**Deciders**: Phillip Green II

## Context

Multiple personal and work Nix configuration repos (`phillipgreenii-nix-personal`,
`phillipgreenii-nix-support-apps`, `phillipg-nix-ziprecruiter`) shared common
infrastructure — bash-builders, dev-env helpers, module helpers, and CI workflows —
that lived exclusively in `phillipgreenii-nix-personal`.

This created two problems:

1. **Wrong dependency direction**: ZR and support-apps config repos imported
   `phillipgreenii-nix-personal` solely to access infrastructure, coupling
   work config to personal config.

2. **Filesystem path hack**: `update-locks.sh` scripts in ZR and support-apps
   sourced `update-locks-lib.bash` via a relative filesystem path
   (`../phillipgreenii-nix-personal/lib/scripts/`) rather than through a proper
   flake input — fragile and breaks outside the co-located workspace.

## Decision

Extract shared infrastructure into this dedicated public repository
`phillipgreenii/nix-repo-base`.

- `lib/bash-builders.nix` and `lib/version.nix` provide the core Nix lib functions
- `lib/scripts/` holds `update-locks-lib.bash` and `update-cache-lib.bash`
- `nix/dev-env.nix` provides `mkTreefmtConfig`, `mkPreCommitHooks`, `mkDevShell`
- `nix/module-helpers.nix` provides `mkProgramModule` and related helpers
- `nix/packages.nix` provides `mkManPage` and re-exports `mkBashBuilders`
- `nix/checks.nix` provides reusable check derivations
- The `lib` flake output exposes all 14 functions for downstream consumption
- Overlay factories (`mkUnstableOverlay`, `mkLlmAgentsOverlay`,
  `mkVscodeExtensionsOverlay`) are co-located here since they are consumed by ZR

## Consequences

### Positive

- ZR and support-apps no longer depend on `phillipgreenii-nix-personal` for infrastructure
- `update-locks-lib.bash` is consumed via flake input, eliminating the filesystem hack
- Single canonical location for shared Nix infrastructure
- Infrastructure versioned and tested independently

### Negative

- One more repo in the workspace; one more step in the flake update sequence
- Changes to shared infrastructure require a PR here then a flake update in consumers

## Related Decisions

See also: phillipgreenii-nix-personal docs/adr/0000-use-architecture-decision-records.md
See also: phillipg-nix-ziprecruiter docs/adr/0013-update-sequence-np-then-sa-then-zr-via-flakeprojects-order.md
