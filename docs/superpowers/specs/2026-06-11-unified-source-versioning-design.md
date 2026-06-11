# Unified per-source content-digest versioning

**Status:** Draft
**Date:** 2026-06-11
**Author:** phillipg (with Claude)
**Tracking:** pg2-xx4g
**Related:** ADR 0005 (mkGoBuilders factory — version contract superseded here), beads nrb-n9f / nrb-c7a (Go src-digest refactor)

## Problem

`pn workspace build` / `apply` (`darwin-rebuild build --flake ./phillipg-nix-ziprecruiter`
with each sibling repo injected via `--override-input <name> git+file://<dir>`) takes
20+ minutes during iteration when it should be well under five. A `--dry-run` of
`darwinConfigurations.phillipg-mbp-02.system` against a **clean, unchanged** tree reports
**84 derivations "will be built", 0 fetched.**

Root cause: a large, growing tier of custom artifacts embeds the **repo git revision** in
its derivation inputs:

- `lib/bash-builders.nix` bakes `GIT_HASH="${gitHash}"` into the build phase of every
  `mkBashScript` (~70 in the terminal repo, ~161 across repos).
- `lib/python-package.nix` interpolates `${gitHash}` into the package's `preBuild`.
- `gitHash = mkGitHash (self.rev or self.dirtyRev)` is an **eval-time** value that changes
  on **every commit**.

Result: committing anything in a repo changes `self.rev` → changes `gitHash` → changes the
derivation hash of every stamped artifact in that repo → they all rebuild, even when their
own source did not change. Back-to-back builds at the same commit hit cache (fast); a commit
(or a dirty→clean flip) invalidates the whole tier (slow), and the tier grows as scripts and
packages are added.

`lib/go-builders.nix` already solved this for Go (`mkGoApp`/`mkGoBinary`): it keys each
package's version to its **own source digest** rather than the repo rev. This spec
generalizes that pattern to **bash and python**, documents it as the standing convention, and
removes a few unrelated aggravators of slow builds.

## Goal & invariants

For every **custom** artifact (Go binary, Python app, Bash script/module), the embedded
version and the derivation identity MUST obey:

1. **Content-driven.** The version changes **iff** the artifact's own source content changes
   — whether that change is uncommitted (dirty) or committed.
2. **Isolation.** The version does **not** change because of unrelated changes elsewhere in
   the same repo, even when those changes are committed (HEAD moves). Such a no-op for this
   artifact MUST be a cache hit.
3. **Transitive.** If an artifact is built from more than one source path/input (e.g. a
   module that sources a shared library, or pulls source from another path), changing **any**
   included source changes the version **and** triggers a rebuild.
4. **Always inspectable.** `--version` always prints a full, informative string (timestamp +
   digest), including for dirty local builds.

> **Source-visibility caveat.** "source content" means files visible to the flake: tracked
> files plus untracked-but-not-`.gitignore`d files. Under `git+file://` Nix copies exactly those
> into the source tree, so editing a `.gitignore`d file does **not** change the digest (this also
> held under the old `narHash` scheme — not a regression).

These invariants are what make iterative builds cache cleanly: only genuinely-changed
artifacts rebuild.

## Design

### Version string

Embedded version (shown by `--version`) has two components:

```
YY.MM.DD.SSSSS+<srcdigest8>
```

- `YY.MM.DD.SSSSS` — **build timestamp** (UTC), computed by `date` _inside the builder_.
  It is **not** a derivation input, so it never busts cache. Informational ("when built").
  This is already how `bash-builders.nix` and `python-package.nix` compute their date.
- `<srcdigest8>` — `first8(sha256(<combined source>))`, computed at **eval** time from the
  artifact's own source set. This is the **authoritative identity** and the only eval-time
  varying component. It is the same mechanism `go-builders.nix` already uses
  (`first8(sha256 "${src}")`).

`+` keeps the string PEP 440 local-version compatible (Python). Go currently emits
`<baseVersion>-<srcdigest8>`; it will additionally surface the build timestamp for parity.

**repoversion is intentionally NOT part of the tool version.** The repo HEAD changes every
commit; embedding it in a tool build is exactly the bug above, and there is no way to bake it
without making it a build input (unlike `date`, the rev is not available inside the sandbox).
Repo-level traceability stays in the repo-meta module (below).

### Shared helper: `mkSrcDigest`

Add to `lib/version.nix` (consumed by all three builders, DRY):

```nix
# first8(sha256) over one or more source paths/inputs. Each "${s}" is the
# content-addressed store path of s, so the digest changes iff included content
# changes — and never for unrelated paths.
mkSrcDigest = srcs:                                  # path | [ path ... ]
  let list = if builtins.isList srcs then srcs else [ srcs ];
  in builtins.substring 0 8
       (builtins.hashString "sha256"
         (builtins.concatStringsSep ":" (map (s: "${s}") list)));
```

