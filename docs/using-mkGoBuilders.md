# Using mkGoBuilders

`mkGoBuilders.mkGoBinary` builds a Go binary via `buildGoModule` with standard postInstall (man page + completions) and a version contract.

## Quick start

```nix
# In your module's default.nix:
{ pkgs, self }:
let
  version = self.lib.mkVersion self;
in
(self.lib.mkGoBuilders { inherit pkgs self; }).mkGoBinary {
  name = "my-tool";
  src = ./.;
  inherit version;
  description = "Short one-line description";
  runtimeDeps = [ pkgs.git ];
  vendorHash = "sha256-...";  # set after first build
}
```

## Required co-deliverables

Every `mkGoBinary` consumer MUST ship these alongside the Nix derivation:

### 1. `run-from-source.sh`

A bash wrapper at the Go module root that invokes `go run`. NOT on PATH. NOT exported via Nix. Used by tests, CI, and developers when the compiled binary may be stale (or when there's no binary at all because we don't commit them).

```bash
#!/usr/bin/env bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
cd "$SCRIPT_DIR"
exec go run ./cmd/<binary-name> "$@"
```

### 2. `.gitignore` entry for the binary

Add the binary name to the module's `.gitignore`:

```gitignore
# Compiled binary (built via Nix; do not commit)
/<binary-name>
```

### 3. Version via `mkVersion`

The `version` field of `mkGoBinary` MUST be `self.lib.mkVersion self` (or equivalent). The factory rejects the literal `"dev"` and any version ending in `"-dev"` to enforce "live binary always non-dev."

### 4. Tests wired into `nix flake check`

Go tests with `-race`:

```
go test -race ./...
```

Hook into `checks.<system>.<name>` in your flake so `nix flake check` runs them.

## Version format

`mkVersion self` produces `YYYYMMDD-shortRev` from the flake's `lastModifiedDate` and `rev`. For clean git trees this is e.g. `20260531-abc1234`. For dirty trees with a `dirtyRev` this is `20260531-<7chars>`. Without any git info at all, falls through to `20260531-dev` — which the factory rejects.

The format may evolve in `version.nix`; consumers pick up changes automatically.

## Why these conventions?

- **Man pages + completions**: Standardized so users get the same affordances from every tool.
- **Version contract**: Operations/debugging needs to know which binary is running. "dev" hides that information.
- **`run-from-source.sh`**: Avoid the failure mode where tests use a stale committed binary instead of current source.
- **Gitignored binary**: Compiled binaries are build artifacts, never source-of-truth.

## See also

- `lib/version.nix` — `mkGitHash`, `mkVersion`, `mkInstallMetadata`.
- `lib/go-builders.nix` — the factory itself.
- `docs/adr/0005-mkGoBuilders-factory.md` — the architectural decision record.
