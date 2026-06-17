# Adopt `gomod2nix` for Go packages (`mkGoApp`/`mkGoBinary`)

**Status**: Accepted
**Date**: 2026-06-17
**Deciders**: Phillip Green II

## Context

ADR [0007](0007-local-replace-go-modules-overlay.md) kept first-party local-replace Go modules
"live" with an overlay hack inside `mkGoApp`: strip the replaced module out of the
`vendorHash`-pinned `-go-modules` FOD (`overrideModAttrs.postBuild`), then re-overlay the live
source into `vendor/` (`postConfigure` + `chmod`). It worked, but it is non-obvious, couples to
`buildGoModule` internals, and only addresses _first-party_ drift — third-party deps still need
`vendorHash` bumps.

After ADR 0007 we evaluated `gomod2nix` more seriously, with an empirical spike rather than
precedent. The spike (on `ccpool`, our leanest local-replace app: `modernc.org/sqlite` + `replace
github.com/phillipgreenii/claude-transcript => ../claude-transcript`) established, on
aarch64-darwin:

1. **The local replace is handled natively.** `gomod2nix generate` does **not** record the local
   replace in `gomod2nix.toml`; at build time `buildGoApplication` symlinks the replaced module
   from source (`ln -s ${pwd}/../claude-transcript vendor/<importpath>`). No FOD, no hash, no
   overlay. **This is exactly what ADR 0007's hack simulates — but built in.**
2. **Live edits need no re-pin.** Editing `claude-transcript` and rebuilding **without**
   regenerating `gomod2nix.toml` recompiled it cleanly — first-party edits are source-path reads,
   not frozen artifacts. (Verified end-to-end, exit 0.)
3. **Dirty trees work** like `buildGoModule` under flakes (tracked-uncommitted edits are picked
   up; no commit needed). The one flake rule: `gomod2nix.toml` must be **git-tracked**.
4. **The full-rebuild class is structurally gone.** There is no monolithic `-go-modules` FOD.
   Each dependency is its own per-module FOD, content-addressed by `(module, version, NAR hash)`
   from the toml, shared across packages **and** repos, and **independent of the repo's git rev or
   version string**. The ADR-0006 failure (version threading renaming the FOD → re-vendor + rebuild
   of every Go package) cannot recur through the dependency layer.
5. **The `gomod2nix.toml` location is standard.** `buildGoApplication` defaults
   `modules ? pwd + "/gomod2nix.toml"`, and `gomod2nix generate` writes it next to `go.mod` — so once
   `mkGoApp` derives `pwd` correctly (see Decision), a consumer never names the toml path.

The spike used `src = <packages/>`, `modRoot = "ccpool"`, `pwd = "${src}/ccpool"`, and an explicit
`modules = "${pwd}/gomod2nix.toml"` — i.e. the subdir-`pwd` shape this ADR standardizes was the one
actually built (exit 0), not just inferred.

Maintenance health (GitHub, observed 2026-06-17): **not dying.** Not archived; last push
2026-05-04; ~35 commits/52wks; releases revived (v1.6.0 2024-10, v1.7.0 2025-08 after a 2022 lull);
recent substantive work (Dec 2025 build-hook refactor + GOCACHE reuse; Feb 2026 cross-compile
fixes). Maintainers include **Jörg Thalheim (Mic92)**, a core nixpkgs contributor. The real risk is
**low velocity / bus factor**, not abandonment — and the codebase is small enough to patch
ourselves if needed.

This reverses ADR 0007's "reject gomod2nix for now": the overlay's only advantage was staying on
stock `buildGoModule`, and the spike shows gomod2nix solves the same problem more cleanly while
_also_ removing third-party `vendorHash` drift and improving cross-package caching.

## Decision

**Adopt `gomod2nix`/`buildGoApplication` as the standard Go builder for the `mkGoApp` /
`mkGoBinary` family.** Concretely:

1. **Rewrite `mkGoApp` (and therefore `mkGoBinary`) in `phillipg-nix-repo-base/lib/go-builders.nix`
   to wrap `buildGoApplication` instead of `buildGoModule`.** Required behavior of the rewrite:
   - Preserve the ADR [0006](0006-source-content-digest-versioning.md) per-source-digest `version`
     string and `ldflags`/`versionPath` injection.
   - **Derive `pwd` from `modRoot`:** `pwd = if modRoot != null then src + "/" + modRoot else src`,
     and pass `modules = pwd + "/gomod2nix.toml"` **explicitly** (do not lean on the default when
     `src ≠ pwd`). This is the load-bearing piece: the local-replace symlink and the toml both
     resolve relative to `pwd`. The subdir-`pwd` shape is the one the spike built; the rewrite must
     reproduce it.
   - **Remove `vendorHash`, `localReplaceModules`, and the `overrideModAttrs` FOD-name pin** from the
     `mkGoApp` signature — all obsolete under gomod2nix. **This is a breaking signature change:**
     every consumer currently passes `vendorHash` (and three pass `localReplaceModules`), so the
     wrapper rewrite and the consumer edits MUST land in the same atomic change or evaluation fails
     on an unknown argument.
   - **Pin the Go toolchain** to our nixpkgs Go (pass `go = pkgs.go`) so it matches the rest of the
     fleet rather than gomod2nix's own pin.
