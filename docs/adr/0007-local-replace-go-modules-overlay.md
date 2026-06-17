# Keep first-party local-replace Go modules "live" via the `mkGoApp` overlay

**Status**: Superseded by [0008](0008-adopt-gomod2nix-for-go-packages.md)
**Date**: 2026-06-17
**Deciders**: Phillip Green II

## Context

Several Go packages share a first-party library through a local `replace => ../<mod>`
directive in `go.mod`. Today the in-repo cases are all in `phillipgreenii-nix-agent-support`:
`pa-monitor`, `ccpool`, and `pr-pool` each `replace github.com/phillipgreenii/claude-transcript
=> ../claude-transcript`.

These packages build with `mkGoApp` (ADR [0005](0005-mkGoBuilders-factory.md)), a thin wrapper
over nixpkgs `buildGoModule`. `buildGoModule`:

1. runs `go mod vendor` in a fixed-output derivation (the `-go-modules` FOD), which **copies the
   local-replace module's source into `vendor/`**;
2. is driven with `GOFLAGS=-mod=vendor`, so the main build compiles from that vendored tree, not
   from `src`.

`mkGoApp` additionally pins the FOD's **name** to a constant (`<pname>-go-modules`, via
`overrideModAttrs`) so editing source never re-vendors (ADR
[0006](0006-source-content-digest-versioning.md)). Because a FOD is content-addressed by
`(name, vendorHash)` — both constant here — its output path is independent of `src`. The intent
was sound for _third-party_ deps (they change only via a deliberate `vendorHash` bump). But a
_first-party_ local-replace module is also captured in that FOD, so:

- Editing the local module (e.g. adding `claude-transcript/apierror.go`) does **not** rebuild the
  FOD → the build silently compiles a **stale** copy. On 2026-06-17 this broke `pn workspace
apply` with `undefined: ct.ErrorKind` even though the symbol was committed.
- If the FOD is garbage-collected, it rebuilds from current `src` → a **different** hash than the
  pinned `vendorHash` → hash-mismatch failure.

Either way every first-party edit needs a manual `vendorHash` bump. This is the recurring
`go-local-replace-vendorhash` hazard noted in the repo deep-dives, and the motivation for spike
`pg2-gjzz`.

Two facts constrain the decision:

- **Fleet precedent.** `phillipgreenii-nix-support-apps` evaluated Go dependency strategy twice and
  both times chose to **stay on stock `buildGoModule`** — ADR 0027 (vendor/ + `vendorHash=null`,
  superseded) and ADR 0035 (`vendorHash="sha256-…"` refreshed with `nix-update`). Both explicitly
  **rejected `gomod2nix`** ("extra tooling, different workflow, extra flake input"). The fleet
  standard is `buildGoModule` + `vendorHash` + `nix-update`.
- **An interim fix already shipped.** `mkGoApp` gained a `localReplaceModules` option that (1)
  strips each listed module from the FOD in `overrideModAttrs.postBuild` (so the hash tracks
  _only_ third-party deps and never drifts on a local edit) and (2) overlays the live source into
  `vendor/` in `postConfigure` (so the build always compiles current local code). It is applied to
  the three consumers and verified: all build; the FOD no longer carries `claude-transcript`; a
  forced FOD rebuild after editing `claude-transcript` produced an **identical** hash.

This ADR decides whether to keep that overlay or replace it with a `go.work` workspace or a
`gomod2nix` migration.

## Decision

**Keep the `mkGoApp` overlay (`localReplaceModules`) as the fleet-standard way to consume
first-party local-replace Go modules. Do not adopt `go.work` or `gomod2nix` at this time.**

Rationale:

- The overlay closes the specific, recurring failure (first-party staleness / hash drift) while
  staying on stock `buildGoModule` + `vendorHash` — fully consistent with ADRs 0035/0006/0005. It
  is compatible with the existing `nix-update`/`update-deps.sh` flow: `nix-update` refreshes
  `vendorHash` from the (now stripped) FOD, so third-party dep bumps work unchanged.
- `go.work` does **not** solve the problem: `go work vendor` still vendors workspace modules, so
  they would still be frozen in the FOD. Its only real benefit (dropping `replace` directives for
  the plain-`go`/IDE loop) is marginal here — the `replace` directives already give a working
  local loop — and `go.work` fights `buildGoModule` (no first-class support; `-mod=vendor` is
  incompatible with multi-module workspace resolution, forcing `GOWORK=off` gymnastics).
- `gomod2nix` _would_ solve it natively (local replaces become path deps; no `vendorHash`), but it
  reverses a decision the fleet has already made twice, adds a flake input and per-package
  `gomod2nix.toml` files, and requires rewriting `mkGoApp`/`mkGoBinary` and ~10 consumers onto
  `buildGoApplication`. The marginal benefit over the overlay (also eliminating _third-party_
  `vendorHash` bumps) is not worth that cost now that the painful first-party drift is gone.

### Comparison

