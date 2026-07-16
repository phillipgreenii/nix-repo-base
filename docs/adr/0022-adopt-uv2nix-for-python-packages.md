# Adopt `uv2nix` for Python packages (`mkPythonPackage`)

**Status**: Proposed (base builder + Tier-1 checks implemented and green on aarch64-darwin; flip to
**Accepted** on owner sign-off of the 3 flake inputs. Two independent reviews (ADR review
APPROVE-WITH-CHANGES; test-coverage review INSUFFICIENT → resolved by the Validation section) plus a
plan review (GO-WITH-ADJUSTMENTS) were applied. The riskiest mechanism — the version-relocation under
`mkVirtualEnv` — is **spike- and check-confirmed** on aarch64-darwin; see Spike evidence in Context and
Decision 4.)
**Date**: 2026-07-15
**Deciders**: Phillip Green II
**Bead**: `pg2-r4cfy` (follow-up/owner-decision from `pg2-gjwpl`)

## Context

Every Python artifact in the workspace routes through base's factory
`phillipgreenii-nix-base.lib.mkPythonBuilders` → `mkPythonPackage` (`lib/python-package.nix`). Two
consumers ship (both in `phillipgreenii-nix-support-apps`): `pd-schedule-manager` and
`work-activity-tracker`; `phillipgreenii-nix-agent-support` instantiates the factory but ships no
Python app.

