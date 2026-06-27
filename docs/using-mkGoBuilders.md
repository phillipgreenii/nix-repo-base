# Using mkGoBuilders

`mkGoBuilders.mkGoBinary` builds a Go binary via `buildGoModule` with standard postInstall (man page + completions) and a version contract.

## Quick start

```nix
# In your module's default.nix:
{ pkgs, self }:
# No `version` argument: mkGoBinary derives the version from this package's own
# source-content digest (ADR 0006). See "Version format" below.
(self.lib.mkGoBuilders { inherit pkgs self; }).mkGoBinary {
  name = "my-tool";
  src = ./.;
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

Beyond `name`, `src`, and `description`, `mkGoBinary` accepts:

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

### 3. Version: derived automatically from the source-content digest

You do **not** pass a `version`. `mkGoBinary` / `mkGoApp` derive it from each package's own
`src` content digest (ADR [0006](adr/0006-source-content-digest-versioning.md)), producing
`0.0.0-<srcdigest8>` (the default `baseVersion` `0.0.0` plus an 8-hex-char `sha256` digest of
the source paths via `lib.mkSrcDigest`). The version changes iff the package's own source
changes, so editing one package never restamps a sibling.

A caller-supplied `version` is ignored: `mkGoBinary` is a **closed** argument set that does
not accept a `version` argument at all (passing one is an evaluation error), and `mkGoApp`
(which accepts arbitrary extra args) **strips** any passed `version` via `removeAttrs` before
delegating to `buildGoApplication`. There is **no** factory-level rejection of `"dev"` — the
derived version is never `"dev"`. A tool that wants to refuse non-Nix builds enforces that
with its own runtime guard in `main` (the `bin/pn` sentinel above), not in the factory.

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

Go packages embed `0.0.0-<srcdigest8>`, where `0.0.0` is the default `baseVersion` and
`<srcdigest8>` is `first8(sha256(...))` of the package's own source paths
(`lib.mkSrcDigest`, ADR [0006](adr/0006-source-content-digest-versioning.md)). Example:
`0.0.0-1a2b3c4d`. The digest is computed at eval time from `src` alone, so it is stable for
identical source and changes on any edit (committed or dirty) — there is no
`lastModifiedDate` / `rev` and no `"dev"` fallback.

Note: `mkSrcDigest` hashes the source store-path strings, not the NAR content directly (see
the note in `lib/version.nix`). `mkVersion` still exists in `lib/version.nix` and is consumed
by the repo-meta module and the bash/python builders — but the Go builders no longer use it.

The version is injected with `-X <versionPath>=<version>`, where `versionPath` defaults to
`main.Version` for `mkGoBinary` (and `main.version` for the lower-level `mkGoApp`). The
format may evolve in `go-builders.nix` / `version.nix`; consumers pick up changes
automatically.

## Why these conventions?

- **Man pages + completions**: Standardized so users get the same affordances from every tool.
- **Version contract**: Operations/debugging needs to know which binary is running. The per-source digest (`0.0.0-<srcdigest8>`) pins a binary to exactly the source it was built from and changes on every edit.
- **`bin/<tool>` go-run shim**: Avoid the failure mode where dev/CI use a stale committed binary instead of current source.
- **Gitignored binary**: Compiled binaries are build artifacts, never source-of-truth.

## See also

- `lib/version.nix` — `mkGitHash`, `mkVersion`, `mkSrcDigest`, `mkInstallMetadata`.
- `lib/go-builders.nix` — the factory itself.
- `docs/adr/0005-mkGoBuilders-factory.md` — the original factory decision.
- `docs/adr/0006-source-content-digest-versioning.md` — per-source-digest versioning (replaces the `mkVersion self` contract for Go).
- `docs/adr/0008-adopt-gomod2nix-for-go-packages.md` — gomod2nix engine (`buildGoApplication`).
