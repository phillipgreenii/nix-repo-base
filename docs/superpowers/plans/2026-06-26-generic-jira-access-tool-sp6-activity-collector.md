# SP6 — activity-collector: Migrate Jira Collector to the Generic CLI

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`
> (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `activity-collector`'s direct REST + keychain + ADF code in
`internal/collector/jira/jira.go` with a shell-out to `jira search --jql "<JQL>" --expand
changelog,comments --limit 100`, then map the generic `{items,truncated}` envelope to the same
`activity.Activity` emissions without regressing. Delete the keychain lookup, the ADF walker
(`textFromADF`/`walkADF`), the raw Atlassian structs (`searchIssue`, `jiraComment`,
`changeHistory`, `changeAuthor`, `changeItem`, `searchResp`, `jiraTime`), and the `HTTPClient`
seam. Credentials move from the macOS Keychain to the `jira` CLI's edge config (SP2).

**Architecture:** `internal/collector/jira/jira.go` gains a `Commander` seam (injectable
function `func(ctx, args...) (stdout []byte, err error)`) replacing `HTTPClient` + `TokenLookup`.
Production uses `exec.CommandContext`. Tests inject a fake commander returning a
`jira.SearchResult`-shaped JSON fixture. The `Collector` struct shrinks: `ServerURL`,
`ProjectKeys`, `HTTP`, and `LookupToken`/`KeychainService` are all removed; only `UserEmail` and
`Commander` survive (plus any new `JiraBin` path override). All filtering logic (author email,
day-window) stays in Go — only the fetch mechanism changes.

**Tech Stack:** Go (activity-collector's existing module), `os/exec`, `encoding/json`, same
`go test` + `mkGoApp doCheck` + flake check gate as today. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §3 UJ-7,
§10 SP6 (Bead `pg2-2x2d`).

**Dependency:** SP6 REQUIRES SP2 to land first (SP2 ships the `jira` binary on PATH and writes
the edge `config.toml` for ZR credentials). activity-collector code itself is generic
(de-ZR'd) — no ZR strings enter this package. SP1 (the generic `pkg/jira` library + CLI)
MUST be shipped and the `jira` binary available at test/build time.

## Global Constraints

- **Location:** `phillipgreenii-nix-support-apps/packages/activity-collector/internal/collector/jira/`
- **Repo-generic:** this package MUST NOT contain `ziprecruiter`, `zr-jira`, `security
find-generic-password`, or any OS-specific command name. All of those live in the edge config
  (SP2). The `jira` binary path defaults to `"jira"` (found on PATH).
- **Preserve all three activity types:** `jira_issue_created`, `jira_issue_commented`,
  `jira_issue_status_changed` — same `ExternalID` scheme, same `Title`/`Body`/`Entities`/`Payload`
  shapes, same day-window + author-email filtering.
- **Delete** the HTTP seam (`HTTPClient`), the keychain seam (`TokenLookup`), the full set of raw
  Atlassian JSON structs, and both ADF functions (`textFromADF`, `walkADF`). The generic CLI
  already flattens ADF to text in `Comment.Body`.
- **Commander seam:** type `Commander func(ctx context.Context, argv []string) ([]byte, error)`;
  injected into `Collector`; defaults to `osCommander` (the real `exec.CommandContext` wrapper).
- **Config migration:** `JiraSource.KeychainService` in `internal/config/config.go` MUST be
  removed (or deprecated with a migration note); `ServerURL` and `KeychainService` fields on
  `Collector` are removed. See Open Design Decisions §ODD-4 for the keychain entry rename.
- **Pagination:** the current code uses `maxResults: 100` with no pagination. SP6 preserves that
  limit; see §ODD-6 for the pagination open question.
- **Build validation before "done":**
  - `cd packages/activity-collector && go test ./...` green
  - `nix build .#activity-collector` green
  - `nix flake check` (support-apps) green
  - `prek run --all-files` green (no pre-commit violations)
- **Commits:** work on a branch in `phillipgreenii-nix-support-apps`. Do not push.
- **Bead:** `pg2-2x2d` (the generic Jira access tool epic).

## Field Mapping: Generic Envelope → `activity.Activity`

The generic CLI emits `SearchResult{Items []Issue, Truncated bool}`. Each `Issue` carries:

| Generic field                  | Used for                                                                             |
| ------------------------------ | ------------------------------------------------------------------------------------ |
| `key`                          | `ExternalID` prefix, `EntityRef.ID`, `Title` prefix                                  |
| `summary`                      | `Title` body, `EntityRef.Title` suffix                                               |
| `created` (RFC3339)            | `jira_issue_created.OccurredAt`                                                      |
| `reporter.email`               | author-email filter for `jira_issue_created` (see §ODD-1)                            |
| `changelog[].field`            | always `"status"` (generic tool already filters); drives `jira_issue_status_changed` |
| `changelog[].from`, `.to`      | `Title`: `"KEY: from → to"`; `Payload.From`/`Payload.To`                             |
| `changelog[].author.email`     | author-email filter for `jira_issue_status_changed`                                  |
| `changelog[].at` (RFC3339)     | `jira_issue_status_changed.OccurredAt`                                               |
| `comments[].author.email`      | author-email filter for `jira_issue_commented`                                       |
| `comments[].body`              | `Activity.Body` (already ADF-flattened plain text)                                   |
| `comments[].created` (RFC3339) | `jira_issue_commented.OccurredAt`                                                    |

**Comment `ExternalID`:** the old code used `cm.ID` (the Jira comment UUID). The generic
`Comment` type does NOT carry an `id` field. SP6 MUST derive a stable substitute; the plan uses
`comments[].created` (RFC3339) as the disambiguator:
`fmt.Sprintf("%s:comment:%s", key, cm.Created)`. See §ODD-5.

