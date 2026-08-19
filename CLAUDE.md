# phillipg-nix-repo-base — Repository Rules

Shared Nix infrastructure (builders, `pn` workspace tooling, lib helpers) imported by the other
`nix-*` flakes in this workspace.

## Architecture Decision Records

ADRs live in `docs/adr/` (`index.md` lists them). Read relevant ADRs before changing the area they
cover; see `docs/adr/0000-use-architecture-decision-records.md` for the process.

## Go packages (`mkGoApp` / `mkGoBinary`)

Go apps built through the `mkGoApp` / `mkGoBinary` helpers (`lib/go-builders.nix`) use the
**gomod2nix engine** (`buildGoApplication`). Authority: **ADR
[0008](docs/adr/0008-adopt-gomod2nix-for-go-packages.md)** (supersedes 0007; retains 0006
per-source-digest versioning).

Rules for this family:

- Pass **`gomod2nixToml = ./gomod2nix.toml;`** (required). Do **not** use `vendorHash`,
  `localReplaceModules`, or `buildGoModule` for these packages.
- Commit a **`gomod2nix.toml`** beside each package's `go.mod`. It must be git-tracked — an
  untracked toml is invisible to flake builds (and to `pn workspace apply`).
- Bump dependencies with **`go mod tidy && nix run github:nix-community/gomod2nix -- generate`**
  (not `nix-update`, no `vendorHash` dance). Regenerate + commit the toml when deps change; pure
  first-party edits (incl. a local-replace sibling) need no regeneration.
- **Pattern A** — single module at the package root: `src = lib.cleanSource ./.;`, no `modRoot`.
- **Pattern B** — a local `replace => ../sibling` in `go.mod`: root the source at the parent so the
  sibling is in one store tree —
  `src = lib.fileset.toSource { root = ./..; fileset = lib.fileset.unions [ ./. ../sibling ]; };`
  plus `modRoot = "<name>";`. `mkGoApp` then sets `pwd = src + "/<name>"` and `buildGoApplication`
  resolves the replace natively via a symlink (no vendoring, no hash).

Note: raw `buildGoModule` packages that do **not** go through these helpers (e.g. third-party
repackages) keep their own `vendorHash` — this guidance is scoped to the `mkGoApp`/`mkGoBinary`
family.

## Python packages (`mkPythonPackage`)

Python apps built through `lib.mkPythonBuilders` → `mkPythonPackage` (`lib/python-package.nix`) use
the **uv2nix engine**: the shipped closure is resolved from each package's committed **`uv.lock`**, not
name-matched against nixpkgs. Authority: **ADR
[0022](docs/adr/0022-adopt-uv2nix-for-python-packages.md)** (retains 0006/0011 per-source-digest
versioning).

Rules for this family:

- Commit a **`uv.lock`** beside `pyproject.toml`. It is **load-bearing** — it drives the build closure,
  not just dev/CI — and must be git-tracked (an untracked lock is invisible to flake builds, and
  `loadWorkspace` requires one at the workspace root). Do **not** delete/gitignore it or exclude it
  from `src`.
- Refresh dependencies with **`uv lock`** (or `uv add`) and commit the lock. There is **no** generate
  step and **no** second lock artifact (unlike gomod2nix) — uv2nix reads `uv.lock` directly, so
  `update-locks.sh` needs no uv2nix-specific step.
- Do **not** hand-package deps via `fetchPypi`/`customDeps` or add `pypiToNixNameMappings` — the lock
  resolves everything, including deps absent from nixpkgs by name. (These args are retained as accepted
  **no-ops** only until the support-apps consumers are cleaned up; do not rely on them.)
- The interpreter stays `pkgs.python3`; per-source-digest versioning (ADR 0006/0011) is preserved — the
  nvd-visible `version` (`0.0.0-<digest>`) is stamped on the wrapper and the runtime `--version`
  (`YY.MM.DD.SSSSS+<digest>`) is stamped on the root package's build.
- Fixture locks under `lib/fixtures/` are intentionally pinned — never `uv lock --upgrade` them.

## Pre-commit hooks (`.pre-commit-config.yaml`)

`.pre-commit-config.yaml` is a git-hooks.nix-generated **symlink into `/nix/store`** and MUST NOT
be committed — a committed store path is GC-eligible and rots into a dangling symlink (ADR
[0016](docs/adr/0016-gitignore-generated-pre-commit-config.md)). Every repo consuming
`flake-modules/pre-commit.nix` MUST gitignore it (exact line `.pre-commit-config.yaml`); the
`checks.pre-commit-config-gitignored` flake check enforces this. Regenerate the working-tree
symlink with `nix run .#install-pre-commit-hooks` or by entering the devShell. Do **not** re-add
it to git and do **not** auto-write the `.gitignore` entry from the shellHook.

