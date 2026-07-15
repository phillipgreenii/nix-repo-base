# ADR-0021: `subPackages` scopes the gomod2nix check hook — gate tests with `mkGoTest`

**Date:** 2026-07-15
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

Go packages in these repos build through `mkGoApp` / `mkGoBinary` over gomod2nix's
`buildGoApplication` (ADR [0008](0008-adopt-gomod2nix-for-go-packages.md)). Those builders
accept `subPackages = [ "cmd/<name>" ]` to pin the shipped `bin/` to specific entrypoints —
correct and expected when a module carries more than one `cmd/*` main (e.g. `pn` ships only
`cmd/pn`, not `cmd/pn-workspace-toml-enforce`).

The footgun: **`subPackages` also scopes gomod2nix's _check_ hook**, so a package whose only
test gate is its own build check silently runs `go test` in _just that one subpackage_ and
skips the rest of the module. Verified against the locked gomod2nix builder
(`nix-community/gomod2nix@1201ddd`):

- `builder/hooks/go-config-hook.sh:37` `getGoDirs()` — when `$subPackages` is set it echoes
  **only** those import paths; otherwise it `find`s every `*_test.go` directory in the module.
- `builder/hooks/go-check-hook.sh` `goCheckHook()` — `for pkg in $(getGoDirs test); do
buildGoDir test "$pkg"; done`.

Net: with `subPackages = [ "cmd/x" ]`, the check compiles the whole module but **executes tests
only in `cmd/x`**. The `internal/*` and `pkg/*` suites — usually the bulk of the coverage — are
never run under `nix flake check` or CI. Such a gate is a **placebo**: it can stay green while
the real tests are red.

This recurred independently across the fleet, always with the same signature (scoped package
build used _as_ the test gate):

- **base** — `pn-go-tests`/`pjira-go-tests` were `callPackage` of the `subPackages=[cmd/pn]`
  module; the ~21.5k-line `internal/*` suite never ran (bead pg2-2jqj0). A real gate would have
  caught a regression that sat green for weeks.
- **personal** — `sb` tested only `cmd/sb`; 16 of 19 suites skipped in the nix build (pg2-ab6k7).
- **agent-support** — ceta / pb / pg-pr / pa-monitor all scoped (pg2-adhga).

A module that sets **no** `subPackages` does get full-module coverage from its own check phase —
but only by luck: adding a second `cmd/*` main, or ever pinning `subPackages`, silently re-empties
the gate with no error and no diff to the test files.

## Decision

The following apply to every Go module built through `mkGoApp` / `mkGoBinary`:

- A Go module's test gate under `nix flake check` **MUST NOT** be the `subPackages`-scoped
  package/binary build's own check phase.
- Each Go module with tests outside its shipped entrypoint **MUST** have a dedicated test check
  that runs `go test ./...` **unscoped** over the whole module. That check **SHOULD** be
  `goBuilders.mkGoTest` (base `lib/go-builders.nix`), which runs the full suite offline in
  gomod2nix's vendored dependency env and **deliberately sets no `subPackages`**.
- The package/binary derivation **MAY** (and typically **SHOULD**) keep
  `subPackages = [ "cmd/x" ]` to pin the shipped `bin/`. That pin is correct and **orthogonal**
  to test coverage: `subPackages` controls what _ships_; `mkGoTest` controls what is _tested_.
  The fix is to _add_ a test check, **NOT** to drop `subPackages` from the build.
- A module that currently sets no `subPackages` (and so is gated by luck) **SHOULD** still adopt
  a dedicated `mkGoTest` check as a recurrence guard, so a later second `cmd/*` main cannot
  silently empty the gate.
- Where a consumer genuinely cannot reach base's `mkGoTest` (e.g. an isolated worktree that
  cannot edit base), it **MAY** use the sanctioned fallback: a `mkGoApp` check derivation built
  over the whole module **without** `subPackages`, so gomod2nix's check hook runs every test
  directory. This is coverage-equivalent but does **NOT** converge on one builder; migrating to
  `mkGoTest` is preferred once base is reachable.
- `mkGoTest` **MUST** strip `-trimpath` from `GOFLAGS` in its `buildPhase` (mirroring gomod2nix's
  own `goCheckHook`), so tests that resolve assets via `runtime.Caller` / source-relative paths
  behave like a developer's `go test ./...`.

`mkGoTest` runs `go vet` (the `go test` default) — intentionally stricter than gomod2nix's
`-vet=off` check hook, matching the developer command whose red state is the proof above.

## Consequences

### Positive

- The test gate is **real** and independent of the package's shipping pin; the placebo class is
  closed at the builder layer.
- **One builder** for the whole fleet: `mkGoTest` auto-exports via `lib.mkGoBuilders`, so
  consumers reach it as `lib.mkGoBuilders {…}.mkGoTest` with no per-repo re-implementation.
- Adding a check (rather than dropping `subPackages`) keeps the shipped `bin/` unchanged, so the
  fix has no effect on package outputs.

### Negative / Neutral

- The gate becoming real can turn CI red on first adoption — that is the point (it surfaces the
  tests that were silently skipped). Sandbox-hostile tests (spawning daemons, binding sockets)
  must be `build`-tag/`t.Skip`-guarded rather than weakening the whole gate.
- Convergence is incremental: base (`pn`, `pjira`) adopted `mkGoTest` (pg2-2jqj0); cross-repo
  adoption and fallback→`mkGoTest` convergence are tracked in consumer beads, not this ADR.

## Alternatives Considered

- **`overrideAttrs { subPackages = ""; }` on the package derivation.** Rejected: couples test
  coverage to the shipping build, is non-obvious, and is not reusable across modules.
- **Drop `subPackages` from the package entirely.** Rejected: the shipped `bin/` would then
  carry every `cmd/*` main, and coverage would still be fragile (a later `subPackages` pin
  re-empties it).
- **A pre-commit `go test` hook instead of a flake check.** Rejected: it does not run in the
  sandboxed `nix flake check` gate / CI and needs network to resolve deps, unlike the offline
  vendored `mkGoTest`.

## Related Decisions

- ADR [0008](0008-adopt-gomod2nix-for-go-packages.md) — adopt gomod2nix (`mkGoApp`/`mkGoBinary`);
  the engine whose check hook exhibits this scoping.
- ADR [0006](0006-source-content-digest-versioning.md) — `mkGoTest` versions from the per-source
  content digest, like the other builders.
- ADR [0005](0005-mkGoBuilders-factory.md) — the `mkGoBuilders` factory that `mkGoTest` extends.
