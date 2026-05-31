# 0005 — mkGoBuilders factory for Go applications

**Status:** Accepted (2026-05-31)
**Context:** tc-perh.5 (pn-\* Bash → Go rewrite)

## Context

Multiple repos under the nix-\* refactor build Go applications via `buildGoModule`. The existing consumers (nix-agent-support's pg-pr, pa-monitor, claude-extended-tool-approver; monorepo's tc-secrets) each reproduce the same postInstall boilerplate: help2man-generated man page, bash/zsh/fish completions via `<binary> completion <shell>`, `-X main.Version=...` ldflag for version embedding.

The bash side has `mkBashBuilders` codifying the equivalent pattern. The Go side lacks such a factory.

## Decision

Add `nix-repo-base.lib.mkGoBuilders` with one function: `mkGoBinary`. Consumers pass `{ name, src, version, vendorHash, description, ... }`; the factory returns a `buildGoModule` derivation with standard postInstall.

The factory enforces a version contract: it rejects the literal `"dev"` string with a `throw`. Consumers compute version via `nix-repo-base.lib.mkVersion self`, which derives `YYYYMMDD-shortRev` from the flake's lastModifiedDate and git rev. This ensures live binaries always carry a non-`dev` version traceable to a git commit (or `dirty` for uncommitted state).

## Consequences

- New Go consumers gain man page + completions + version embedding by passing a few fields.
- Existing consumers (pg-pr, pa-monitor, tc-secrets) should migrate. Migration is tracked as separate beads, not gating this epic.
- Future change: extending `mkVersion` to second-level granularity (e.g., `YYYYMMDDhhmmss-shortRev`) is non-breaking and propagates to all consumers via the helper.
- Required co-deliverables for any `mkGoBinary` consumer:
  - `run-from-source.sh` wrapper at the Go module root (gitignored binary; `go run` invocation; NOT on PATH).
  - `.gitignore` entry for the built binary in the Go module root.
  - `mkVersion self` for the `version` field (NEVER hardcoded, NEVER `dev`).
  - `go test ./...` with `-race` wired into `nix flake check`.
- `goccc` (third-party upstream wrapper) is exempt from migration — its postInstall fixes upstream go.mod issues, doesn't fit the factory cleanly.
