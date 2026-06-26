# pn applied-state store, workspace info API, and wsid registry

**Status**: Accepted
**Date**: 2026-06-26
**Deciders**: Phillip Green II (with Claude)
**Tracking**: pg2-k43p.2

## Context

Phase 1 of `pn:applied-gates` requires a definitive, machine-local record of "which ref was last
successfully applied to this checkout." Two pre-existing mechanisms served adjacent purposes but
were inadequate as a single source of truth:

1. **The rebuild-skip cache** — `$XDG_STATE_HOME/zn-self-upgrade/apply/applied-hash` — stored only
   a content hash for the `needsRebuild` decision. It lived under the legacy `zn-self-upgrade`
   namespace, carried no timestamp, no dirty-tree signal, and was a flat file keyed by internal
   identity rather than checkout path. Overloading it with gate semantics would conflate a build
   cache with an applied-state store.

2. **The git HEAD ref** alone — querying `git rev-parse HEAD` at gate-check time does not record
   whether `pn workspace apply` ever succeeded for that ref; a dirty tree or a mid-apply failure
   would appear clean.

The `pn workspace info` command (Task 4) must emit a stable JSON schema consumed by downstream
tools (the `pb` gate checker). That schema must include per-repo applied state. Publishing it from
a separate data store (rather than recomputing it from the build cache) keeps the API contract
independent of the rebuild-skip heuristic.

The `pn:applied` gate design also requires workspace IDs (`wsid`) to be machine-invariant
cross-session identifiers. Without enforcement, two workspaces with the same `wsid` on the same
machine would silently collide in downstream gate state, producing false positives or negatives.

## Decision

### 1. Single per-repo applied-state store

A new store is written by `markApplied` on every successful `pn workspace apply` and read by both
`needsRebuild` and `pn workspace info`. The store path is:

```
$XDG_DATA_HOME/pn-workspace/applied/<sha256(checkout-path)>
```

where `<sha256(checkout-path)>` is the hex SHA-256 of the absolute checkout path. This key is
stable across process restarts and machine reboots for a given checkout location.

Each file is a JSON object:

```json
{
  "applied_ref": "<git-ref>",
  "dirty":       <bool>,
  "applied_at":  "<RFC 3339 timestamp>"
}
```

- `applied_ref` — the output of `git rev-parse HEAD` at apply time.
- `dirty` — `true` iff `git status --porcelain` is non-empty at apply time.
- `applied_at` — UTC timestamp of the successful apply.

The legacy `$XDG_STATE_HOME/zn-self-upgrade/apply/applied-hash` store is **retired**. All callers
that previously wrote or read that path MUST use the new store. There is one source of truth.

### 2. `pn workspace info --json` stable API schema

`pn workspace info --json` MUST emit the following JSON schema:

```json
{
  "wsid":     "<string>",
  "root":     "<string>",
  "terminal": "<string>",
  "repos": [
    {
      "name":        "<string>",
      "path":        "<string>",
      "applied_ref": "<string>",
      "dirty":       <bool>
    }
  ]
}
```

- `wsid` — the `[workspace].id` slug from `pn-workspace.toml`. Empty string if not set.
- `root` — absolute path to the workspace root directory.
- `terminal` — the repo key of the terminal flake, taken from `[workspace].terminal` in
  `pn-workspace.toml`.
- `repos` — ordered by topological sort (`topoAlpha`); each entry's `applied_ref` is the empty
  string (`""`) when the repo has no applied-state record (the field is always present; never
  `null`). `dirty` is `false` when the store is absent.

This schema is a **stable consumed API**. Fields MUST NOT be removed or renamed without a
superseding ADR. New optional fields MAY be added.

### 3. Machine-local `wsids/` registry

A registry of workspace IDs is maintained under:

```
$XDG_DATA_HOME/pn-workspace/wsids/<wsid>
```

Each entry is a file whose content is the canonical path to the workspace root. On `pn workspace
apply`, if `[workspace].id` is non-empty, the tool MUST:

1. Read the registry entry for that `wsid`, if it exists.
2. If the registered path differs from the current workspace root, **abort with a non-zero exit**
   (duplicate wsid collision on the same machine).
3. Otherwise write (or overwrite) the registry entry with the current workspace root.

This prevents silent collision of gate-state between two checkouts that share a `wsid` on the same
machine. A `wsid` MUST be unique per machine at apply time.

## Consequences

### Positive

- One store for both rebuild-skip and gate-scan; `needsRebuild`, `pn workspace info`, and the `pb`
  gate checker all see the same record. No staleness between two parallel caches.
- `pn workspace info --json` exposes a versioned, stable API that downstream tools can depend on
  without parsing `pn` internals.
- The `wsids/` registry makes same-machine wsid collisions a hard error rather than silent
  misbehavior.
- The store path encodes the checkout path via SHA-256, so multiple checkouts of the same repo on
  one machine each get independent records.

### Negative

- The applied-state file is written to `$XDG_DATA_HOME`, which is machine-local and not version
  controlled. A fresh clone with no prior apply has no record; `applied_ref` will be an empty
  string until the first successful apply.
- Retiring `zn-self-upgrade/apply/applied-hash` is a one-way migration; existing upgrade-state
  from the old path is silently dropped (the next apply re-creates it at the new path). Because
  the legacy store is not migrated, the first `pn workspace apply` after this ships finds no
  record and therefore forces a one-time full rebuild of every repo (benign; subsequent applies
  skip correctly).
- The applied-state store is keyed by the repo checkout path that was applied. `pn workspace apply`
  with `OverridePaths` (e.g. a coordinated-worktree / override apply) records state under the
  override path, whereas `pn workspace info` always reads the canonical `<root>/<name>` path. So
  after an override-path apply, `pn workspace info` (and any consumer of it, e.g. Phase 2's
  `pb gate check`) reports an empty `applied_ref` for that repo. The common case — a normal apply
  from the workspace root with no overrides — is unaffected (both paths resolve identically).
  Resolving this for the override flow is deferred to Phase 2 (tracked as a follow-up bead).
- A workspace moved to a new path on disk loses its applied-state record (the SHA-256 key changes).
  Users MUST re-run `pn workspace apply` after moving a checkout.

### Neutral

- The `wsids/` registry is a flat directory of small text files; no database or locking mechanism
  is required for the single-writer (apply) / single-reader (apply startup check) pattern.

## Alternatives Considered

### Two parallel stores — keep the rebuild-skip cache, add a second applied-state store

The rebuild-skip cache could remain at the legacy path while a new store holds gate-state. This
avoids retiring the old path. Rejected: two stores diverge under error conditions (apply succeeds
at one but not the other), require double writes on success, and give callers two places to query.
The added complexity outweighs the migration simplicity.

### Reuse the rebuild-skip cache as-is (no new store)

The cache could be left at `zn-self-upgrade/apply/applied-hash` and extended in-place with the new
fields. Rejected: the legacy namespace (`zn-self-upgrade`) belongs to a retired tool; the flat-file
format has no extensible structure; and `$XDG_STATE_HOME` is the wrong XDG base for persistent
applied state (it is for non-essential runtime data, whereas `$XDG_DATA_HOME` is for portable
user data).

## Related Decisions

- Amends ADR [0002](0002-pn-workspace-toml-schema.md) (`pn-workspace.toml` schema): adds the
  `[workspace].id` field (the `wsid` source) to the documented schema.
- See also: phillipgreenii-nix-agent-support docs/adr/0018-… — the `pb` tool and `pn:applied` gate
  contract; `pb gate check` consumes the `pn workspace info --json` schema defined here.
