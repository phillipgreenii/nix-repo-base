# 0006 — Per-source content-digest versioning for custom artifacts

**Status:** Accepted (amended by [ADR-0011](0011-source-digest-in-derivation-version.md))
**Date:** 2026-06-11
**Deciders:** phillipg (with Claude)
**Tracking:** pg2-xx4g
**Design:** docs/superpowers/specs/2026-06-11-unified-source-versioning-design.md

## Context

Custom artifacts (Bash scripts, Python apps, Go binaries) across the `nix-*` repos embed a
version string for `--version` and `nvd` attribution. The original convention derived that
version from the **repo git revision**: `mkBashBuilders` baked `GIT_HASH="${gitHash}"` into
every script's build phase, `mkPythonPackage` interpolated `${gitHash}` into `preBuild`, and
`gitHash = mkGitHash (self.rev or self.dirtyRev or null)`.

Because `self.rev` changes on **every commit**, the derivation hash of every stamped artifact
changed on every commit — so committing anything in a repo rebuilt the entire stamped tier,
even artifacts whose own source was untouched. Measured: a `--dry-run` of the darwin system
closure against a clean, unchanged tree reported **84 derivations to build, 0 fetched**, and
real `pn workspace build` / `apply` runs reached 20+ minutes during normal iteration. The
tier grows as scripts/packages are added, so the problem worsens over time.

`lib/go-builders.nix` had already fixed this for Go (beads nrb-n9f / nrb-c7a): `mkGoApp` keys
each package's version to its **own `src` digest** and pins the vendor FOD name, so editing
one package — or committing an unrelated change — never rebuilds it. That refactor superseded
the `mkVersion self` version contract described in ADR 0005, which is now stale.

## Decision

**A custom artifact's version is a function of its own source content, never the repo HEAD.**

The embedded version (shown by `--version`) is:

```
YY.MM.DD.SSSSS+<srcdigest8>
```

- `YY.MM.DD.SSSSS` — build timestamp (UTC), computed by `date` **inside the builder**. Not a
  derivation input; never busts cache. Informational only.
- `<srcdigest8>` — `first8(sha256(<combined source>))`, computed at **eval** time from the
  artifact's own source set. The authoritative identity; the only eval-time varying component.

A new shared helper `phillipgreenii-nix-base.lib.mkSrcDigest` (path or list of paths) will produce
the digest, consumed by all three builders:

- **Bash** (`mkBashBuilders`): unit = **each `mkBashScript`** (per-script — each script is already
  its own derivation; `mkBashLibrary` exposes no `src`, and `mkBashModule` only aggregates). The fix
  proper is _removing_ `GIT_HASH` from the build phase; the `--version` digest covers the script's
  `src` plus each sourced library's composed-lib path (`l.lib`). `gitHash` is no longer threaded
  into scripts.
- **Python** (`mkPythonPackage`): drops the `gitHash` argument; digest derived from `src`.
- **Go** (`mkGoApp`/`mkGoBinary`): already digest-based; to be refactored onto the shared helper and
  to additionally surface the build timestamp for parity (mechanism per the spec).

**Transitivity contract:** the set of paths passed to `mkSrcDigest` MUST equal the set of
source inputs the derivation depends on, so a version bump and a rebuild always happen
together.

**Two explicit exceptions:**

1. **Repo-meta module** (`mkVersion` / `mkInstallMetadata`) continues to use the repo HEAD
   (`self.rev or self.dirtyRev or null`) + `lastModifiedDate`. It builds nothing (one `writeTextFile`
   per repo), is cheap, and is _intended_ to bump on every repo change for repo-level `nvd`
   attribution. This is the **sole** legitimate consumer of the repo rev.
2. **Third-party packages** are versioned by their pinned lock and bump **only** when
   `update-locks.sh` is run. The content-digest convention applies to **custom** artifacts only.

## Consequences

### Positive

- A custom artifact rebuilds **iff** its own source changes (committed or dirty). Unrelated
  commits in the same repo are cache hits. Iterative `pn workspace build`/`apply` caches
  cleanly; the 84-rebuild-on-every-commit tier collapses to only what actually changed.
- `--version` still always carries a timestamp and a content digest, including for dirty local
  builds. The digest lets you verify "this binary was built from exactly this source."
- `nvd` per-artifact attribution is preserved via the digest; repo-level attribution via the
  repo-meta module.

### Negative

- The `--version` timestamp is **build time, not commit time**, and is frozen at first build
  (re-stamps only on rebuild). The digest, not the timestamp, is the identity of record.
- **Granularity:** per-script (each `mkBashScript`). Editing one script does **not** rebuild
  siblings unless they share a sourced library. (This corrects the original "per-module" framing,
  which did not match the code.)
- Outputs are non-reproducible in the timestamp byte only; derivation identity stays
  content-addressed, so caching and substitution are unaffected.

### Neutral

- The repo-meta module still produces 5 cheap new store paths per commit (one per repo) — by
  design, and the desired repo-level `nvd` signal.

## Alternatives Considered

- **Keep `gitHash`, but make it constant for locally-overridden builds.** Loses per-edit
  attribution locally and still rebuilds the whole tier on the first build after each commit
  in CI. Rejected — content digest is strictly better and is what Go already ships.
- **Include `repoversion` in `--version`.** The repo HEAD cannot be baked without becoming a
  build input (unlike `date`, the rev is not available inside the sandbox), which reintroduces
  the per-commit rebuild. Runtime injection (env var / metadata file) or a cheap wrapper layer
  could surface it, but both add coupling or churn for little benefit. Rejected; repoversion
  stays in the repo-meta module only.

## Related Decisions

- Supersedes the **version contract** of ADR 0005 (mkGoBuilders factory). The `mkGoBinary`
  factory itself stands; its "compute version via `mkVersion self`, reject `dev`" contract is
  replaced by the per-source digest computed inside `mkGoApp`.
- Implements beads nrb-n9f / nrb-c7a (Go src-digest refactor) as a cross-language convention.
- See also: phillipg-nix-ziprecruiter docs/adr/0044-reduce-darwin-rebuild-build-time.md —
  complementary build-time work that removes uncached local-compile derivations (ollama,
  contained-claude, serena). This ADR targets a different axis: per-commit rebuild churn.