**The defect (deepdive B20, ranked #1 highest-leverage).** `mkPythonPackage` reads only the dependency
**names** from `pyproject.toml` (`project.dependencies`, constraint stripped) and resolves each by
**name lookup in `python.pkgs`** — i.e. whatever version the pinned `nixpkgs` carries. It never reads
`uv.lock`. Meanwhile `uv.lock` **is** the maintained source of truth for dev/CI: support-apps
`update-locks.sh` runs `uv lock --upgrade` per package, and the dev/pre-commit flow runs `uv run …
mypy/pytest` against that lock. Two resolvers therefore run in parallel (acknowledged as a "Negative"
in support-apps ADR 0013):

- dev + CI + tests run against `uv.lock`-pinned versions;
- the shipped nix artifact runs against `nixpkgs`-name-matched versions.

They drift silently: a dependency can be tested at one version and shipped at another (or, for a
transitive dep only `uv.lock` knows about, tested-present and shipped-absent).

**What `pg2-gjwpl` already landed (base `82dfb7e`).** An unresolved dependency now `throw`s (behind an
`allowMissingDeps ? false` escape hatch) instead of trace-warn-and-drop, and `dontCheckRuntimeDeps =
false` re-enables the nixpkgs runtime-deps check. This closed the _silent-drop_ and
_unchecked-runtime-deps_ sub-issues but **not** the headline: the shipped closure is still
`nixpkgs`-versioned, so **version drift persists**. That residue is what this ADR resolves.

**Precedent — base ADR 0008 (gomod2nix).** Base already made this exact class of decision for Go: it
swapped `buildGoModule` + `vendorHash` for `gomod2nix`/`buildGoApplication` so the nix build reads Go's
native lock instead of guessing, killing name/hash drift. `uv2nix` is the Python analog — the build
reads `uv.lock` instead of name-matching `python.pkgs`.

**Maintenance health (observed 2026-07-15).** `pyproject-nix`/`uv2nix`/`build-system-pkgs` are authored
and actively maintained by adisbladis, a major nix-community contributor (not formally nixpkgs-core, but
active and widely adopted), and are the de-facto modern Nix+uv path. As with gomod2nix, the real risk is
bus factor / velocity, not abandonment; the code is small enough to carry a patch if needed.

### Spike evidence (aarch64-darwin, 2026-07-15)

Like ADR 0008, this decision is spike-backed rather than precedent-only. In an isolated subset
workforest with the three inputs wired into base, three throwaway fixtures proved the mechanism:

1. **Drift-equality.** A fixture pinning `six==1.16.0` in `uv.lock` (base nixpkgs carries `1.17.0`)
   built and reported `1.16.0` at runtime — the artifact ships the **lock** version, not the nixpkgs
   version. Under the name-match builder it would ship `1.17.0`.
2. **Version-stamp relocation.** Substituting the stamped version into the root project's own wheel
   build (`pyprojectOverrides → overrideAttrs.preBuild`) took cleanly: `__version__` = the literal
   stamp, `importlib.metadata` = the stamp with PEP 440 leading-zero normalization (`26.07`→`26.7`) —
   identical to what the current `buildPythonApplication` already produces, so no regression. This
   **refutes** the pre-spike worry that `uv.lock`'s recorded `0.0.0` project version would override the
   substitution.
3. **Absent-from-nixpkgs.** A fixture depending on `eventsourcing==9.4.6` (absent from nixpkgs by name)
   resolved from the lock and imported at runtime — confirming the work-activity-tracker cleanup.

The build used plain nixpkgs with **no consumer pkgs overlay** (`pyproject-nix.build.packages` via
`callPackage` from the captured input), confirming Decision 1's "no consumer input/overlay" claim.
Not yet exercised by the spike (deferred to the full Tier-3 gate): x86_64-linux, an sdist-only dep,
and the runtimeDeps/completions wrapper over the venv.

## Decision

**Adopt `uv2nix` (the `pyproject-nix` workspace loader) as the engine behind `mkPythonPackage`.** The
workspace loader is an **Adapter**: it adapts each package's `pyproject.toml` + committed `uv.lock` into
a nixpkgs Python package set, so the shipped closure is exactly the resolved-and-tested lock.

Concretely:

1. **Add three flake inputs to base only** — `pyproject-nix` (`github:pyproject-nix/pyproject.nix`),
   `uv2nix` (`github:pyproject-nix/uv2nix`), `pyproject-build-systems`
   (`github:pyproject-nix/build-system-pkgs`) — beside the `gomod2nix` block, each with `follows`-dedupe
   onto base `nixpkgs` (and intra-ecosystem `follows` chaining so the dedupe is complete). **No new
   inputs — and no pkgs overlay — in consumer flakes.** This is a **cleaner departure from gomod2nix,
   not a mirror of it**: gomod2nix _requires_ every Go-building consumer to declare `inputs.gomod2nix`
   and apply `self.overlays.gomod2nix` to its own pkgs (enforced by `mkGoBuilders`' `pkgs ?
buildGoApplication` assert; see agent-support/support-apps `flake.nix`), because `buildGoApplication`
   must live on `pkgs`. uv2nix instead consumes `pyproject-nix.build.packages` via `callPackage` from the
   input captured by base's builder (Decision 2), so nothing needs to reach consumers' `pkgs` at all —
   the three inputs appear only in base's node of the composed lock and never in a consumer flake.

2. **Thread the inputs by currying, NOT a `pkgs` overlay** (structural difference from gomod2nix, which
   IS a global overlay). The uv2nix workspace loader is _per-package_ — it needs each package's `src` —
   so it cannot be a global overlay. `lib/python-package.nix` becomes two-stage: an outer function
   capturing `{ uv2nix, pyproject-nix, pyproject-build-systems }` and an inner function keeping the
   **unchanged** `{ pkgs, lib, mkSrcDigest }` signature. `flake.nix` applies the outer set:
   `mkPythonBuilders = import ./lib/python-package.nix { inherit (inputs) uv2nix pyproject-nix
pyproject-build-systems; };`. Every `self.lib.mkPythonBuilders { pkgs; lib; mkSrcDigest; }` call site
   is untouched — including agent-support (factory-only, no app), which MUST keep evaluating.

3. **Rewrite the resolution core** to construct a lock-driven `pythonSet` and build a virtualenv from
   it. The canonical shape (exact API MUST be confirmed against the pinned inputs — see Alternatives /
   the "Rough edges" note on version fragility):

   ```nix
   workspace  = uv2nix.lib.workspace.loadWorkspace { workspaceRoot = src; };
   overlay    = workspace.mkPyprojectOverlay { sourcePreference = "wheel"; };
   pythonSet  = (pkgs.callPackage pyproject-nix.build.packages { inherit python; }).overrideScope
                  (lib.composeManyExtensions [
                    pyproject-build-systems.overlays.default
                    overlay
                    pyprojectOverrides          # ← carries the Decision-4 version-relocation preBuild
                  ]);
   venv       = pythonSet.mkVirtualEnv "${pname}-env" workspace.deps.default;
   ```

   Note `overrideScope` takes a **single** extension, hence `composeManyExtensions` over the three;
   `pyprojectOverrides` is the seam where Decision 4's version substitution lives, so items 3 and 4
   connect. Requirements:
   - Runtime dependencies MUST be resolved from the committed `uv.lock`, never by name lookup in
     `python.pkgs`.
   - An unresolvable/absent lock entry MUST fail evaluation/build (uv2nix does this by construction;
     consistent with the `pg2-gjwpl` fail-fast direction).
   - The closure MUST be **complete by construction from the lock** — the class of bug the old
     name-match builder's `dontCheckRuntimeDeps=false` guarded against (an import with no propagated
     dep) cannot occur when the venv is the resolved lock closure. Note the `buildPythonApplication`
     `dontCheckRuntimeDeps` knob no longer applies: `pyproject-nix.build.packages` derivations are not
     `buildPythonPackage`, so that nixpkgs hook likely does not run at all. This is therefore an
     invariant to _prove by a runtime import smoke_ (Validation), not a nixpkgs check to keep green.
   - The interpreter (`python = pkgs.python3`) MUST remain unchanged (interpreter skew, deepdive B22, is
     out of scope).

4. **Preserve ADR 0006/0011 versioning on both surfaces (see Consequences → Versioning).** The
   nvd-visible derivation `version` (`${baseVersion}-${srcDigest}`, i.e. `0.0.0-<digest>` under the
   default `baseVersion="0.0.0"`) MUST be stamped on the **wrapper** derivation. The runtime `--version`
   string (`YY.MM.DD.SSSSS+<srcDigest>`) MUST keep its format; its substitution moves off the wheel
   `preBuild` onto the **root package's build inside the pythonSet** (via the `pyprojectOverrides`
   overlay from Decision 3), with the digest still computed at eval from the original `src` so it stays
   pure/cacheable.

   > **SPIKE-CONFIRMED (aarch64-darwin, 2026-07-15).** The relocation works: substituting the stamp in
   > the root project's `preBuild` set both `__version__` (literal) and `importlib.metadata` (PEP 440
   > normalized) to the stamped value — `uv.lock`'s recorded `0.0.0` did **not** override it. One
   > nuance to carry into the acceptance test: `importlib.metadata` normalizes leading zeros
   > (`26.07`→`26.7`) exactly as the current builder does, so the AC2 `--version` regex MUST match
   > whichever surface the app reads — `^\d{2}\.\d{2}\.\d{2}\.\d{5}\+` for `__version__`, or `\d{1,2}`
   > per segment for `importlib.metadata`. Still to prove at Tier-3: x86_64-linux (the spike was
   > darwin-only). Botched-relocation failure mode: `--version` prints `0.0.0`.

5. **Update and rewrite the two base Python checks for the currying signature.** Both
   `lib/python-package-version-tests.nix` and `lib/python-package-resolve-tests.nix` import the builder
   directly and MUST receive the three inputs (in their own args and in `flake.nix`'s checks wiring) or
   `nix flake check` fails at eval. The resolve check is **rewritten, not deleted** (see Validation).

6. **Retain `customDeps`, `pypiToNixNameMappings`, `allowMissingDeps` in the signature as accepted
   no-ops** so base can land BEFORE consumer cleanup without an unknown-arg eval error (the arg set has
   no `...`). Their removal is a separate later bead once consumer usage is gone.

7. **Land base-first, then support-apps cleanup** (coordinated, one logical change): work-activity-tracker
   drops its hand-packaged `eventsourcing` block + `customDeps`; pd-schedule-manager drops
   `pypiToNixNameMappings`.

### The pattern (canonical reference — agents MUST follow this)

**Consumer `default.nix` (unchanged call shape):**

```nix
{ pkgs, lib, mkSrcDigest, mkPythonBuilders, ... }:
let
  pythonPackageLib = mkPythonBuilders { inherit pkgs lib mkSrcDigest; };
in
pythonPackageLib.mkPythonPackage {
  name = "<name>";
  # The committed uv.lock in this dir is LOAD-BEARING: it drives the shipped
  # closure. Do NOT delete/gitignore it or exclude it from src.
  src = ./.;
  runtimeDeps = [ /* external tools onto PATH */ ];
  versionPlaceholder = "0.0.0";
  versionInitFile = "src/<pkg>/__init__.py";
  hasCompletions = true;
  hasTldr = true;
}
```

No `customDeps`, no `fetchPypi` hand-packaging, no `pypiToNixNameMappings`, no name-match guessing.

**How to add / bump a dependency** (contrast gomod2nix's `generate` — there is NO generate step here):

```bash
cd packages/<name>
uv add <dep>          # or edit pyproject.toml, then `uv lock`
# commit uv.lock  ← the nix build consumes THIS
nix build .#<name>    # ships exactly the locked closure
```

### Rough edges (agents MUST be aware)

- **`uv.lock` must be git-tracked and present at the workspace root.** `loadWorkspace` requires it;
  flake builds ignore untracked files.
- **Wheel vs sdist.** `sourcePreference = "wheel"` may lack a wheel for a target → per-package override
  to `"sdist"`. A Tier-3 build on BOTH `x86_64-linux` and `aarch64-darwin` is the guard (no Go analog —
  Go compiles from source uniformly).
- **Version stamping is validation-sensitive** (see Decision 4 / Consequences → Versioning).
- **Three inputs, not one.** They enter every downstream lock transitively via base; MUST
  `follows`-dedupe; propagate with `pn --override-input`, never local-path URLs.
- **Bus factor.** Single-maintainer ecosystem; pin the inputs and bump deliberately via
  `update-locks.sh`'s existing `nix flake update`.

## Validation

A build passing does **not** prove the drift is closed: `python-version-digest` (digit/hyphen check),
an absent-dep import test, and a naive `--version` exit-0 smoke can all stay green while the builder
silently resolves a nixpkgs-present dependency from its incidental nixpkgs version. The acceptance bar
is therefore a **positive drift-equality proof**, plus fail-loud and version-surface coverage. Full
per-tier checklist lives on bead `pg2-r4cfy`; the load-bearing items:

- **Drift-equality (the headline proof).** A fixture `lib/fixtures/py-lock-pin/` whose `uv.lock` pins a
  **real, nixpkgs-present, pure-Python** dep (e.g. `certifi`) to a version **different from**
  `pkgs.python3.pkgs.<dep>.version`; the artifact MUST report the **lock** version, and the check MUST
  also assert `pinned != nixpkgs version` so it fails loudly ("re-pin") if nixpkgs ever converges onto
  the pin rather than silently ceasing to discriminate. RED under the old name-match builder, GREEN
  under uv2nix — this is the single test that proves D1.
- **Absent-from-nixpkgs, lock-driven (positive).** A fixture depending on a **real** PyPI package absent
  from nixpkgs by name (`eventsourcing`, the actual work-activity-tracker dep) with its real committed
  `uv.lock`; build and assert `import eventsourcing` succeeds. NB the current `py-missing-dep` fixture
  depends on a non-existent name (`this-package-is-not-in-nixpkgs-xyz`) that `uv lock` cannot resolve —
  it MUST be re-authored against a real package for this positive case.
- **Unresolvable lock entry (negative / fail-loud).** A separate fixture with a `uv.lock` entry pointing
  at a nonexistent version/source; assert `nix build` (or `tryEval` on the drvPath) **fails**. Preserves
  the superseded `pg2-gjwpl` fail-fast intent in the uv2nix world.
- **Both version surfaces.** Retarget `python-version-digest` to the **wrapper** derivation and assert
  `version` has prefix `0.0.0-` with digest `== mkSrcDigest src` (AC1); at Tier-3 assert runtime
  `--version` carries the `YY.MM.DD.SSSSS+<digest>` shape with the `+` local separator (distinct from
  the derivation's `-`), explicitly `!= 0.0.0` and `!=` the `0.0.0-<digest>` form (AC2/AC3). Pin the
  exact regex to the surface the app reads: `^\d{2}\.\d{2}\.\d{2}\.\d{5}\+` for a literal `__version__`,
  or `\d{1,2}` per segment if it reads `importlib.metadata` (spike-confirmed PEP 440 leading-zero
  normalization, identical to the current builder).
- **Completeness invariant.** The Tier-3 import smoke MUST import each app's **real top-level package**
  (transitively its deps), never `import sys`, so a missing transitive is actually exercised.
- **Currying / propagation seams.** demo-py gains a git-tracked `uv.lock`; both checks updated for
  currying; a factory-instantiate-without-build eval check for the agent-support shape (D2); extend
  `consumer-fixture-eval` to assert the 3 inputs are present transitively and `follows`-deduped onto
  base's `nixpkgs` (D3); grep-assert no consumer flake declares the 3 inputs.
- **Wheel/sdist.** Build both consumers on `x86_64-linux` **and** `aarch64-darwin`; add an sdist-forcing
  fixture so `pyproject-build-systems` is actually exercised (the real consumers ship universal wheels).

## Consequences

### Positive

- Fully closes the `uv.lock` version drift: dev/CI/pytest and the shipped artifact resolve from the
  same lock — one source of truth.
- Deletes the manual holes: work-activity-tracker's hand-packaged `eventsourcing` (`fetchPypi` + hash
  pin) and pd-schedule-manager's `pypiToNixNameMappings` name-fixup.
- Matches the accepted gomod2nix precedent (ADR 0008); the fleet gains a consistent "nix build reads the
  language's native lock" story across Go and Python.
- **`update-locks.sh` needs NO new step** — unlike gomod2nix (which added `gomod2nix generate` steps
  because it introduces a second derived lock artifact), uv2nix reads `uv.lock` directly. support-apps
  already runs `uv lock --upgrade`; base's 3 new inputs are covered by the existing `nix flake update`.

### Negative

- Three new upstream sources enter base's lock (larger eval/closure); a genuinely new tool to own.
- Breaking-ish builder internals: the build moves from `buildPythonApplication` to `mkVirtualEnv`, so
  the runtime-deps check and the version-stamp mechanism change (mitigated by the no-op-arg retention
  and the Tier-3 `--version` gate).
- Wheel/sdist platform coverage is a new risk class requiring a two-system Tier-3 build.
- Bus-factor bet on a lightly-staffed ecosystem.

### Neutral — Versioning (ADR 0006/0011 retained; formats unchanged)

- **Derivation `version`** (`nvd` / "Package changes"): unchanged string `0.0.0-<srcDigest>`, from
  `mkSrcDigest src`, stamped on the wrapper. The digest _input_ is unchanged (`src` already includes
  `uv.lock` + `pyproject.toml`); what changes is that a `uv.lock`-only edit now honestly corresponds to
  a closure change instead of bumping the version while shipping the same nixpkgs versions.
- **Runtime `--version`**: unchanged format `YY.MM.DD.SSSSS+<srcDigest>`; substitution relocated (see
  Decision 4). Digest still computed at eval from original `src`.
- **Cosmetic**: the app becomes a thin wrapper over a `<pname>-env` venv, so `nvd` shows an extra `-env`
  closure member beside the (unchanged) top-level line.

### Neutral — tests

- As implemented, base ships **four** Python checks, each importing the builder directly and so
  receiving the 3 uv2nix inputs (else `nix flake check` fails at eval): `python-version-digest` (AC1,
  eval-only), `python-lock-version-drift` (D1 + AC2/AC3 slice), `python-resolve-lock-driven` (positive
  absent-from-nixpkgs resolution), and `python-factory-currying-eval` (D2 factory-only shape).
- The old `python-resolve-fail-fast` is **rewritten, not deleted**: its name-match
  `allowMissingDeps`/`throw` assertions are superseded; the regression intent survives as the positive
  `python-resolve-lock-driven` check. The complementary **negative** (fail-loud on an unresolvable /
  missing lock) is **deferred to the Tier-2/3 follow-up** — empirically uv2nix surfaces those as
  build-time or non-`tryEval`-catchable eval errors, so they cannot be a hermetic green Tier-1 check.
- Fixtures gain git-tracked, intentionally-pinned `uv.lock` files that MUST NOT be auto-upgraded:
  `py-lock-pin` (drift-equality; its `[project].name` is deliberately non-normalized to guard the PEP
  503 overlay-key normalization) and the re-authored `py-missing-dep` (absent-from-nixpkgs). `demo-py`
  needs no lock — the digest check reads `.version` without forcing `loadWorkspace`.

## Alternatives Considered

- **Bare `pyproject.nix` (no uv2nix).** `pyproject.nix` alone does not consume `uv.lock`; you would
  hand-roll lock resolution — the exact problem uv2nix exists to solve. Rejected (strictly dominated).
- **poetry2nix.** Consumes `poetry.lock`, not `uv.lock`; would reverse ADR 0013 (which rejected Poetry)
  or maintain a second lockfile — reintroducing drift by another door. Rejected.
- **Status quo + a lock-drift check gate.** A Guard that makes drift _visible_ but does not _close_ it;
  the shipped artifact stays nixpkgs-versioned. Sanctioned only as an interim if adoption is deferred.
- **Do nothing (accept the `pg2-gjwpl` fallback).** The reviewer advised against closing on the fallback
  alone; drift persists indefinitely. Rejected.

## Related Decisions

- Precedent: [0008](0008-adopt-gomod2nix-for-go-packages.md) — adopt gomod2nix for Go (the engine-swap
  analog).
- Retains [0006](0006-source-content-digest-versioning.md) and
  [0011](0011-source-digest-in-derivation-version.md) versioning (both surfaces preserved).
- support-apps ADR 0013 (uv + setuptools) — records the two-resolver drift as a known negative this ADR
  closes; add a note there that the nix build now consumes `uv.lock` via base uv2nix.
- Implements the reviewed plan on bead `pg2-gjwpl` (closed) → owner decision/implementation `pg2-r4cfy`.
- Decision brief: support-apps `docs/superpowers/specs/2026-07-15-uv2nix-lock-driven-python-builds-design.md`.