2. **Each Go package commits a `gomod2nix.toml`** beside its `go.mod`, generated by `gomod2nix
generate` and refreshed on dependency changes. (No-dependency packages still commit a near-empty
   toml.)
3. **Scope = the `mkGoApp`/`mkGoBinary` family**, because we are rewriting that wrapper — _not_
   because every member has drift pain. The 8 in-scope packages: `pa-monitor`, `ccpool`, `pr-pool`,
   `pg-pr`, `pa-monitor-decorator-gc`, `claude-extended-tool-approver` (agent-support),
   `activity-collector` (support-apps), and `pn` (repo-base; `nix/packages.nix` wires it). **Only
   `pa-monitor`, `ccpool`, and `pr-pool` actually have a local replace** (`../claude-transcript`) and
   exercise the rooted-fileset/`modRoot` path; the other five are single-module-at-root packages
   whose benefit is purely the elimination of third-party `vendorHash` churn. Plain `buildGoModule`
   packages on the raw builder (`beads`, `statusBar`, `goccc`, `gascity`) are **out of scope** — same
   third-party-churn benefit, but migrating them is optional consistency work, not part of the
   wrapper rewrite.
4. **Cross-repo replaces are out of scope here** — `pg-pr-zr` (bead `pg2-wtjz`) needs the producing
   flake to expose source first; tracked separately.

### The pattern (canonical reference — agents MUST follow this)

There are two shapes. Use the simpler one unless the package has a local `replace => ../sibling`.

**Case A — single module at the package root (the common case: `pg-pr`, `claude-extended-tool-approver`, `activity-collector`, `pa-monitor-decorator-gc`, `pn`, …):**

```nix
# packages/<name>/default.nix
{ lib, mkGoApp, ... }:
mkGoApp {
  pname = "<name>";
  src = lib.cleanSource ./.;     # go.mod + gomod2nix.toml are at the root
  subPackages = [ "cmd/<name>" ];
  # NO modRoot. NO vendorHash. NO localReplaceModules. mkGoApp sets pwd = src.
}
```

**Case B — a local `replace => ../sibling` (only `pa-monitor`, `ccpool`, `pr-pool` today):**

```nix
# packages/<name>/default.nix
{ lib, mkGoApp, ... }:
mkGoApp {
  pname = "<name>";
  # Root src at the PARENT so the sibling is inside ONE store tree.
  src = lib.fileset.toSource {
    root = ./..;                                       # packages/
    fileset = lib.fileset.unions [ ./. ../<sibling> ]; # e.g. ../claude-transcript
  };
  modRoot = "<name>";            # mkGoApp sets pwd = src + "/<name>"
  subPackages = [ "cmd/<name>" ];
  # NO vendorHash. NO localReplaceModules.
}
```

In both cases the committed `gomod2nix.toml` sits beside `go.mod`; `mkGoApp` derives `pwd` (= `src`
in Case A, `src + "/" + modRoot` in Case B) and passes `modules = pwd + "/gomod2nix.toml"`, so a
consumer never names the toml. The local-replace symlink in Case B resolves because `pwd` and the
sibling live in the same rooted store copy.

**How to add a new dependency / bump versions:**

```bash
cd packages/<name>
go get <module>@<version>     # or edit go.mod
go mod tidy
nix run github:nix-community/gomod2nix -- generate   # rewrites gomod2nix.toml; commit it
nix build .#<name>                                    # verify
```

No `vendorHash`, no `lib.fakeHash` dance, no `nix-update`. Just regenerate + commit the toml.

**How to edit a first-party local module (e.g. `claude-transcript`):** just edit it. No toml
regeneration, no hash bump — it is read live from source.

### Rough edges (agents MUST be aware)

- **`pwd` rooting is load-bearing.** A `../sibling` replace only resolves if `src` is rooted at the
  parent (`packages/`) and `pwd` points _into that same store copy_. `mkGoApp` does this from
  `modRoot`; if you bypass `mkGoApp`, replicate it (`src = packages/`, `pwd = packages/<mod>`), or
  the symlink dangles silently.
