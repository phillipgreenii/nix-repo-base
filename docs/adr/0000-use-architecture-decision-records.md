# Use Architecture Decision Records at Repository Root

**Status**: Accepted
**Date**: 2026-03-05
**Deciders**: Phillip Green II

## Context

Across three Nix configuration repositories (phillipgreenii-nix-personal, phillipg-nix-ziprecruiter, phillipgreenii-nix-support-apps), architectural decisions accumulate over time — technology choices, structural patterns, cross-cutting conventions — but live only in commit history and tribal knowledge. As AI agents increasingly drive development in these repos, the problem intensifies: agents lose conversation context across sessions and cannot recover decision rationale from code alone.

We need a lightweight, discoverable way to capture architectural decisions that:

- Lives alongside code in version control
- Is accessible to both humans and AI agents
- Supports cross-repository references (decisions in one repo often affect others)
- Handles both repo-level and package-level decisions
- Uses a consistent naming scheme for chronological traceability

## Decision

We adopt **Architecture Decision Records (ADRs)** stored in `docs/adr/` at each repository root, using a `NNNN-{short-title}.md` naming scheme with zero-padded sequential numbering.

### Directory Structure

```
<repo-root>/
└── docs/
    └── adr/
        ├── 0000-use-architecture-decision-records.md
        ├── 0001-next-decision.md
        └── draft-upcoming-decision.md
```

### Naming Conventions

- **Accepted ADRs**: `NNNN-{short-title}.md` — sequentially numbered per repo (e.g., `0000-use-adrs.md`, `0001-use-event-sourcing.md`)
- **Draft ADRs**: `draft-{short-title}.md` — unnumbered until accepted, then renamed to the next available sequence number
- **NNNN** is a zero-padded 4-digit number; each repository maintains its own independent sequence

### Cross-Repository References

When a decision in one repo relates to a decision in another, the "Related Decisions" section uses the format:

    See also: <repo-name> docs/adr/NNNN-short-title.md

### Package-Level Exception

The phillipgreenii-nix-support-apps repository is polyglot (Bash, Python, Go, JS) and contains packages with substantial architectural scope. Packages like work-activity-tracker maintain their own `docs/adr/` with an independent numbering sequence for package-scoped decisions (e.g., event sourcing, SPI plugin architecture). Repo-root `docs/adr/` remains the location for cross-cutting and repo-level decisions.

## Consequences

### Positive

- **Single discoverable location** per repo — `docs/adr/` is predictable and easy to find
- **Version-controlled alongside code** — decisions traceable in git history, reviewed in PRs
- **Chronological traceability** — sequential numbering reveals decision order and evolution
- **Cross-repo coherence** — explicit reference format connects related decisions across repos
- **Agent-friendly** — AI agents can read `docs/adr/` at session start to recover architectural context
- **Lightweight** — plain markdown, no special tooling required

### Negative

- **Numbering differs across repos** — no global sequence; cross-repo references must include the repo name
- **Write overhead** — creating an ADR takes effort, especially for retroactive documentation of historical decisions
- **Package-level exception adds complexity** — two `docs/adr/` locations in phillipgreenii-nix-support-apps requires awareness of scope

### Neutral

- ADRs are decisions, not comprehensive design documents — they capture the "why" and "what", not detailed implementation
- Old ADRs remain in the repo even when deprecated or superseded, serving as historical record

## Alternatives Considered

### Per-Package `adr/` Directories

Each Nix package or module maintains its own `adr/` directory.

**Rejected**: Fragments the decision history across many locations. No repo-level sequence for cross-cutting decisions. Harder to discover — must know which package a decision belongs to. The work-activity-tracker exception is narrowly scoped to packages with truly independent architectural scope.

### Wiki or External Documentation

Decisions captured in a wiki, Notion, or other external system.

**Rejected**: Not version-controlled alongside code. Disconnected from the implementation it describes. Another tool to maintain. Less discoverable for AI agents that operate within the repo.

### Comprehensive Design Documents

Full design documents for every significant decision.

**Rejected**: Too heavyweight for most decisions. Harder to maintain over time. The lightweight ADR format captures the essential "why" without the overhead of a complete design spec.

### Inline Code Comments

Document decisions directly in the relevant source files.

**Rejected**: Not discoverable without knowing where to look. Cannot capture alternatives considered or cross-cutting context. Scattered across files with no chronological ordering. Poor fit for decisions that span multiple files or packages.

## Related Decisions

See also: phillipg-nix-ziprecruiter docs/adr/0000-use-architecture-decision-records.md
See also: phillipgreenii-nix-support-apps docs/adr/0000-use-architecture-decision-records.md
See also: phillipgreenii-nix-support-apps packages/work-activity-tracker/docs/adr/0000-use-architecture-decision-records.md
