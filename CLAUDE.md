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

## Versioning

Custom artifacts version from a per-source content digest, never the repo git rev (ADR
[0006](docs/adr/0006-source-content-digest-versioning.md)). Do not thread a repo `gitHash` into a
package build.