**`jira_issue_created` author filter:** activity-collector today does NOT filter
`jira_issue_created` by author — it emits a created event if the issue's `created` timestamp
falls in the window, regardless of who created it. The JQL already constrains to
`creator = email`, so all returned issues are created by the user. SP6 preserves this
behavior (no per-issue `reporter.email` check is added). See §ODD-1.

**`Payload` shape:** the old code marshalled the full `searchIssue` or `jiraComment` or an
inline struct as the payload. SP6 marshals the generic `jira.Issue` (for created events) and
inline structs matching the old shapes for comment/status events. This keeps payload structure
consistent with what downstream consumers expect. See per-task notes below.

## File Structure (changes only)

```text
packages/activity-collector/
  internal/collector/jira/
    jira.go          ← REPLACE: drop HTTP/keychain/ADF/raw structs; add Commander seam
    jira_test.go     ← REPLACE: drop fakeHTTP; add fakeCommander; preserve assertions
  internal/config/
    config.go        ← MODIFY: remove KeychainService from JiraSource; add JiraBin
```

No new files; no new Go dependencies.

---

### Task 1: Introduce the `Commander` seam + wire `buildJQL` (no behavioral change)

**Files:**

- Modify: `internal/collector/jira/jira.go`

**Interfaces:**

- Produces: `type Commander func(ctx context.Context, argv []string) ([]byte, error)`; adds
  `Commander Commander` and `JiraBin string` fields to `Collector`; adds `osCommander` production
  impl. The existing `HTTP`/`LookupToken`/`KeychainService` fields stay (removed in Task 3).
  Exports `buildJQL` behavior unchanged.

**Why a separate task:** adding the seam before changing behavior lets the existing tests stay
green throughout this task, confirming the seam compiles without breaking anything.

- [ ] **Step 1: Write the failing test**

Append to `internal/collector/jira/jira_test.go`:

```go
// TestCommanderSeamExists verifies the Commander field and osCommander are present.
func TestCommanderSeamExists(t *testing.T) {
    c := &Collector{Commander: osCommander}
    assert.NotNil(t, c.Commander)
}
```

Run: `cd packages/activity-collector && go test ./internal/collector/jira/ -run TestCommanderSeamExists`
Expected: FAIL — `undefined: osCommander` (and `Commander` field missing).

- [ ] **Step 2: Add the Commander seam**

In `internal/collector/jira/jira.go`, add after the imports:

```go
// Commander executes argv and returns stdout. Injectable for tests; production
// uses osCommander.
type Commander func(ctx context.Context, argv []string) ([]byte, error)

// osCommander is the production Commander: exec.CommandContext, direct (no shell).
func osCommander(ctx context.Context, argv []string) ([]byte, error) {
    return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}
```

Add to `Collector` struct (alongside existing fields, not replacing them yet):

```go
// JiraBin is the path to the jira CLI binary. Defaults to "jira" (found on PATH).
JiraBin   string
// Commander executes the jira CLI. Defaults to osCommander.
Commander Commander
```

- [ ] **Step 3: Run all existing tests to verify nothing is broken**

Run: `cd packages/activity-collector && go test ./internal/collector/jira/`
Expected: all tests PASS (including `TestCommanderSeamExists`).

- [ ] **Step 4: Commit**

```bash
git add packages/activity-collector/internal/collector/jira/jira.go \
        packages/activity-collector/internal/collector/jira/jira_test.go
git commit -m "feat(activity-collector/jira): add Commander seam (no behavioral change) [pg2-2x2d]"
```

---

### Task 2: Replace `searchIssues` with CLI shell-out

**Files:**

- Modify: `internal/collector/jira/jira.go`

**Interfaces:**

- Consumes: `Commander`, `buildJQL`, `JiraBin`.
- Produces: replaces `func (c *Collector) searchIssues(ctx, httpc, token, jql)` with
  `func (c *Collector) runSearch(ctx context.Context, jql string) (SearchResult, error)` that
  shells out to `jira search --jql "<jql>" --expand changelog,comments --limit 100` and
  unmarshals `{items, truncated}` from stdout.
- Adds local `SearchResult` / `Issue` / `ChangelogEntry` / `Comment` / `User` types mirroring
  the generic model (these are the only structs activity-collector needs; all old raw Atlassian
  structs except `jiraTime` remain for now — removed in Task 3).

**Why a separate task:** `runSearch` is the new fetch seam; `Collect` still calls `searchIssues`
until Task 3 rewires it, so existing tests still pass after this task.

- [ ] **Step 1: Write the failing test**

Append to `internal/collector/jira/jira_test.go`:

```go
// fakeCommander returns a fixed JSON payload simulating `jira search` stdout.
func makeFakeCommander(payload string) Commander {
    return func(_ context.Context, _ []string) ([]byte, error) {
        return []byte(payload), nil
    }
}

func TestRunSearch_parsesEnvelope(t *testing.T) {
    payload := `{"items":[{"key":"PROJ-1","summary":"Test","status":"Open","issuetype":"Task","labels":[],"url":"https://example.atlassian.net/browse/PROJ-1","created":"2026-05-08T02:00:00Z","reporter":{"email":"u@example.com"},"changelog":[{"field":"status","from":"Open","to":"In Progress","author":{"email":"u@example.com"},"at":"2026-05-08T04:00:00Z"}],"comments":[{"author":{"email":"u@example.com"},"body":"looks good","created":"2026-05-08T03:00:00Z"}]}],"truncated":false}`

    c := &Collector{
        UserEmail: "u@example.com",
        Commander: makeFakeCommander(payload),
    }
    result, err := c.runSearch(context.Background(), "project = PROJ")
    require.NoError(t, err)
    require.Len(t, result.Items, 1)
    assert.Equal(t, "PROJ-1", result.Items[0].Key)
    require.Len(t, result.Items[0].Changelog, 1)
    assert.Equal(t, "status", result.Items[0].Changelog[0].Field)
    require.Len(t, result.Items[0].Comments, 1)
    assert.Equal(t, "looks good", result.Items[0].Comments[0].Body)
}

func TestRunSearch_commandError(t *testing.T) {
    c := &Collector{
        UserEmail: "u@example.com",
        Commander: func(_ context.Context, _ []string) ([]byte, error) {
            return nil, errors.New("jira: exit status 1")
        },
    }
    _, err := c.runSearch(context.Background(), "project = PROJ")
    require.Error(t, err)
    assert.Contains(t, err.Error(), "jira search")
}
```

