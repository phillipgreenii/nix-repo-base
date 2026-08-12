# pn — workspace flake orchestration

`pn` is a Go program that bootstraps and maintains a `pn-workspace.toml` project, coordinating nix flake dependencies across multiple local git repositories.

## pn workspace doctor

`pn workspace doctor` audits a pn workspace against the build-equality invariant and (with `--fix`) repairs the safe drifts.

**The Invariant (one sentence):** If `doctor` reports no errors, a local override build (`--override-input git+file://<clone>` for each workspace dependency) and a pure-remote build (a plain nix build that uses each repo's committed `flake.lock`, no local overrides) produce the same output.

### Modes

- **Primary mode**: Compares local checkouts against the remote default branch (obtained via `git ls-remote` on the canonical URL).
- **Worktree mode**: Relaxes to branch-name uniformity across the set and drops remote checks; dirty trees become warnings instead of errors.

### Flags

- `--fix` — Apply safe, auto-fixable repairs (respects dependency order).
- `--dry-run` — Print the fix plan without applying changes (requires `--fix`).
- `--offline` — Skip remote-dependent checks; reported as skipped, never silently ok.
- `--json` — Machine-readable output (on stdout only; no banner or progress).
- `--strict` — Treat warnings as errors for the exit code.

### Exit Codes

- `0` — No errors (and, under `--strict`, no warnings). Local and remote builds will match.
- `1` — Errors present (or any finding under `--strict`).
- `2` — Doctor itself failed (e.g., `ls-remote` unreachable without `--offline`).

### Important Note: flake-lock-fresh Fix

The `flake-lock-fresh` fix delegates to `pn workspace update`, which is the only fix that pushes. It relocks affected repos, commits the new lock, and pushes to remote — the one auto-fix that modifies remote state. It is always gated behind `--fix` and shown in `--dry-run`.

### Important Note: ruff-pin (single-ruff-version invariant)

A uv Python package's `ruff` dependency and the nixpkgs `ruff` that the generated `.pre-commit-config.yaml` hooks execute format the SAME files. They are pinned independently — the uv one by `pyproject.toml`, the nixpkgs one by the flake's `nixpkgs` input — so unless they name the same version they can disagree, and a mutating pre-commit formatter then trips pre-commit's "files were modified by this hook" rule mid-commit.

The `ruff-pin` check asserts they agree:

- `ruff-pin-drift` (ERROR) — the package is exact-pinned, but to a different version than the hooks run. This is what a nixpkgs ruff bump produces, and surfacing it is the point: the pin must be bumped in the same change.
- `ruff-pin-floating` (WARN) — the package's spec is not an exact pin (`>=`, `~=`, `==x.y.*`, a range, or unbounded), so a `uv lock` can float it across a formatter-behaviour boundary.
- `ruff-pin` (SKIP) — `.pre-commit-config.yaml` is generated and gitignored (ADR 0016), so it is absent in a fresh clone or worktree. The nixpkgs side is then unknown and nothing is asserted; run `nix run .#install-pre-commit-hooks` and re-run doctor.

The check is read-only and **not** auto-fixable: the remedy is a pin edit plus a `uv lock` relock plus, across a formatter boundary, a reformat of the affected sources. Each finding carries that sequence as its `Manual` hint. Repos that declare no `ruff` dependency, or whose generated config runs no nixpkgs ruff hook, produce no findings.

### Example: Clean Run

```
$ pn workspace doctor
workspace doctor — primary checkouts (origin/main is the baseline)

=== workspace ===

=== phillipg-nix-repo-base ===

=== phillipgreenii-nix-support-apps ===

workspace doctor: no errors (0 warnings). local and remote builds will match.
```

### Example: Run with Findings

```
$ pn workspace doctor --fix --dry-run
workspace doctor — primary checkouts (origin/<branch> is the baseline)

=== dep ===
  ERROR branch-current        repo "dep" is not on its default branch "main" (on "feature") [manual]
          ↳ git -C /workspace/dep switch main
  WARN  repos-extra            git repo "stray" is on disk but not in pn-workspace.toml [fixable]

=== lib ===
  ERROR flake-lock-fresh      flake.lock input "dep" (→ "dep") pins abc1234 but "dep" is at def5678 [would fix]
  SKIP  branch-synced         remote comparison skipped [—]

workspace doctor: 1 errors, 1 warnings.
```