`flake-modules/pre-commit.nix` is a LIGHT-UPSTREAM module imported by EVERY consumer as
`inputs.phillipgreenii-nix-base.flakeModules.pre-commit`. Any hook added to its base `hooks` set
therefore runs inside every consumer's sandboxed `checks.pre-commit` — not just this repo's. This
bit `pg2-q6i5`: a golangci-lint hook in the base set turned `pn workspace flake-check` RED on
personal, support-apps, AND ziprecruiter. Per-repo hooks belong in each repo's
`phillipgreenii.pre-commit.extraHooks` (with sandbox-skip guards where needed); a base-set hook
needs an opt-in gate (fix `e90c73b` gates golangci on a root `.golangci.yml`, which consumers lack
→ no-op). Corollary: a per-repo `nix flake check` (Tier 1, locked inputs) CANNOT catch a shared-
module change breaking consumers — only `pn workspace flake-check` (Tier 2, sibling overrides)
reveals it.

## Versioning

Custom artifacts version from a per-source content digest, never the repo git rev (ADR
[0006](docs/adr/0006-source-content-digest-versioning.md)). Do not thread a repo `gitHash` into a
package build. The per-source digest now ALSO appears in the derivation `version` for bash and
python builders (matching Go), so it surfaces in `nvd` / the darwin "Package changes" report
(ADR [0011](docs/adr/0011-source-digest-in-derivation-version.md)).

## Mutation testing (`pg-go-mutate`, `pg-go-mutate-sweep`)

`pg-go-mutate` reports which assertions a Go package's tests are missing. Every surviving mutant is
an assertion the tests do not make. It is a diagnostic, not a gate: it always exits 0 on a completed
analysis however many mutants survived, records nothing, and tracks no score over time. Use it when
strengthening tests; see the `go-test-gaps` skill for the workflow. Design:
`docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md`.

A completed analysis still exits 0, but a GUARD failure now identifies itself, so a caller can tell
"this package needs assertions" from "this package never ran": `10` no test files, `11` not
enumerable, `12` unhealthy (does not vet, or tests already fail on unmutated source), `13`
environment precondition failed (`go` or the pinned engine absent or mismatched), `14` target path
absent or not a directory. `2` remains usage error, and `1` stays reserved for generic/unexpected
failure — callers MUST NOT give `1` a branchable meaning. The new codes are strictly additive:
every prior consumer asserted only the 0/non-zero dichotomy. Allocation and rationale: ADR
[0026](docs/adr/0026-mutation-sweep-state-contract.md).

`pg-go-mutate-sweep` is the unattended sibling. It runs `pg-go-mutate` over every Go package in the
workspace one `(project, package)` unit at a time, and is resumable — re-running the same command
continues from where it stopped, and by default re-attempts no already-recorded unit, so a run
always makes forward progress and cannot loop on a broken one. Unlike `pg-go-mutate` it DOES record
durable state, under `${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-sweep/`: an append-only
`ledger.jsonl`, replayed (last record per key) to derive what is done, plus one worklist JSON per
unit. The ledger records unit STATUS only — `done`, `no-tests`, `failed`, `timeout`, … — and MUST
NOT record a survivor count, a percentage, or any score. That prohibition is the family's, not the
file format's: it binds beads, commit messages and docs equally, because a worklist is actionable
and a score is not. On finishing a project the sweep files exactly ONE triage bead for it and MUST
NOT file an epic — an open epic never leaves `bd ready`.

Read `tags_withheld` on a unit's record before acting on its survivors: non-empty means that unit's
tests were partly gated behind build tags that were not applied, so the survivors are UNANALYSED.
Such a unit records `done` and is otherwise indistinguishable from a genuine gap.

The sweep resolves `pg-go-mutate` and `bd` from `PATH`, and neither MUST be added as a nixpkgs
`runtimeDep`: `runtimeDeps` are appended with `--suffix`, so listing them could not displace the
machine's wrapper but WOULD supply a silent fallback to an unwrapped `pg-go-mutate` (no engine pin,
no version assertion) or an unmanaged `bd` (losing `BEADS_DOLT_AUTO_START=0`, so it can spawn a
competing dolt server). Neither command MUST be added to CI, a pre-commit hook, or any `checks.*`
derivation that performs a mutation run — a sweep costs hours and gates nothing. Registering the
sweep's bats check is the only `checks.*` entry in scope. Design:
`docs/superpowers/specs/2026-08-17-pg-go-mutate-sweep-design.md`.