**Transitivity contract (critical):** the set of paths passed to `mkSrcDigest` MUST equal the
set of source inputs the derivation actually depends on. Version-change and rebuild-trigger
must stay in lockstep — otherwise a change could bump the version without rebuilding, or
rebuild without bumping. Builders own enforcing this (see below).

**Mechanism & caveat:** `"${s}"` resolves to `/nix/store/<hash>-<basename>`; the digest covers
both the content-derived `<hash>` and the `<basename>`. Content-change detection is therefore
sound (the hash moves with content). Under `git+file://` with a dirty tree, Nix copies the whole
working tree to `self.outPath` and `./subdir` resolves to a content-addressed path beneath it —
that is _why_ a dirty edit propagates into the digest. Consequences: renaming a source directory
churns its digest once (basename changed), and `concatStringsSep` is order-sensitive, so callers
pass a stable, ordered list.

### Per-language application

**Bash (`lib/bash-builders.nix`) — unit = each `mkBashScript`:**

- **Granularity correction (vs the original "per-module" question):** each script is _already_ its
  own `mkBashScript` derivation with its own `src = ./.` (e.g. `modules/zw/zw-start-work/`);
  `mkBashLibrary` returns `{ lib; check; description; }` and exposes **no `src`**; `mkBashModule` is
  only an aggregator and builds nothing. There is no shared "module src" to digest, so per-script is
  the natural and strictly better-isolating unit. Editing one script does **not** rebuild siblings
  unless they share a sourced library.
- **The real fix is removal:** delete `GIT_HASH="${gitHash}"` from the build phase
  (`bash-builders.nix:175`) — the only eval-time `self.rev`-derived build input. With it gone, an
  unchanged script's derivation depends only on `src` (+ deps), so a HEAD move with no source change
  is a true cache hit. (Bash/Python derive identity from `src` already; unlike Go, the digest need
  not enter the derivation hash.)
- For the `--version` string, compute
  `SRC_DIGEST = mkSrcDigest ([ src ] ++ map (l: l.lib) libraries)` — the script's own `src` plus the
  **composed-lib store path** (`l.lib`, a content-addressed `writeText`) of each sourced library.
  `l.lib` transitively embeds nested libraries' store paths, so any library change moves the digest
  (satisfies transitivity). This `SRC_DIGEST` is cosmetic — for `--version` only.
- The generated `--version` handler prints `YY.MM.DD.SSSSS+SRC_DIGEST` (date still build-time).
- `mkBashBuilders` stops threading `gitHash` into scripts. (It keeps `mkGitHash` for the repo-meta
  module only.)

**Python (`lib/python-package.nix`):**

- Drop the `gitHash` argument.
- Compute `srcDigest = mkSrcDigest src` (extended to a list when a package draws from extra
  paths) at eval time; interpolate it into `preBuild` in place of `${gitHash}`.
- `BUILD_VERSION` stays `date.SSSSS+<srcDigest>`, substituted into `pyproject.toml` /
  `__init__.py` as today.

**Go (`lib/go-builders.nix`):**

- Already digest-based — refactor the inline `first8(sha256 "${src}")` to call the shared
  `mkSrcDigest` (gains multi-path support), and extend `src` to accept a list where needed.
- Add the build-time timestamp to `--version` for cross-language parity. **This is the least
  trivial piece and needs a concrete mechanism, not hand-waving:** `buildGoModule`'s `ldflags` is an
  eval-time Nix list, so `$(date)` cannot be placed there directly (it would reach the linker
  literally). A `preBuild` must export e.g. `buildDate=$(date -u ...)` and the ldflag must reference
  that shell var (`-X <versionPath>Date=$buildDate`), which also requires each consumer's `main.go`
  to declare a **second** version var (e.g. `main.Date`). If that consumer-code change is
  undesirable, drop the Go timestamp and keep Go digest-only `--version` (still meets every
  invariant; only loses cosmetic cross-language parity). The plan decides.

**Call sites:** remove `inherit gitHash;` threading from `flake.nix` in `support-apps`,
`agent-support`, `ziprecruiter`, `personal`. Builders self-derive the version from source.

### Repo-meta module — UNCHANGED (the one legitimate repo-rev consumer)

`lib/version.nix` `mkVersion` / `mkInstallMetadata` keep using the repo HEAD
(`self.rev or self.dirtyRev`) + `lastModifiedDate`. This module **builds nothing** (a single
`writeTextFile` per repo), is cheap, and is _meant_ to bump on every repo change so `nvd`
shows repo-level attribution. It is documented as the **sole** place repo-rev belongs.

### Third-party packages — UNCHANGED

