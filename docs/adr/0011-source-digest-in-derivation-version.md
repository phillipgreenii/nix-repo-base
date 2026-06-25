# Per-source digest in the derivation `version` for bash & python

**Status**: Accepted
**Date**: 2026-06-25
**Deciders**: Phillip Green II (with Claude)
**Tracking**: pg2-rns7

## Context

ADR [0006](0006-source-content-digest-versioning.md) established that a custom artifact's
identity is a per-source content digest (`mkSrcDigest`), never the repo HEAD. It wired that
digest into the **runtime** `--version` output for bash, python, and Go.

For Go, the digest also lands in the **derivation** `version` attribute, so `nvd` (and the
darwin "Package changes" report) attributes a rebuild to the artifact whose source actually
changed. Bash and python diverged: their derivation `version` was the bare placeholder
`"0.0.0"`, with the digest surfaced only at runtime. As a result `nvd` could not distinguish
one bash/python artifact's change from another's, and the per-source signal ADR 0006 created
was invisible at the `nvd` layer for those two families.

A secondary artifact complicated the bash story: the man page was a **separate**
`pkgs.runCommand "${name}-man"` derivation built from `${script}/bin/${name}`. Even if the
script's `version` carried the digest, the man-page derivation had its own identity and would
not inherit it.

`mkSrcDigest` (`lib/version.nix`) hashes store-path **strings**, not NAR content. The digest
therefore reflects the artifact's _source set_ — its `src` plus each sourced library's
composed-lib store path. Because store paths can move on a toolchain bump (e.g. a `pkgs.writeText`
input re-realizing under a different hash), the digest MAY change even when the human-authored
source bytes did not. This nuance MUST be stated so the version contract is not over-claimed.

## Decision

This ADR **amends** ADR 0006. The eval-time `srcDigest` MUST now enter the **derivation**
`version` for the bash and python builders, matching the Go builders.

- `mkBashScript` (`lib/bash-builders.nix`) MUST set
  `version = "${baseVersion}-${srcDigest}"`, where `baseVersion` is a new argument defaulting
  to `"0.0.0"`. The runtime `--version` string (which additionally embeds the build date) is
  unchanged.
- `mkPythonPackage` (`lib/python-package.nix`, consolidated into this repo and exported as
  `lib.mkPythonBuilders`) MUST set `version = "${baseVersion}-${srcDigest}"`, with the same new
  `baseVersion ? "0.0.0"` argument. The PEP 440 wheel version (with a `+local` segment) computed
  in `preBuild` stays distinct and unchanged.
- The bash man page MUST be folded into the **script** derivation (written to
  `$out/share/man/man1/${name}.1` during `installPhase`) so it inherits the script's `version`.
  The separate `${name}-man` derivation MUST be removed, and the `manPage` attribute MUST be
  dropped from the `mkBashScript` return set. A new `manPage ? true` argument MAY disable man-page
  generation for a script whose `--help` is not help2man-parseable.

The derivation `version` for these artifacts changes when the artifact's **source set** (its
`src` plus the store paths of its sourced libraries) changes. It MUST NOT be described as
changing "iff the source changes": a toolchain bump that moves an input store path MAY change
the digest with no change to the authored source, and conversely the digest is stable across
repo commits that do not touch the source set.

## Consequences

### Positive

- `nvd` / "Package changes" now attributes a bash/python rebuild to the specific artifact whose
  source set changed, matching the Go families — the per-source signal from ADR 0006 is now
  visible at the `nvd` layer for all three languages.
- The bash man page inherits the script's version automatically; there is one fewer derivation
  per public bash script, and no second identity to keep in sync.

### Negative

- `help2man` now runs inside **every** bash script build (previously a separate derivation). The
  framework already requires `--help` for all scripts, so this is generally safe; a script whose
  `--help` is not parseable MUST set `manPage = false`.
- A toolchain bump that moves an input store path will bump the derivation `version` even with no
  authored-source change (see the `mkSrcDigest` nuance above). This is acceptable: the digest
  tracks the realized source set, which is the correct rebuild unit.

### Neutral

- `baseVersion` defaults to `"0.0.0"`, so existing call sites need no change to keep their
  current behavior; the digest suffix is additive.

## Alternatives Considered

### Keep the man page as a separate derivation, stamp it independently

Rejected: it would require threading the digest into a second derivation and keeping the two
identities consistent. Folding the man page into the script derivation gives it the script's
version for free and removes a derivation.

### Reuse the runtime `--version` string as the derivation `version`

Rejected: the runtime string embeds the build **date**, which is not an eval-time value and
would either be unavailable at eval time or reintroduce non-determinism into the derivation
identity. Only the eval-time `srcDigest` belongs in the derivation `version`.

## Related Decisions

- Amends ADR [0006](0006-source-content-digest-versioning.md) (per-source content-digest
  versioning): extends the digest from runtime `--version` into the derivation `version` for
  bash & python.
- See also: phillipgreenii-nix-support-apps — consolidates `mkPythonPackage` into this repo's
  `lib.mkPythonBuilders` factory; that flake consumes it via `callPackage` rather than a local
  copy.
