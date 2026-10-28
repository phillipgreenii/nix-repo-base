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
- `pjira create --project <KEY> --type <NAME> --summary <TEXT> [--description <TEXT>]` — creates
  an issue; writes `{key,url}`. `--description` is plain text, encoded to Atlassian Document
  Format (`pjira.EncodeADFText`) before being sent.

### Issue JSON

`pjira issue` returns one issue object; `pjira search` returns the same object for
each entry of `items`. Always present: `key`, `summary`, `status`, `issuetype`,
`labels`, `url`. Optional fields are omitted when empty, including:

- `status_category` — Jira's status category key for the issue's status, one of
  `new` (To Do), `indeterminate` (In Progress) or `done`. Lets a consumer ask
  "is it finished" without listing workflow-specific status names. The legacy
  "No Category" (`undefined`) and an absent category are both omitted.
- `parent` — the key of the parent issue (the Epic of a child issue, or the
  parent of a sub-task), e.g. `ENG-100`. Omitted when the issue has no parent.

Epic membership is read from `parent` only; the legacy Epic Link custom field is
not read (no tenant-specific custom field ids live in `pkg/pjira`).

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

### Rate-limit headers

Atlassian reports how much of the tenant's rate limit is left in response headers. pjira
conveys them for EVERY invocation on **stderr** (stdout is untouched, so a caller that
ignores the data sees exactly the output it saw before). For each HTTP response that
carries at least one of the headers below, pjira writes one line, as the response arrives
(so on a failure it comes before the error message):

```
pjira-ratelimit: {"status":200,"limit":100,"remaining":97,"near_limit":false}
pjira-ratelimit: {"status":429,"limit":100,"remaining":0,"near_limit":true,"retry_after_seconds":11,"reset":"2026-10-27T12:00:11Z","reason":"jira-burst-based"}
```

- **Format.** The literal prefix `pjira-ratelimit: ` (colon, one space), then ONE compact JSON
  object, then a newline. Select the lines by that prefix and ignore every other stderr line
  (errors, the `--all` truncation warning).
- **Fields.** Every field except `status` is omitted when its header is absent or
  unparsable, so "absent" is never confused with `0`:

  | JSON key              | Header                  | Type    | On                          |
  | --------------------- | ----------------------- | ------- | --------------------------- |
  | `status`              | (HTTP status code)      | integer | always                      |
  | `limit`               | `X-RateLimit-Limit`     | integer | every response              |
  | `remaining`           | `X-RateLimit-Remaining` | integer | every response              |
  | `near_limit`          | `X-RateLimit-NearLimit` | boolean | every response              |
  | `retry_after_seconds` | `Retry-After`           | integer | a 429 (some transient 5xx)  |
  | `reset`               | `X-RateLimit-Reset`     | string  | a 429 (the header verbatim) |
  | `reason`              | `RateLimit-Reason`      | string  | a 429 (the header verbatim) |

  `near_limit` is true below 20% capacity. `retry_after_seconds` is a MINIMUM delay per
  Atlassian; a fractional value is rounded up and an HTTP-date value is converted to seconds
  from now (never negative). `reason` is one of `jira-burst-based`,
  `jira-quota-tenant-based`, `jira-quota-global-based`, `jira-per-issue-on-write`.

- **One line per response, not per invocation.** `transition` makes two requests and
  `search --all` makes one per page, so they print several lines; the LAST line is the
  freshest reading. All error statuses (including 429) are covered, not only 2xx.
- **No headers, no line.** A tenant that sends none of them produces no `pjira-ratelimit:`
  line at all.
- **No secrets.** Only the headers above are copied; the line never carries the token,
  email, URL or any other header.

Library callers set `Client.OnRateLimit` (a `func(pjira.RateLimit)`) to receive the same
reading per response; `pjira.ParseRateLimit` parses one from a status and headers.
