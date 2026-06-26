# jira — generic Atlassian Jira access tool

A tenant-agnostic Jira library (`pkg/jira`) + CLI (`cmd/jira`). It hard-codes no
tenant, credential location, or OS-specific behavior; ZR (and any other tenant)
specifics are injected as configuration at the edge.

> **TAG — future extraction:** this lives in `repo-base` only to satisfy the
> "no new flake input / no dependency cycle" constraint (see
> `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §5.2).
> It is intended to move to a dedicated repo. Keep it free of repo-base-specific
> coupling so the lift-and-shift stays cheap. Tracking bead: pg2-2x2d.

## Operations

- `jira issue <KEY>` — one issue as JSON.
- `jira search --jql "<JQL>" [--limit N] [--expand changelog[,comments]]` — `{items,truncated}`.
- `jira auth-status` — credential check.
