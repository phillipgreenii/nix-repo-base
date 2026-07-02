# pjira — generic Atlassian Jira access tool

A tenant-agnostic Jira library (`pkg/pjira`) + CLI (`cmd/pjira`). It hard-codes no
tenant, credential location, or OS-specific behavior; ZR (and any other tenant)
specifics are injected as configuration at the edge.

> **TAG — future extraction:** this lives in `repo-base` only to satisfy the
> "no new flake input / no dependency cycle" constraint (see
> `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §5.2).
> It is intended to move to a dedicated repo. Keep it free of repo-base-specific
> coupling so the lift-and-shift stays cheap. Tracking bead: pg2-2x2d.

## Operations

- `pjira issue <KEY>` — one issue as JSON.
- `pjira search --jql "<JQL>" [--limit N] [--expand changelog[,comments]]` — `{items,truncated,next_page_token?}`.
- `pjira auth-status` — credential check.

### Configuration

The default config path is `$XDG_CONFIG_HOME/pjira/config.toml` (or
`~/.config/pjira/config.toml`). **BREAKING:** this moved from the old
`~/.config/jira/` directory when the tool was renamed `jira` → `pjira`; migrate
any existing config with `mv ~/.config/jira ~/.config/pjira`.

### Pagination

`pjira search` returns one page by default (`{items, truncated, next_page_token?}`).
`next_page_token` is present when more pages remain.

- `--cursor <token>` — fetch the single page at `<token>` (the `next_page_token`
  from a previous call). Lets a caller drive the loop itself.
- `--all` — loop every page internally and return the complete, concatenated
  result (`truncated:false`). Bounded by an internal safety cap; on a cap-hit the
  envelope is partial with `truncated:true` and a warning is written to stderr
  (exit 0). `--all` and `--cursor` are mutually exclusive.