Run: `cd packages/activity-collector && go test ./internal/collector/jira/ -run TestRunSearch`
Expected: FAIL — `undefined: (*Collector).runSearch`.

- [ ] **Step 2: Add local generic model types**

Add to `internal/collector/jira/jira.go` (these mirror the generic tool's output schema;
activity-collector owns its own copy — no import of `pkg/jira`):

```go
// genericUser mirrors pkg/jira.User from the generic tool's JSON output.
type genericUser struct {
    Email       string `json:"email,omitempty"`
    AccountID   string `json:"account_id,omitempty"`
    DisplayName string `json:"display_name,omitempty"`
}

// genericChangelogEntry mirrors pkg/jira.ChangelogEntry.
type genericChangelogEntry struct {
    Field  string      `json:"field"`
    From   string      `json:"from"`
    To     string      `json:"to"`
    Author genericUser `json:"author"`
    At     string      `json:"at"` // RFC3339
}

// genericComment mirrors pkg/jira.Comment (Body already ADF-flattened).
type genericComment struct {
    Author  genericUser `json:"author"`
    Body    string      `json:"body"`
    Created string      `json:"created"` // RFC3339
}

// genericIssue mirrors pkg/jira.Issue.
type genericIssue struct {
    Key       string                  `json:"key"`
    Summary   string                  `json:"summary"`
    Status    string                  `json:"status"`
    IssueType string                  `json:"issuetype"`
    Labels    []string                `json:"labels"`
    URL       string                  `json:"url"`
    Priority  string                  `json:"priority,omitempty"`
    Project   string                  `json:"project,omitempty"`
    Created   string                  `json:"created,omitempty"` // RFC3339
    Updated   string                  `json:"updated,omitempty"` // RFC3339
    Reporter  *genericUser            `json:"reporter,omitempty"`
    Assignee  *genericUser            `json:"assignee,omitempty"`
    Changelog []genericChangelogEntry `json:"changelog,omitempty"`
    Comments  []genericComment        `json:"comments,omitempty"`
}

// genericSearchResult mirrors pkg/jira.SearchResult.
type genericSearchResult struct {
    Items     []genericIssue `json:"items"`
    Truncated bool           `json:"truncated"`
}
```

- [ ] **Step 3: Implement `runSearch`**

Add to `internal/collector/jira/jira.go`:

```go
// jiraBin returns the configured jira binary name or "jira" as the default.
func (c *Collector) jiraBin() string {
    if c.JiraBin != "" {
        return c.JiraBin
    }
    return "jira"
}

// commander returns the configured Commander or osCommander as the default.
func (c *Collector) commander() Commander {
    if c.Commander != nil {
        return c.Commander
    }
    return osCommander
}

// runSearch shells out to `jira search --jql <jql> --expand changelog,comments --limit 100`
// and returns the parsed generic search result.
func (c *Collector) runSearch(ctx context.Context, jql string) (genericSearchResult, error) {
    argv := []string{
        c.jiraBin(),
        "search",
        "--jql", jql,
        "--expand", "changelog,comments",
        "--limit", "100",
    }
    out, err := c.commander()(ctx, argv)
    if err != nil {
        return genericSearchResult{}, fmt.Errorf("jira search: %w", err)
    }
    var result genericSearchResult
    if err := json.Unmarshal(out, &result); err != nil {
        return genericSearchResult{}, fmt.Errorf("jira search: parse response: %w", err)
    }
    return result, nil
}
```

- [ ] **Step 4: Run the new tests**

Run: `cd packages/activity-collector && go test ./internal/collector/jira/ -run TestRunSearch`
Expected: PASS.

- [ ] **Step 5: Run all jira tests (existing + new)**

Run: `cd packages/activity-collector && go test ./internal/collector/jira/`
Expected: all PASS (existing HTTP-based tests still pass because `Collect` still calls `searchIssues`).

- [ ] **Step 6: Commit**

```bash
git add packages/activity-collector/internal/collector/jira/jira.go \
        packages/activity-collector/internal/collector/jira/jira_test.go
git commit -m "feat(activity-collector/jira): add runSearch via Commander seam [pg2-2x2d]"
```

---

### Task 3: Rewrite `Collect` to use `runSearch`; delete old code

**Files:**

- Modify: `internal/collector/jira/jira.go`, `internal/collector/jira/jira_test.go`

**Interfaces:**

- `Collect` now calls `runSearch` instead of `searchIssues`. The `HTTP`, `LookupToken`, and
  `KeychainService` fields are removed from `Collector`. The old structs (`searchIssue`,
  `jiraComment`, `changeHistory`, `changeAuthor`, `changeItem`, `searchResp`, `jiraTime`) and
  functions (`keychainLookup`, `commentMatchesUser`, `historyMatchesUser`, `textFromADF`,
  `walkADF`, `searchIssues`, `safeURL`) are all deleted.