Third-party/upstream packages are versioned by their pinned lock and bump **only** when
`update-locks.sh` is run explicitly. The content-digest convention applies to **custom**
artifacts only. This distinction is documented so agents don't apply src-digest to vendored
deps.

## Related build-speed fixes (folded in)

These are independent of versioning but serve the same "make iteration fast" goal.

### Drop `nix fmt` from build/apply; add `pn workspace format`

`treefmt` already runs as a pre-commit hook in every repo, so the `nix fmt` step in
`build.go` / `apply.go` is redundant — and it can dirty the tree (eval-cache bust) and adds a
fixed per-build cost. Remove it from build and apply; add a `pn workspace format` verb that
fans `nix fmt` across the workspace repos (parity with the other verbs). Update pn's tests.
Removing it also helps caching directly: a mid-run `nix fmt` that reformats a tracked file
dirties the tree, flipping `needsRebuild`'s `git status` gate (`updatecache.go`) and `mkVersion`'s
dirty `narHash` — the very eval-cache bust we are fighting. (Verified: no logic in `build.go` /
`apply.go` depends on the formatting side effect.)

### Enable `auto-optimise-store`

The `/nix/store` is ~98G with no hardlink dedup (`auto-optimise-store = false`, commented
"maybe causes build failures" — never re-verified). Run a manual `nix store optimise` (done as
part of this work); if it completes cleanly, set `auto-optimise-store = true` in
`shared/nix-settings.nix` and delete the disabling comment/flag. (Capping the 67 retained
system generations is optional and noted, not forced.)

## Out of scope (separate track)

`neovim-0.12.2` building from source is a substituter cache-miss, unrelated to versioning.
Tracked separately: find a cached source (substituter/channel/overlay) or adjust
`update-locks.sh` to pin a cached build.

## Deliverables

1. `lib/version.nix`: add `mkSrcDigest`; keep `mkGitHash` / `mkVersion` for repo-meta only.
2. `lib/bash-builders.nix`, `agent-support/lib/python-package.nix`, `lib/go-builders.nix`:
   adopt `mkSrcDigest`; remove `gitHash` threading from bash/python; add build-time timestamp
   to Go.
3. Flake call-site cleanup (`inherit gitHash;` removal) in the four consuming repos.
4. pn: remove `nix fmt` from build/apply; add `pn workspace format`; tests.
5. `shared/nix-settings.nix`: enable `auto-optimise-store` (gated on the manual run succeeding).
6. **ADR 0006** "Per-source content-digest versioning for custom artifacts" — supersedes the
   version contract of ADR 0005; mark 0005's version section accordingly. (No `docs/adr/index.md`
   exists yet in `nix-repo-base`; the plan creates one covering 0000–0006.)
7. **Agent docs/instructions** so the pattern is followed going forward:
   - the bash-scripting skill (authoritative mkBash\* reference),
   - relevant `CLAUDE.md` files,
   - inline doc-comments in the three builders.

## Migration & rollout

- Land the helper + builder changes in `nix-repo-base` first; then the consuming repos pick it
  up via their `--override-input` / lock.
- First build after the change rebuilds the affected artifacts once (new version scheme), then
  caches stably thereafter.
- Verify with a `--dry-run` of the system closure. First **enumerate the 84 derivations** from the
  current dry-run and bucket each as bash-script / man-page / python / go / repo-meta (man pages
  depend on `${script}`, so they fix transitively) — making the "0 after" claim falsifiable rather
  than assumed. Then confirm: an unchanged tree reports **0** custom-artifact rebuilds; a
  single-script edit rebuilds **only** that script (plus any sibling sharing a sourced library, plus
  its man page); an unrelated commit rebuilds **only** the 5 repo-meta files.

## Risks & trade-offs

- **`--version` timestamp is build-time, not commit-time**, and is frozen at first build (it
  re-stamps only on rebuild). The digest — not the timestamp — is the authoritative identity;
  this is acceptable and documented.
- **Granularity:** the unit is per-script (each `mkBashScript`), so editing one script does **not**
  rebuild siblings unless they share a sourced library (in which case all consumers of that library
  correctly rebuild). Finer isolation than the "module" granularity originally discussed — and it
  matches what the code already does.
- **Outputs become non-reproducible in the timestamp byte only.** Derivation identity stays
  content-addressed, so caching/substitution are unaffected.
- **Non-git / `path:` sources** (used by some override paths) have no `rev`/`dirtyRev`; after this
  change bash/python no longer call `mkGitHash` at all, and `mkVersion` (repo-meta) already handles
  that case via its existing dirty-`narHash` branch — unaffected.
- **No version-string parsers exist** in consumers (Go/bash/python only print or substitute the raw
  string), so changing the format is safe.