| Dimension                                 | Keep overlay (chosen)                       | `go.work`                              | `gomod2nix`                         |
| ----------------------------------------- | ------------------------------------------- | -------------------------------------- | ----------------------------------- |
| Fixes first-party staleness/drift         | ✅ yes                                      | ❌ no (`go work vendor` still vendors) | ✅ yes                              |
| Eliminates third-party `vendorHash` bumps | ❌ no (still `nix-update`)                  | ❌ no                                  | ✅ yes                              |
| Stays on stock `buildGoModule`            | ✅ yes                                      | ⚠️ fights it (`GOWORK=off`)            | ❌ no (`buildGoApplication`)        |
| Consistent with ADR 0035 fleet standard   | ✅ yes                                      | ⚠️ partial                             | ❌ reverses it                      |
| New flake input / committed lock files    | ✅ none                                     | ✅ none (just `go.work`)               | ❌ input + `gomod2nix.toml` per pkg |
| Migration blast radius                    | ✅ done (3 pkgs + helper)                   | 🔶 medium                              | 🔴 large (~10 pkgs, 3 repos)        |
| Improves plain-`go`/IDE multi-module loop | ➖ no change                                | ✅ yes                                 | ➖ no change                        |
| Couples to `buildGoModule` internals      | 🔶 yes (vendor-dir layout, `postConfigure`) | n/a                                    | ✅ decoupled                        |

### Triggers to revisit (toward `gomod2nix`)

Re-open this decision if any of these occur:

- A nixpkgs `buildGoModule` change breaks the overlay's assumptions (vendor-dir layout copied via
  `cp -r $goModules vendor`, the `postConfigure` hook, or `overrideModAttrs`) — caught by
  `nix flake check`/CI building these packages.
- Third-party `vendorHash` churn (independent of the local-replace fix) becomes a frequent,
  fleet-wide pain that `nix-update` no longer makes cheap.
- The number of cross-repo Go-module shares grows (see the `pg-pr-zr` follow-up below) such that a
  single dependency model with first-class path-dep support is clearly warranted.

## Consequences

### Positive

- The 2026-06-17 failure class is gone: editing a first-party local-replace module no longer
  silently compiles stale code nor forces a `vendorHash` bump. Verified end-to-end.
- No new tooling, flake inputs, or committed lock files; stays on the fleet-standard
  `buildGoModule` + `vendorHash` + `nix-update`.
- The mechanism is centralized in `mkGoApp`, opt-in via `localReplaceModules`, and fully
  backward-compatible (default `[]` reproduces prior behavior), so no other Go package is affected.

### Negative

- The overlay couples to `buildGoModule` internals (the vendor-dir layout, the `cp -r $goModules
vendor` copy, and the `postConfigure` hook). A future nixpkgs refactor toward a module-cache
  model would require updating `mkGoApp`. Mitigated by CI building these packages and by the
  fallback of reverting to plain `vendorHash` bumps.
- It is a non-obvious "strip from FOD + overlay live" mechanism a maintainer must read the
  `mkGoApp` comment (and this ADR) to understand.
- `vendorHash` is still required and still bumps when _third-party_ deps change (via `nix-update`).
  That is expected and correct, but it is not zero-maintenance the way `gomod2nix` would be.

### Neutral

- A genuinely _structural_ change to a local module — adding a new imported sub-package, which
  changes `vendor/modules.txt`'s package set — still needs a one-time `vendorHash` bump. This is
  rare and correct (the dependency surface actually changed).
- `claude-transcript`'s content still feeds each consumer's `mkSrcDigest` version string (the
  `src` fileset includes it), so a local edit still re-stamps the binary version — desirable
  attribution, unrelated to `vendorHash`.

## Alternatives Considered

### `go.work` workspace at `packages/`

Add a `go.work` listing the sibling modules; drop the `replace` directives. Pros: cleanest
plain-`go`/IDE multi-module editing. Cons: does **not** fix the nix freeze (`go work vendor` still
vendors workspace modules into the FOD); `buildGoModule` has no first-class workspace support and
`-mod=vendor` is incompatible with multi-module workspace resolution (forces `GOWORK=off`); the
local loop already works via `replace`. Rejected: high friction, doesn't solve the stated problem.

### `gomod2nix` / `buildGoApplication`

Generate a committed `gomod2nix.toml` per package and build with `buildGoApplication`; local
replaces become path deps read from `src`, eliminating `vendorHash` entirely. Pros: natively
solves the local-replace freeze **and** all third-party `vendorHash` churn; decoupled from
`buildGoModule` internals. Cons: reverses ADRs 0027 **and** 0035; adds a flake input and a
`gomod2nix.toml` per package to commit and regenerate; requires rewriting `mkGoApp`/`mkGoBinary`
and ~10 consumers across three repos onto a different builder/interface. Rejected **for now** — the
overlay already removes the painful first-party drift at a fraction of the cost. Retained as the
designated escalation if a "trigger to revisit" fires.

### Do nothing (keep bumping `vendorHash` by hand on every first-party edit)

The status quo that caused the outage. Rejected.

## Related Decisions

- Builds on [0005](0005-mkGoBuilders-factory.md) (`mkGoBuilders`/`mkGoApp`) and
  [0006](0006-source-content-digest-versioning.md) (per-source digest + pinned FOD name).
- Implements spike `pg2-gjzz`.
- Follow-up: `pg-pr-zr` (in `phillipg-nix-ziprecruiter`) is a _cross-repo_ local replace
  (`=> ../../../phillipgreenii-nix-agent-support/packages/pg-pr`) and is currently **not**
  nix-built for this exact reason. Unblocking it needs the producing flake to expose `pg-pr`'s
  source so the consumer's sandbox can include it, after which the same overlay applies. Tracked
  separately (see beads).
- See also: phillipgreenii-nix-support-apps docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md
- See also: phillipgreenii-nix-support-apps docs/adr/0027-go-vendor-directory-for-nix-reproducible-builds.md