- `fakeHTTP` and `TestKeychainErrorIsSurfaced` are removed from the test file; a new
  `TestCollectFromCommanderFixture` replaces `TestCollectFromFixture`, asserting the same
  three activity types are emitted with the same filtering behavior.

**Field mapping detail (implemented in `Collect`):**

```
jira_issue_created:
  OccurredAt  ← time.Parse(time.RFC3339, issue.Created)
  ExternalID  ← fmt.Sprintf("%s:created", issue.Key)
  Title       ← issue.Summary
  Body        ← ""   (no body for created events)
  Payload     ← json.Marshal(issue)   [the full genericIssue]
  Entities[0] ← {Type:"jira_issue", ID:issue.Key, Title:issue.Key+" "+issue.Summary}
  Filter      ← issue.Created in [dayStart, dayEnd)

jira_issue_commented:
  OccurredAt  ← time.Parse(time.RFC3339, cm.Created)
  ExternalID  ← fmt.Sprintf("%s:comment:%s", issue.Key, cm.Created)  [see §ODD-5]
  Title       ← issue.Key + ": " + issue.Summary
  Body        ← truncate(cm.Body, 4000)   [already plain text]
  Payload     ← json.Marshal(struct{IssueKey, Author, Body, Created})
  Entities[0] ← {Type:"jira_issue", ID:issue.Key, Title:issue.Key+" "+issue.Summary}
  Filter      ← strings.EqualFold(cm.Author.Email, c.UserEmail)
               && cm.Created in [dayStart, dayEnd)

jira_issue_status_changed:
  OccurredAt  ← time.Parse(time.RFC3339, entry.At)
  ExternalID  ← fmt.Sprintf("%s:status:%s", issue.Key, entry.At)  [see §ODD-5]
  Title       ← fmt.Sprintf("%s: %s → %s", issue.Key, entry.From, entry.To)
  Body        ← ""
  Payload     ← json.Marshal(struct{Issue, From, To, Author, Changed})
  Entities[0] ← {Type:"jira_issue", ID:issue.Key, Title:issue.Key+" "+issue.Summary}
  Filter      ← strings.EqualFold(entry.Author.Email, c.UserEmail)
               && entry.At in [dayStart, dayEnd)
```

Note: the generic CLI already filters `changelog` to `field == "status"` entries only, so the
inner `if !strings.EqualFold(item.Field, "status")` loop in the old code simplifies to iterating
directly over `issue.Changelog`.

- [ ] **Step 1: Write the failing test (replacing the old fixture test)**

Replace the existing `TestCollectFromFixture` and `TestKeychainErrorIsSurfaced` in
`internal/collector/jira/jira_test.go` with a new `fakeCommander`-based test:

```go
func buildSearchPayload(
    day time.Time,
    created, commentTime, statusChange, yesterday time.Time,
) string {
    payload := genericSearchResult{
        Items: []genericIssue{
            {
                Key:      "PROJ-1",
                Summary:  "Test issue",
                Status:   "In Progress",
                Created:  created.Format(time.RFC3339),
                Reporter: &genericUser{Email: "u@example.com"},
                Changelog: []genericChangelogEntry{
                    {
                        Field:  "status",
                        From:   "Open",
                        To:     "In Progress",
                        Author: genericUser{Email: "u@example.com"},
                        At:     statusChange.Format(time.RFC3339),
                    },
                    {
                        Field:  "status",
                        From:   "Backlog",
                        To:     "Open",
                        Author: genericUser{Email: "u@example.com"},
                        At:     yesterday.Format(time.RFC3339), // outside window
                    },
                },
                Comments: []genericComment{
                    {
                        Author:  genericUser{Email: "u@example.com"},
                        Body:    "looks good",
                        Created: commentTime.Format(time.RFC3339),
                    },
                    {
                        Author:  genericUser{Email: "other@example.com"}, // filtered out
                        Body:    "someone else",
                        Created: commentTime.Format(time.RFC3339),
                    },
                },
            },
        },
        Truncated: false,
    }
    b, _ := json.Marshal(payload)
    return string(b)
}

func TestCollectFromCommanderFixture(t *testing.T) {
    day := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
    tomorrow := day.Add(24 * time.Hour)
    created := day.Add(2 * time.Hour)
    commentTime := day.Add(3 * time.Hour)
    statusChange := day.Add(4 * time.Hour)
    yesterday := day.Add(-24 * time.Hour)

    c := &Collector{
        UserEmail: "u@example.com",
        Commander: makeFakeCommander(buildSearchPayload(day, created, commentTime, statusChange, yesterday)),
    }

    acts, next, err := c.Collect(context.Background(), day, nil)
    require.NoError(t, err)

    // Expect: created + comment(self, in window) + status change(in window).
    // Other-author comment and yesterday's status are filtered.
    require.Len(t, acts, 3)
    types := []string{}
    for _, a := range acts {
        types = append(types, a.ActivityType)
        assert.Equal(t, "jira", a.Source)
    }
    assert.ElementsMatch(t, []string{
        "jira_issue_created",
        "jira_issue_commented",
        "jira_issue_status_changed",
    }, types)

    require.Len(t, next, 1)
    assert.Equal(t, "default", next[0].Scope)
    parsed, _ := time.Parse(time.RFC3339, next[0].Cursor)
    assert.True(t, parsed.After(day))
    assert.True(t, parsed.Before(tomorrow))
}

func TestCommanderErrorIsSurfaced(t *testing.T) {
    c := &Collector{
        UserEmail: "u@example.com",
        Commander: func(_ context.Context, _ []string) ([]byte, error) {
            return nil, errors.New("jira: exit status 1")
        },
    }
    _, _, err := c.Collect(context.Background(), time.Now().UTC(), nil)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "jira search")
}
```

