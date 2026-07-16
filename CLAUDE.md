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

## Versioning

Custom artifacts version from a per-source content digest, never the repo git rev (ADR
[0006](docs/adr/0006-source-content-digest-versioning.md)). Do not thread a repo `gitHash` into a
package build. The per-source digest now ALSO appears in the derivation `version` for bash and
python builders (matching Go), so it surfaces in `nvd` / the darwin "Package changes" report
(ADR [0011](docs/adr/0011-source-digest-in-derivation-version.md)).