### Verifying the engine pin in a post-apply check

The engine guard (`pgm_require_engine`, `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`) is
observable on BOTH install paths, and the SAME substitution attempt MUST produce OPPOSITE outcomes
on them — so a verification step MUST name which binary it drives:

- **wrapped** — what `homeModules.pg-go-mutate` installs; on `PATH` after an apply, with
  `command -v pg-go-mutate` resolving into a `…-wrapped` store path.
- **unwrapped** — `packages.pg-go-mutate` / `overlays.default`, reached as
  `nix run <repo-base>#pg-go-mutate` (equivalently the wrapper's inner `.pg-go-mutate-wrapped_`
  sibling, which IS that same script — but that filename is `makeWrapper`'s, not a contract).

| Substitution attempt against `pg-go-mutate <pkg>` | wrapped                                | unwrapped                  |
| ------------------------------------------------- | -------------------------------------- | -------------------------- |
| a fake `gomu` earlier on `PATH`                   | MUST still run the pinned store engine | MUST abort, naming the pin |
| `PG_GO_MUTATE_GOMU=<fake gomu>`                   | MUST still run the pinned store engine | MUST abort, naming the pin |

- A check driving the WRAPPED binary MUST expect BOTH attempts to SUCCEED. Expecting an abort there
  is the CHECK's error, not a defect in the wiring: pg2-uk8wi's check 4b asked for one and is
  unsatisfiable by construction (bead pg2-3x7xm).
- The mismatch abort MUST therefore be asserted against the UNWRAPPED entry point, where both seams
  are honoured — and MUST be asserted on the MESSAGE, which names the expected pin and what the
  engine reported, rather than on an exit code alone: that code is `13` since the guard exit-code
  allocation above and was plain `1` before it, so a code-only assertion silently dates the check.
- WHY the wrapped column reads "still run": the wrapper binds `PG_GO_MUTATE_GOMU` and
  `PG_GO_MUTATE_GOMU_VERSION` with `makeWrapper --set` (spec W9), an UNCONDITIONAL export, so it
  closes the env-var vector as well as the `PATH` one — stronger than the check assumed. Should
  either binding become `--suffix`, or stop being emitted, the wrapped column changes and this table
  MUST be re-derived before it is trusted again.

## treefmt markdown formatting (prettier)

treefmt formats markdown/yaml/json with prettier (version UNPINNED — it comes from
`nix/dev-env.nix` `mkTreefmtConfig` via `programs.prettier.enable = true`, so it tracks nixpkgs).
prettier is NON-IDEMPOTENT on some markdown: wide-unicode tables plus star-emphasis next to
underscored identifiers need 2+ passes to reach a fixed point. A file committed at a non-fixed
point reds BOTH `nix flake check` (`checks.formatting`) and prek — they run the IDENTICAL
prettier, so it is never a version skew. Always run treefmt/prek TO CONVERGENCE before committing
markdown. Decision (Phillip, after `pg2-qe48`): prettier was chosen only as the treefmt-nix
batteries-included default, never vetted for markdown idempotency — if non-idempotency recurs,
evaluate switching the markdown formatter (dprint or mdformat via treefmt `extraPrograms`) in
`mkTreefmtConfig` here, instead of chasing prettier fixed points.

## Consumer input alignment

The alignment machinery VERIFIES consumer input alignment; it does NOT converge revs. Each
heavy-upstream overlay flakeModule (`flake-modules/overlays/{unstable,llm-agents,vscode-extensions,flox}.nix`,
exposed as `flakeModules.*-overlay`) reads the CONSUMER's own input and registers
`phillipgreenii.alignment.requires += [that input name]` — this repo deliberately does NOT own
those inputs. The consumer-input-alignment check (`flake-modules/checks.nix`) reads the
accumulated requires from the consumer's top-level `flake.lock` and FAILS if a required input is
missing from `.nodes.root.inputs` or has duplicate `<name>_N` nodes. `follows` is exactly how you
SATISFY the check — ad-hoc follows ARE the intended dedup tool. Importing an `*-overlay`
flakeModule therefore obligates the consumer to declare that input at top level and dedup
downstream copies onto it via follows.

Policy (workspace audit `pg2-qt7u`): standalone per-repo `flake.lock` drift on third-party inputs
is EXPECTED; convergence happens at the terminal repo (phillipg-nix-ziprecruiter) via follows.
Intentionally INDEPENDENT inputs — do not "align" these: `flox` (own cache `cache.flox.dev`; MUST
NOT follows-override its nixpkgs or it rebuilds from source); `llm-agents` (own cache
`cache.numtide.com`; do NOT force its nixpkgs to follow); `nixpkgs-unstable`
(cache.nixos.org-backed, double-realize is cheap).

## pn workspace: building ONE repo from a worktree

`pn workspace build`/`apply`/`info` CANNOT target a single repo's worktree: repo-path resolution
always joins `<workspace_root>/<repo-name>` — that repo's CANONICAL clone — no matter which
directory `pn` was invoked from. Running any of the three from inside a plain `git worktree` (not
a coordinated workforest set) therefore silently validates the canonical checkout, never the
worktree — so it is NOT a valid pre-land gate for worktree-based work on that repo (bead
pg2-9ova0). Empirically confirmed 2026-08-18: a syntax-breaking edit made ONLY inside a throwaway
worktree (which independently failed `nix flake metadata` run directly against that worktree) did
not fail `pn workspace build`/`info` when run from inside that same worktree — see bead pg2-9ova0
for the full transcript.

RE-VERIFIED against `modules/pn` @ main `127bd5eb93a83d6edf5ed67bae88d5c5dd40c289` (2026-08-18,
bead pg2-9ova0) — the prior claim below still holds, and is narrower than it may have sounded:

- `PN_WORKSPACE_OVERRIDE_PATHS` does NOT affect `build`/`apply`/`info`.
  `workspace.ParseOverridePaths`/`parseOverridePaths`
  (`modules/pn/internal/workspace/overridepaths.go:15,53`) has exactly **three** callers
  repo-wide, all in `internal/workspace/overridepaths_test.go` (confirmed via
  `git grep -rn "ParseOverridePaths"` across the whole repo, not just `modules/pn`). No CLI `RunE`
  handler calls it, and no command registers a `--override-path` (or any array/slice) flag:
  `workspaceBuildCmd`/`workspaceApplyCmd` construct `workspace.BuildOptions{Terminal: *terminal}` /
  `workspace.ApplyOptions{Terminal: *terminal}` with no `OverridePaths` field set
  (`internal/cli/workspace.go:202,220`), and `workspaceInfoCmd` doesn't accept overrides at all —
  `Info()` takes no options whatsoever.
- This is narrower than "dead code" sounds: the `OverridePaths map[string]string` field on
  `BuildOptions`/`ApplyOptions` IS real, tested, production logic — consumed by
  `Workspace.Build`/`Workspace.Apply` and their helpers (`build.go:47,53`; `apply.go:38,49,101`;
  `helpers.go:94`), and documented in-place as the "override-path apply / coordinated-worktree
  flow" (`apply.go:212`, `info.go:87`, `updatecache.go:101,103`). It is ONLY the CLI _surface_ for
  it — a flag, or reading the env var — that was never built. A 2026-06-01 design plan
  (`docs/superpowers/plans/2026-06-01-pn-workspace-build-apply.md`, ~L1683-1751) shows a
  `--override-path name=path` flag WAS designed for exactly this, wired through
  `ParseOverridePaths`, but that step's own checkboxes are unchecked and it was never implemented
  — the design apparently pivoted to the coordinated-workforest-set mechanism below instead.
- Working as designed, not a separate bug: treat `PN_WORKSPACE_OVERRIDE_PATHS`/`--override-path`
  as unsupported CLI surface until someone deliberately wires it up (a plausible, narrowly-scoped
  follow-up — NOT attempted here: it touches CLI flag surface used workspace-wide, so pg2-9ova0
  didn't do it speculatively; see bead pg2-9ova0 for the follow-up recommendation).

pn builds against worktrees only via a coordinated workforest set (`pn workspace workforest add`),
where ALL repos resolve to set worktrees on the same branch — it cannot mix one repo-from-worktree
with canonical siblings. WORKAROUND: invoke `nix build .#darwinConfigurations.<host>.system` (or
`darwin-rebuild build`) directly from the terminal repo, replicating pn's sibling
`--override-input <alias> git+file://<path>` flags by hand with the target repo's path swapped to
the worktree path — or simply run `nix flake check`/`nix build` directly INSIDE the worktree: that
never routes through pn's repo-path resolution, so it is worktree-correct by construction.