Run: `cd packages/activity-collector && go test ./internal/collector/jira/ -run 'TestCollectFromCommanderFixture|TestCommanderErrorIsSurfaced'`
Expected: FAIL — `Collect` still calls `searchIssues` using the old HTTP path.

- [ ] **Step 2: Rewrite `Collect` to use `runSearch`**

Replace the body of `Collect` in `internal/collector/jira/jira.go`. The new implementation:

1. Computes `dayStart`/`dayEnd` (unchanged).
2. Validates `c.UserEmail` (still required; `ServerURL` validation removed).
3. Calls `c.runSearch(ctx, buildJQL(c.UserEmail, c.ProjectKeys, dayStart))`.
4. Iterates `result.Items` and applies the field-mapping table above.
5. Parses timestamps with `time.Parse(time.RFC3339, ...)` (the generic tool emits RFC3339;
   the `jiraTime` custom parser is deleted).
6. Returns `acts`, watermarks, and any error.

```go
func (c *Collector) Collect(ctx context.Context, day time.Time, prev []activity.Watermark) ([]activity.Activity, []activity.Watermark, error) {
    dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).UTC()
    dayEnd := dayStart.Add(24 * time.Hour)

    if c.UserEmail == "" {
        return nil, nil, fmt.Errorf("jira: user_email is required")
    }

    jql := buildJQL(c.UserEmail, c.ProjectKeys, dayStart)
    result, err := c.runSearch(ctx, jql)
    if err != nil {
        return nil, nil, err
    }

    var acts []activity.Activity
    latest := time.Time{}
    track := func(t time.Time) {
        if t.After(latest) {
            latest = t
        }
    }

    for _, iss := range result.Items {
        // Created event: issue.Created in [dayStart, dayEnd)
        // (JQL creator=email guarantees authorship; no extra author check needed)
        if iss.Created != "" {
            t, err := time.Parse(time.RFC3339, iss.Created)
            if err == nil && !t.Before(dayStart) && t.Before(dayEnd) {
                payload, _ := json.Marshal(iss)
                acts = append(acts, activity.Activity{
                    Source:       "jira",
                    ExternalID:   fmt.Sprintf("%s:created", iss.Key),
                    ActivityType: "jira_issue_created",
                    OccurredAt:   t.UTC(),
                    Title:        iss.Summary,
                    Payload:      payload,
                    Entities: []activity.EntityRef{
                        {Type: "jira_issue", ID: iss.Key, Title: iss.Key + " " + iss.Summary},
                    },
                })
                track(t)
            }
        }

        // Commented events: author == user, created in [dayStart, dayEnd)
        for _, cm := range iss.Comments {
            if !strings.EqualFold(cm.Author.Email, c.UserEmail) {
                continue
            }
            t, err := time.Parse(time.RFC3339, cm.Created)
            if err != nil || t.Before(dayStart) || !t.Before(dayEnd) {
                continue
            }
            cmPayload, _ := json.Marshal(struct {
                IssueKey string `json:"issue_key"`
                Author   genericUser `json:"author"`
                Body     string `json:"body"`
                Created  string `json:"created"`
            }{iss.Key, cm.Author, cm.Body, cm.Created})
            acts = append(acts, activity.Activity{
                Source:       "jira",
                ExternalID:   fmt.Sprintf("%s:comment:%s", iss.Key, cm.Created),
                ActivityType: "jira_issue_commented",
                OccurredAt:   t.UTC(),
                Title:        iss.Key + ": " + iss.Summary,
                Body:         truncate(cm.Body, 4000),
                Payload:      cmPayload,
                Entities: []activity.EntityRef{
                    {Type: "jira_issue", ID: iss.Key, Title: iss.Key + " " + iss.Summary},
                },
            })
            track(t)
        }

        // Status-changed events: generic CLI already filtered to field=="status";
        // filter by author email + window.
        for _, entry := range iss.Changelog {
            if !strings.EqualFold(entry.Author.Email, c.UserEmail) {
                continue
            }
            t, err := time.Parse(time.RFC3339, entry.At)
            if err != nil || t.Before(dayStart) || !t.Before(dayEnd) {
                continue
            }
            entryPayload, _ := json.Marshal(struct {
                Issue   string      `json:"issue"`
                From    string      `json:"from"`
                To      string      `json:"to"`
                Author  genericUser `json:"author"`
                Changed time.Time   `json:"changed"`
            }{iss.Key, entry.From, entry.To, entry.Author, t})
            acts = append(acts, activity.Activity{
                Source:       "jira",
                ExternalID:   fmt.Sprintf("%s:status:%s", iss.Key, entry.At),
                ActivityType: "jira_issue_status_changed",
                OccurredAt:   t.UTC(),
                Title:        fmt.Sprintf("%s: %s → %s", iss.Key, entry.From, entry.To),
                Payload:      entryPayload,
                Entities: []activity.EntityRef{
                    {Type: "jira_issue", ID: iss.Key, Title: iss.Key + " " + iss.Summary},
                },
            })
            track(t)
        }
    }

    var next []activity.Watermark
    if !latest.IsZero() {
        next = []activity.Watermark{{Scope: "default", Cursor: latest.UTC().Format(time.RFC3339)}}
    }
    return acts, next, nil
}
```

- [ ] **Step 3: Delete old code**

Remove from `internal/collector/jira/jira.go`:

- `HTTPClient` interface
- `TokenLookup` type
- Fields `ServerURL`, `HTTP`, `LookupToken`, `KeychainService` from `Collector`
- `searchIssues` method
- Struct types: `searchIssue`, `jiraComment`, `changeHistory`, `changeAuthor`, `changeItem`,
  `searchResp`, `jiraTime`