- **`gomod2nix.toml` must be git-tracked.** Untracked files are invisible to flake builds (incl.
  the pn-workspace `git+file` overrides). A new package will fail until the toml is `git add`-ed.
- **Cross-repo replaces don't work** (gomod2nix issue #101): a `../` that escapes the repo's store
  tree dangles. Needs the other repo's source brought into the sandbox (bead `pg2-wtjz`).
- **`go.work` is unsupported** (gomod2nix issue #98). We don't use it; do not introduce it expecting
  gomod2nix support.
- **Go toolchain comes from gomod2nix's nixpkgs** unless you pass `go = pkgs.go_1_xx`. `mkGoApp`
  should pin `go` to our nixpkgs Go so the toolchain matches the rest of the fleet.
- **Maintenance bus factor:** gomod2nix is low-velocity. Pin the input and bump deliberately; be
  prepared to carry a patch if a future Go release breaks it.

## Consequences

### Positive

- Deletes the ADR-0007 overlay hack (strip-from-FOD + overlay + chmod) and all `vendorHash` lines —
  less clever Nix to maintain.
- Eliminates the entire `vendorHash`-drift class (first-party **and** third-party): dep changes are
  a `gomod2nix generate`, not a hash hunt.
- The ADR-0006 full-rebuild hazard is closed on both halves: the _dependency_ half by gomod2nix
  (per-module content-addressed FODs, repo-version-independent, shared/cached across packages and
  repos — no monolithic vendor FOD to invalidate), and the _package-recompile_ half by the
  **retained 0006 per-source digest** (`version` changes only when the package's own `src` changes).
  Neither half is reintroduced provided the rewrite keeps the digest version (and does not thread a
  whole-repo `gitHash`).
- First-party local modules are a native path dep; live edits, dirty trees, and the pn workspace
  all "just work" with no special handling.

### Negative

- Trades hand-written Nix for committed lockfile data — one `gomod2nix.toml` per Go package (roughly
  one stanza per dependency; near-empty for no-dep packages like `pa-monitor-decorator-gc`/`pn`).
- New flake input (`gomod2nix`) in every flake that builds Go.
- Breaking `mkGoApp` signature change (drops `vendorHash`/`localReplaceModules`/`overrideModAttrs`):
  the wrapper rewrite and all consumer edits must be one atomic change or evaluation fails.
- Replaces the ADR-0035 (`nix-update`) workflow with `gomod2nix generate`; `update-locks.sh` /
  `update-deps.sh` must change accordingly (see migration plan).
- Maintenance bet on a lightly-staffed project; Go ≥1.27 support unverified.

### Neutral

- ADR 0006's per-source-digest **versioning** is retained; its **pinned-FOD-name** half becomes
  moot (no FOD to pin) — 0006 is not superseded, just partially obsoleted.
- `buildGoApplication` runs tests by default (`doCheck = true`); the wrapper keeps current
  check behavior.
- Out-of-scope plain `buildGoModule` packages remain; the fleet is briefly two-builder until/unless
  they migrate.

## Alternatives Considered

### Keep the ADR-0007 overlay

Works and ships today, but couples to `buildGoModule` internals, leaves third-party `vendorHash`
churn, and is a non-obvious mechanism. Superseded — gomod2nix subsumes its benefit natively.

### `go.work` workspace

Doesn't fix the freeze (`go work vendor` still vendors), fights `buildGoModule`, and is unsupported
by gomod2nix. Rejected (also in ADR 0007).

### Status quo `buildGoModule` + `vendorHash` + `nix-update` (support-apps ADR 0035)

The fleet standard before this ADR. Rejected for the `mkGoApp` family because it neither fixes the
local-replace freeze without a hack nor removes third-party hash churn.

## Related Decisions

- Supersedes [0007](0007-local-replace-go-modules-overlay.md).
- Retains [0006](0006-source-content-digest-versioning.md) versioning; obsoletes its FOD-name pin.
- Builds on [0005](0005-mkGoBuilders-factory.md) (`mkGoApp`/`mkGoBinary`).
- Implements findings from spike `pg2-gjzz`; migration tracked in the
  `2026-06-17-gomod2nix-migration` plan.
- Cross-repo follow-up: `pg2-wtjz` (`pg-pr-zr`).
- See also: phillipgreenii-nix-support-apps docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md
  (its `nix-update` decision is superseded for the `mkGoApp` family by this ADR; beads `pg2-sz8f`,
  `pg2-eg1c`, `pg2-b9pb`, `pg2-o0jd`).
