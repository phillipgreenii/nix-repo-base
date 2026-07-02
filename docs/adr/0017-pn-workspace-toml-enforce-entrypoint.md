# A separate `pn-workspace-toml-enforce` entrypoint for the two nix-owned keys

**Status**: Accepted
**Date**: 2026-07-02
**Deciders**: Phillip Green II (implemented by Claude, bead pg2-k43p.6)

## Context

`pn-workspace.toml` is a real, writable file at the workspace root. `pn` owns most
of it — `[repos.*]`, `workspace.terminal`, the command templates — and rewrites it
via `pn workspace init` / `pn workspace doctor --fix`. Two keys, however, express a
machine's committed intent rather than pn's repo discovery:

- `[workspace].id` — the stable, machine-invariant `wsid` consumed by pn:applied
  gates (`pn workspace info` surfaces it). It is deliberately NOT
  `networking.hostName`.
- `[hooks.apply].post` — the apply post-hook (e.g. `pb gate check`).

The pn:applied-gates spec (bead pg2-k43p) wanted these produced from committed nix
source, not a hand-edit. A downstream consumer (`phillipg-nix-ziprecruiter`) needs
to enforce them declaratively at home-manager activation.

The critique of the design established that no packaged CLI (`yq-go`, `tq`, `taplo`,
`dasel`) can do a surgical, format-preserving in-place TOML set that stays
byte-compatible with pn's own writer. Reimplementing a TOML writer would risk
fighting pn's `pn workspace init` / `doctor --fix` output.

## Decision

Add a small, SEPARATE Go entrypoint `cmd/pn-workspace-toml-enforce` to the existing
`modules/pn` Go module, exposed as a repo-base flake package
(`packages.pn-workspace-toml-enforce`) and surfaced through `overlays.default`
alongside `pn`.

It REUSES pn's own serialization — `internal/workspace.ParseConfig`, the
`orderedConfig` struct, and an atomic tempfile+rename writer — via a new exported
function `workspace.EnforceKeys(path, id, applyPost)`. `EnforceKeys`:

- Is a no-op when the file is absent (pn owns creation via `pn workspace init`).
- Sets ONLY `Workspace.Id` and `Hooks["apply"].Post = [applyPost]` (create-if-missing
  for the hooks table), preserving `[repos.*]` and everything else verbatim.
- Writes only when a value differs (idempotent), atomically, preserving file mode.
- Rejects a non-slug id (`^[a-z0-9][a-z0-9-]*$`).

Because it round-trips through the SAME `go-toml/v2` (v2.3.1) writer pn uses, its
output is byte-identical to `pn init` / `doctor --fix` — nix-driven enforcement and
pn's own commands never fight over format.

`pn` itself stays parse-and-surface-only for these keys; it does NOT gain a
user-facing `pn workspace` verb that would imply pn owns the id. To keep the `pn`
derivation's `bin/` to just `pn` (the module now has two `cmd/*` mains),
`mkGoBinary` gained an optional `subPackages` parameter and both packages pin it.

## Consequences

### Positive

- One serializer, one source of truth for TOML formatting; no drift between nix
  enforcement and pn's own writes.
- Narrow blast radius: only two keys are touched; the enforcer is a tiny, testable
  binary with its own Go tests.
- `mkGoBinary subPackages` is a reusable capability for any future multi-entrypoint
  Go module.

### Negative

- The enforcer and `pn` share a Go module, so a change to `internal/workspace`
  rebuilds both. Acceptable — they are intentionally coupled to the same serializer.

### Neutral

- When the enforcer must CREATE a missing `[hooks]` table, go-toml/v2 emits a
  `[hooks]` header and `pre = []` for the apply entry. This matches pn's own writer
  exactly and is only produced on a write (never on a no-op).

## Alternatives Considered

### A packaged TOML CLI (yq-go / tq / taplo / dasel) in a bash activation script

Rejected: none can do a surgical, format-preserving in-place set that stays
byte-compatible with pn's `go-toml/v2` writer.

### A user-facing `pn workspace set-id` verb

Rejected: implies pn owns the id, contradicting the parse-and-surface-only design.

## Related Decisions

- ADR 0008 — gomod2nix engine for Go packages (`mkGoApp`/`mkGoBinary`).
- ADR 0012 — pn applied-state store, workspace info API, and wsid registry.
- ADR 0014 — activation-output convention for home-manager.
- See also: phillipg-nix-ziprecruiter docs/adr/0049-nix-enforce-pn-workspace-toml-keys.md