- Functions: `keychainLookup`, `commentMatchesUser`, `historyMatchesUser`, `textFromADF`,
  `walkADF`, `safeURL`

Remove from `internal/collector/jira/jira_test.go`:

- `fakeHTTP` struct and its `Do` method
- `TestKeychainErrorIsSurfaced` (replaced by `TestCommanderErrorIsSurfaced`)
- Any import of `net/http` / `io` that is now unused

Remove from the import block any of: `"bytes"`, `"io"`, `"net/http"`, `"net/url"`, `"os/exec"`
(exec moves to being imported only indirectly via `osCommander`; check if it's still needed).

Update the package-level comment to reflect the new auth mechanism.

- [ ] **Step 4: Run all tests**

Run: `cd packages/activity-collector && go test ./internal/collector/jira/`
Expected: all PASS.

- [ ] **Step 5: Run the full module**

Run: `cd packages/activity-collector && go test ./...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/activity-collector/internal/collector/jira/jira.go \
        packages/activity-collector/internal/collector/jira/jira_test.go
git commit -m "feat(activity-collector/jira): replace HTTP/keychain/ADF with Commander shell-out [pg2-2x2d]"
```

---

### Task 4: Update `config.go` — remove `KeychainService`, add `JiraBin`

**Files:**

- Modify: `internal/config/config.go`

**Interfaces:**

- `JiraSource.KeychainService` is removed (creds now live in the `jira` CLI's edge config).
- `JiraSource.ServerURL` is removed (server URL now lives in the `jira` CLI's edge config).
- `JiraSource.JiraBin` (`yaml:"jira_bin"`) is added — optional path to the `jira` binary;
  defaults to `"jira"` (found on PATH) when empty.
- The `Example` config constant is updated to remove the `keychain_service`/`server_url` lines
  and add a `jira_bin` comment.
- `Config.Validate()` is updated to remove any `ServerURL`-required check from the Jira source
  (if one existed — review the current validate method; none appears to exist today for
  Jira-specific fields, so this may be a no-op).

The caller that constructs `Collector` from config (find in `cmd/activity-collector/main.go` or
equivalent) MUST also be updated to stop passing `ServerURL`/`KeychainService` and to pass
`JiraBin` if set.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go` (create this file if it does not exist):

```go
package config

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestJiraSourceNoKeychainOrServerURL(t *testing.T) {
    // After SP6, JiraSource must not expose KeychainService or ServerURL.
    var s JiraSource
    _ = s.JiraBin      // must compile
    _ = s.Enabled
    _ = s.UserEmail
    _ = s.ProjectKeys
    // These fields must be gone:
    // s.KeychainService  // must NOT compile
    // s.ServerURL        // must NOT compile
}

func TestJiraSourceYAML_JiraBin(t *testing.T) {
    p := filepath.Join(t.TempDir(), "c.yaml")
    os.WriteFile(p, []byte(`
db_path: /tmp/x.db
timezone: UTC
user:
  email: u@x
llm:
  provider: claude_cli
  binary: claude
sources:
  jira:
    enabled: true
    user_email: u@x
    jira_bin: /opt/jira/bin/jira
`), 0o600)
    c, err := Load(p)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if c.Sources.Jira.JiraBin != "/opt/jira/bin/jira" {
        t.Errorf("JiraBin = %q", c.Sources.Jira.JiraBin)
    }
}

func TestExampleConfig_NoKeychainService(t *testing.T) {
    if strings.Contains(Example, "keychain_service") {
        t.Error("Example config still contains keychain_service — update it")
    }
    if strings.Contains(Example, "server_url") {
        t.Error("Example config still contains server_url — update it")
    }
}
```

Run: `cd packages/activity-collector && go test ./internal/config/ -run 'TestJiraSource|TestExampleConfig'`
Expected: FAIL — `JiraSource` has no `JiraBin` field; `KeychainService`/`ServerURL` still present.

- [ ] **Step 2: Update `JiraSource` in `config.go`**

Replace the `JiraSource` struct:

```go
type JiraSource struct {
    Enabled     bool     `yaml:"enabled"`
    UserEmail   string   `yaml:"user_email"`
    ProjectKeys []string `yaml:"project_keys"` // optional JQL filter
    // JiraBin is the path to the jira CLI binary. Defaults to "jira" (found on PATH).
    JiraBin string `yaml:"jira_bin"`
}
```

Update the `Example` constant's `jira:` section to remove `server_url`/`keychain_service` and
add a `jira_bin` comment block. The new section should read:

```yaml
jira:
  enabled: false
  user_email: you@example.com
  # jira_bin: /path/to/jira   # optional; defaults to "jira" on PATH
  project_keys: []
```

- [ ] **Step 3: Update the Collector construction call site**

Locate where `JiraSource` is used to build a `jira.Collector` (likely in
`cmd/activity-collector/main.go`). Update it to:

- Remove the `ServerURL` and `KeychainService` assignments (fields gone).
- Add `JiraBin: cfg.Sources.Jira.JiraBin` (forwarded to `Collector.JiraBin`).

- [ ] **Step 4: Run all tests**

Run: `cd packages/activity-collector && go test ./...`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/activity-collector/internal/config/config.go \
        packages/activity-collector/internal/config/config_test.go
# include the call-site update file
git commit -m "feat(activity-collector/config): remove JiraSource.KeychainService+ServerURL, add JiraBin [pg2-2x2d]"
```

---

### Task 5: Wire `jira` binary into the nix build + full validation

**Files:**

- Modify: `packages/activity-collector/default.nix`

**Interfaces:**

- `jira` binary (from SP1/SP2) MUST be on `$PATH` at runtime so `osCommander` finds it.
- `postInstall` `wrapProgram` in `default.nix` MUST include `pkgs.jira` in `makeBinPath`.
- The nix build (`nix build .#activity-collector`) MUST succeed.
- `nix flake check` (support-apps) MUST pass.

**Note:** `pkgs.jira` is available in support-apps via the repo-base overlay (shipped in SP1).
This task cannot fully complete until SP1 is merged and the overlay is updated. If running
ahead of SP1 merge, add `jira` to the `wrapProgram` line anyway (it will fail `nix flake check`
with a missing package error) and note it is SP1-blocked. The Go tests (Task 1–4) do NOT
require the real binary — they use `fakeCommander`.

- [ ] **Step 1: Update `default.nix`**

In `packages/activity-collector/default.nix`, update the `wrapProgram` line from:

```nix
      --prefix PATH : ${
        lib.makeBinPath [
          pkgs.git
          pkgs.gh
        ]
      }
```

to:

```nix
      --prefix PATH : ${
        lib.makeBinPath [
          pkgs.git
          pkgs.gh
          pkgs.jira
        ]
      }
```

- [ ] **Step 2: Verify the Go build still compiles**

Run: `cd packages/activity-collector && go build ./...`
Expected: PASS (the nix store path for `jira` doesn't affect the Go compiler).

- [ ] **Step 3: Run all gates**

Run (in order):

```bash
cd packages/activity-collector && go test ./...
prek run --all-files
nix build .#activity-collector   # requires SP1 overlay
nix flake check                  # requires SP1 overlay
```

Expected: all green. If `pkgs.jira` is not yet available from SP1/SP2, note the `nix build`
failure and mark this task blocked on SP1.

- [ ] **Step 4: Commit**

```bash
git add packages/activity-collector/default.nix
git commit -m "feat(activity-collector): wire pkgs.jira into PATH (SP1-dep) [pg2-2x2d]"
```

---

### Task 6: Guardrail test — no keychain/OS/ZR strings in the collector

**Files:**

- Modify: `internal/collector/jira/jira_test.go`

**Interfaces:**

- A test scans `jira.go` and `jira_test.go` for strings that indicate the old ZR/keychain/OS
  coupling and fails if any are present. This mirrors the guardrail gate in SP1 Task 10 but
  is scoped to this package.

- [ ] **Step 1: Write the guardrail test**

Append to `internal/collector/jira/jira_test.go`:

```go
func TestNoForbiddenStringsInCollector(t *testing.T) {
    forbidden := []string{
        "ziprecruiter",
        "zr-jira",
        "activity-collector-jira",
        "security find-generic-password",
        "keychain",
        "net/http",
    }
    files := []string{"jira.go"}
    for _, f := range files {
        b, err := os.ReadFile(f)
        if err != nil {
            t.Fatalf("read %s: %v", f, err)
        }
        low := strings.ToLower(string(b))
        for _, tok := range forbidden {
            if strings.Contains(low, strings.ToLower(tok)) {
                t.Errorf("%s contains forbidden token %q (collector must be generic — no keychain/ZR/OS strings)", f, tok)
            }
        }
    }
}
```

- [ ] **Step 2: Run the test**

Run: `cd packages/activity-collector && go test ./internal/collector/jira/ -run TestNoForbiddenStrings`
Expected: PASS. If it fails, a forbidden token leaked back in — remove it; do not weaken the gate.

- [ ] **Step 3: Run the full suite one final time**

Run: `cd packages/activity-collector && go test ./...`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add packages/activity-collector/internal/collector/jira/jira_test.go
git commit -m "test(activity-collector/jira): guardrail — no keychain/ZR/OS strings in collector [pg2-2x2d]"
```

---

## Self-Review

**Spec coverage** (against `2026-06-26-generic-jira-access-tool-design.md` §3 UJ-7, §10 SP6):

- Delete REST + keychain + ADF — Tasks 3, 4. ✓
- Shell-out to `jira search --expand changelog,comments` — Task 2. ✓
- Map generic Issue.changelog → `jira_issue_status_changed` — Task 3 (field mapping). ✓
- Map generic Issue.comments → `jira_issue_commented` — Task 3 (field mapping). ✓
- Map Issue.created → `jira_issue_created` — Task 3 (field mapping). ✓
- ADF already flattened by generic CLI — ADF walker deleted in Task 3. ✓
- Day-window + user-email filtering preserved in Go — Task 3. ✓
- Replace `fakeHTTP` seam with `fakeCommander` seam — Tasks 1, 3. ✓
- Config migration (`KeychainService`/`ServerURL` removed, `JiraBin` added) — Task 4. ✓
- Nix build wires `pkgs.jira` onto PATH — Task 5. ✓
- Guardrail test (no ZR/keychain/OS strings) — Task 6. ✓
- Generic (no ZR strings in Go code) — Global Constraints. ✓
- Dependency on SP2 noted — Global Constraints. ✓

**Placeholder scan:** no `TODO`, `TBD`, `add error handling`, or `similar to Task N` in the
implementation steps above.

**Type consistency:** `Commander`, `genericUser`, `genericChangelogEntry`, `genericComment`,
`genericIssue`, `genericSearchResult`, `osCommander`, `runSearch`, `jiraBin`, `commander` are
used consistently across Tasks 1–6.

---

## Resolved Decisions (settled 2026-06-29; supersedes the Open Design Decisions below)

Implemented in `phillipgreenii-nix-support-apps` (collector) + `phillipg-nix-repo-base`
(`pkg/jira` id extension) on branch `jira-sp5-sp6`.

- **ODD-1 created filter:** preserve JQL-`creator` only — no per-issue `reporter.email` guard.
- **ODD-2 author filter:** email-equality only — no `account_id` fallback, no new config field
  (ZR always has email).
- **ODD-3 status-only changelog:** confirmed (only `jira_issue_status_changed` is emitted); a
  defensive `entry.Field == "status"` guard is kept even though the CLI already filters.
- **ODD-4 keychain:** collector drops all keychain code; old `activity-collector-jira` entry
  orphaned (unified `zr-jira`, decision #4). Cleanup tracked in `pg2-4jy6`.
- **ODD-5 ExternalID → EXTEND `pkg/jira` (user decision):** repo-base `pkg/jira.Comment` +
  `ChangelogEntry` now carry a stable `id` (Jira comment / changelog-history id), populated in
  `SearchPage`. The collector's local mirror structs read it, so ExternalIDs stay **byte-identical**
  to the old scheme (`KEY:comment:<cm.ID>`, `KEY:status:<entry.ID>`) — no timestamp-derived ids.
- **ODD-6 pagination → REWORKED (decision #3):** `runSearch` runs
  `jira search --jql <jql> --expand changelog,comments --all` (loop to completeness), superseding
  the draft's `--limit 100`.

**Implementation deltas from the draft:** (a) **Task 5 PATH → ambient PATH (decision #2):** the
collector relies on the ambient profile PATH for `jira` (like pr-pool/SP4); **no `pkgs.jira` in
`makeBinPath`**, so `default.nix` is unchanged and there is no SP1 build coupling. (b) timestamps are
parsed with a robust `parseJiraTime` covering Jira's **no-colon offset** (`…-0700`) — the draft's
`time.Parse(time.RFC3339, …)` would fail on the live CLI output. Gate: `go test ./...` + `go vet`
green; guardrail test asserts no keychain/ZR/net-http strings.

## Open Design Decisions

### ODD-1: `jira_issue_created` — reporter vs. JQL creator filter

The old code emits a `jira_issue_created` event for any issue whose `created` timestamp falls in
the window, relying entirely on the JQL `creator = email` clause to scope to the user's issues.
It does NOT additionally check `reporter.email` on each issue.

The generic tool returns `reporter` in the Issue envelope. SP6 preserves the old behavior
(JQL-only filter, no per-issue reporter check). However: if the JQL `creator` and the `reporter`
email can diverge (e.g. a Jira bot creates an issue on behalf of a user, setting `reporter` but
not `creator`), the two approaches yield different results. **Decision needed:** should SP6 add a
`strings.EqualFold(issue.Reporter.Email, c.UserEmail)` guard for belt-and-suspenders, or
continue relying solely on JQL?

### ODD-2: Changelog author — `email` vs. `account_id` for filtering

The old code filtered changelog histories by `author.emailAddress`. The generic `User` type
carries `email`, `account_id`, and `display_name`. SP6 continues filtering by `email`
(`strings.EqualFold(entry.Author.Email, c.UserEmail)`). If the generic tool does not always
populate `email` (e.g. for guest/service accounts where Atlassian omits `emailAddress`), the
filter would silently drop real events. **Decision needed:** should SP6 add a fallback filter
path using `account_id` if `email` is empty, and if so, does `activity-collector`'s config need
an `account_id` field alongside `user_email`?

### ODD-3: Generic tool's status-only changelog filter — coverage gap?

The generic CLI's `Search` implementation (SP1 Task 5) already filters `changelog` histories to
`field == "status"` entries. The old activity-collector code had the same filter (`if
!strings.EqualFold(item.Field, "status") { continue }`). These are aligned. **Verify:** does
activity-collector need any other changelog field (e.g. `assignee`, `priority` changes) that
would require either (a) weakening the generic tool's filter to pass all fields, or (b) a
separate expand option? Current answer is no — only `jira_issue_status_changed` is emitted —
but this should be confirmed before finalizing SP6.

### ODD-4: Keychain entry migration — `activity-collector-jira` → `zr-jira`

The old collector used the `activity-collector-jira` macOS Keychain service name. SP2 (ZR edge)
creates a unified `zr-jira` entry. The user's existing `activity-collector-jira` entry is
superseded. **Decision needed (SP2 scope, not SP6 code):** does SP2 need a migration script that
copies the existing `activity-collector-jira` password to the `zr-jira` entry, or does the user
re-enter the token manually? SP6 itself is agnostic (no keychain code), but the edge config and
SP2 plan should document the migration path.

### ODD-5: Comment `ExternalID` — `cm.ID` (old) vs. `cm.Created` (new)

The old code used the Jira comment UUID (`cm.ID`) for `ExternalID`:
`fmt.Sprintf("%s:comment:%s", key, cm.ID)`. The generic `Comment` type does NOT carry `id`.
SP6 proposes `fmt.Sprintf("%s:comment:%s", key, cm.Created)` (RFC3339 timestamp). This is
stable for any given comment, but two comments made in the same second (extremely rare) would
collide. **Decision needed:** is the `cm.Created`-based ID acceptable, or should the generic
tool be extended to carry `comment.id`? Adding `id` to `pkg/jira.Comment` would be a small
backwards-compatible additive change to SP1's model. Similarly, `jira_issue_status_changed`
old `ExternalID` used `h.ID` (history UUID); SP6 proposes `entry.At` (RFC3339). Same question
applies.

### ODD-6: Pagination beyond one page

The current collector calls `maxResults: 100` with no pagination loop. SP6 preserves this
(one call, `--limit 100`). If a user touched more than 100 issues in a day (unlikely but
possible for large orgs), events are silently truncated. The `Truncated: true` field in the
response could be used to either: (a) log a warning, (b) return an error, or (c) implement a
pagination loop (requires multiple CLI invocations or a `--offset` flag in the generic tool,
which it does not currently support). **Decision needed:** what is the correct behavior when
`truncated == true`? At minimum, SP6 SHOULD log a warning rather than silently truncating.
