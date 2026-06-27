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

### Required in `cmd/<binary>/main.go`

The factory injects the version string via `-X main.Version=${version}` ldflag. For
this to actually embed the version, your `package main` MUST declare a matching
`var Version` symbol at the package level:

```go
package main

var Version = "dev"  // overridden at build time via -X main.Version

func main() {
    // ... use Version as your --version output ...
}
```

Without this declaration, the ldflag silently no-ops at link time and the embedded
version is lost — `--version` (or whatever you print) will return the literal
fallback (`"dev"`) or an empty string. Go's linker does not warn when the target
symbol is missing.

## Optional parameters

Beyond `name`, `src`, `version`, and `description`, `mkGoBinary` accepts:

| Parameter          | Default                                     | Purpose                                                                                                                                                     |
| ------------------ | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `runtimeDeps`      | `[]`                                        | Runtime deps wrapped onto PATH (e.g., `pkgs.git` for shell-outs). Propagated to consumers. NOT for static linking — Go binaries link statically by default. |
| `testDeps`         | `[]`                                        | Extra packages on PATH during `go test` (e.g., `pkgs.git` for tests that invoke `git`).                                                                     |
| `manPage`          | `true`                                      | When `false`, skips help2man man-page generation. Useful for binaries without a `--help` that help2man can parse.                                           |
| `completions`      | `{ bash = true; zsh = true; fish = true; }` | Per-shell completion generation. Set e.g. `completions.fish = false` to skip a shell.                                                                       |
| `vendorHash`       | `null`                                      | Go module vendoring hash. `null` means no vendoring; otherwise set to the sha256 reported by your first `nix build`.                                        |
| `extraPostInstall` | `""`                                        | Extra shell commands appended to postInstall (escape hatch).                                                                                                |

`description` is fed to `help2man --name=`, so if `manPage = true` and
`description = ""` the resulting man page will be poorly formed — either set a real
`description` or pass `manPage = false`.

## Required co-deliverables

Every `mkGoBinary` consumer MUST ship these alongside the Nix derivation:

### 1. `bin/<tool>` go-run shim

A bash wrapper in the repo's `bin/` directory that invokes `go run`. NOT on PATH. NOT exported via Nix. Used by CI and developers when the compiled binary may be stale (or when there's no binary at all because we don't commit them). This replaces the former `run-from-source.sh` at the module root.

```bash
#!/usr/bin/env bash
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
cd "$SCRIPT_DIR/../path/to/go-module"
exec go run ./cmd/<binary-name> "$@"
```

**Version-guarded tools inject a sentinel.** A tool whose `main` rejects the `"dev"` version (to refuse binaries built outside the Nix derivation) cannot use the plain shim above — `go run` leaves the version at `"dev"` and the tool refuses to run. Such a tool's shim injects a sentinel that passes the guard (the check is an exact `"dev"` match) while staying obviously a from-source run. Use a literal like `dev-wrapped` — **not** a synthesized `<date>-<rev>`, which would masquerade as a real Nix-built release and defeat the guard's purpose of signalling how the binary was produced:

```bash
exec go run -ldflags "-X main.Version=dev-wrapped" ./cmd/<binary-name> "$@"
```

Keep the injection coupled to the guard: tools **without** a `"dev"` guard use the plain shim (a sentinel on an unguarded tool is misleading ceremony). `bin/pn` is the canonical guarded example.

### 2. `.gitignore` entry for the binary

Add the binary name to the module's `.gitignore`:

```gitignore
# Compiled binary (built via Nix; do not commit)
/<binary-name>
```

### 3. Version via `mkVersion`

The `version` field of `mkGoBinary` MUST be `self.lib.mkVersion self` (or equivalent). The factory rejects the literal `"dev"` and any version ending in `"-dev"` to enforce "live binary always non-dev."

### 4. Tests wired into `nix flake check`

Go tests are run with `-race`:

```
go test -race ./...
```

Hook them into your flake's `checks.<system>.<name>` output so `nix flake check`
runs them. The minimal pattern wraps `go test` in `pkgs.runCommand`:

```nix
# In your flake.nix's checks output (per-system):
checks.my-tool-go-tests = pkgs.runCommand "my-tool-go-tests" {
  src = ./.;  # path to your Go module
  nativeBuildInputs = [ pkgs.go ];
} ''
  export HOME=$TMPDIR
  cp -r $src/* .
  go test -race ./...
  touch $out
'';
```

The actual implementation may differ slightly — for example, a vendored module
needs `GOFLAGS="-mod=vendor"` and the vendored `vendor/` directory copied in, and a
module that hits the network during tests needs explicit fixtures. Reference the
`pn` module's flake wiring (Task 18 of the nix-\* refactor plan) once it lands for a
fully worked example.

## Version format

`mkVersion self` produces `YYYYMMDD-shortRev` from the flake's `lastModifiedDate` and `rev`. For clean git trees this is e.g. `20260531-abc1234`. For dirty trees with a `dirtyRev` this is `20260531-<7chars>`. Without any git info at all, falls through to `20260531-dev` — which the factory rejects.

The format may evolve in `version.nix`; consumers pick up changes automatically.

## Why these conventions?

- **Man pages + completions**: Standardized so users get the same affordances from every tool.
- **Version contract**: Operations/debugging needs to know which binary is running. "dev" hides that information.
- **`bin/<tool>` go-run shim**: Avoid the failure mode where dev/CI use a stale committed binary instead of current source.
- **Gitignored binary**: Compiled binaries are build artifacts, never source-of-truth.

## See also

- `lib/version.nix` — `mkGitHash`, `mkVersion`, `mkInstallMetadata`.
- `lib/go-builders.nix` — the factory itself.
- `docs/adr/0005-mkGoBuilders-factory.md` — the architectural decision record.
