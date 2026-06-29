# Generic Jira `search` Pagination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add cursor pagination to the generic `jira search` CLI so windowed collectors (SP5, SP6) get complete results instead of capping at one page.

**Architecture:** Three client layers — a private/public `SearchPage` primitive that sends/surfaces Atlassian's `nextPageToken`, a `SearchAll` loop built on it with a constant safety cap, and the existing `Search` refactored into a thin single-page delegate (byte-compatible with SP1). The CLI gains `--cursor` (single page) and `--all` (loop) plus a hidden `--max-pages` test seam.

**Tech Stack:** Go (stdlib `net/http`, `encoding/json`, `net/http/httptest`), `spf13/cobra`. Built via repo-base `mkGoBinary` (gomod2nix). No new dependencies.

## Global Constraints

- **Module location:** `modules/jira/` (package `jira` at `pkg/jira/`, CLI `main` at `cmd/jira/`). All paths below are relative to the repo-base worktree root.
- **Spec:** `docs/superpowers/specs/2026-06-29-generic-jira-search-pagination-design.md`.
- **Back-compat (SP4):** the existing `Search(...)` and the default-mode `{items, truncated}` envelope MUST remain byte-identical. `next_page_token` is the ONLY new key and MUST be `omitempty`.
- **Endpoint:** `POST /rest/api/3/search/jql` only (never the 410'd `/search`). Pagination is via the request-body `nextPageToken`; truncation is authoritative via `nextPageToken`/`isLast`, never a count.
- **Guardrail (SP1, `pkg/jira/guardrails_test.go`):** `pkg/jira` + `cmd/jira` MUST contain no ZR string (`ziprecruiter`, `zr-jira`), no OS command (`security find-generic-password`, `secret-tool`), and no `pg-pr` reference (`/pg-pr/`, `provider/issues`). New code MUST NOT introduce any of these tokens.
- **`--max-pages` is a HIDDEN flag** (`MarkHidden`), default `jira.DefaultMaxSearchPages` (=100). It exists only as a test seam + safety valve; it is NOT in `--help`, honoring the spec's "no user-facing flag" decision (§7).
- **Validation gate (mirrors SP1):** `go test ./...`, `go vet ./...`, `gofmt -l modules/jira` clean (run manually — repo-base `treefmt` has no Go formatter yet, tracked by pg2-2uat), `nix build .#jira`, repo-base `nix flake check`.
- **Branch:** `jira-search-pagination` (do NOT push). Commit after each task. No `Refs:` line (nix repo — not the ZR monorepo).

---

### Task 1: `SearchResult.NextPageToken` + extract `SearchPage` primitive

Refactor the existing single-page `Search` into a public `SearchPage(…, pageToken)` primitive that sends an inbound token and surfaces the response token; keep `Search` as a thin delegate so SP1 behavior is byte-identical.

**Files:**

- Modify: `modules/jira/pkg/jira/model.go` (add `NextPageToken` to `SearchResult`)
- Modify: `modules/jira/pkg/jira/client.go:143-216` (`Search` → `SearchPage` + delegate)
- Test: `modules/jira/pkg/jira/client_test.go` (new tests)

**Interfaces:**

- Produces: `SearchResult{Items []Issue; Truncated bool; NextPageToken string}`; `(*Client).SearchPage(ctx context.Context, jql string, limit int, exp ExpandOpts, pageToken string) (*SearchResult, error)`; `(*Client).Search(ctx, jql, limit, exp) (*SearchResult, error)` (unchanged signature, now delegates).
- Consumes: existing `ExpandOpts`, `Issue`, `(*Client).do`, `mapIssue`, `rawHistory`/`searchFields` (unchanged).

- [ ] **Step 1: Add the failing test for `SearchPage` token round-trip**

Add to `modules/jira/pkg/jira/client_test.go`:

```go
func TestSearchPage_sendsTokenAndSurfacesNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["nextPageToken"] != "PAGE2" {
			t.Errorf("request nextPageToken = %v, want PAGE2", body["nextPageToken"])
		}
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-9","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"PAGE3"}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).SearchPage(context.Background(), "project = ENG", 100, ExpandOpts{}, "PAGE2")
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if got.NextPageToken != "PAGE3" {
		t.Errorf("NextPageToken = %q, want PAGE3", got.NextPageToken)
	}
	if !got.Truncated || len(got.Items) != 1 || got.Items[0].Key != "ENG-9" {
		t.Errorf("bad result: %+v", got)
	}
}

func TestSearch_firstPageOmitsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["nextPageToken"]; present {
			t.Errorf("Search() must NOT send nextPageToken on the first page; body=%v", body)
		}
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()
	got, err := testClient(srv).Search(context.Background(), "project = ENG", 100, ExpandOpts{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Truncated || got.NextPageToken != "" {
		t.Errorf("complete page must be untruncated with empty token: %+v", got)
	}
}
```

Add `"encoding/json"` to the `client_test.go` import block (place it sorted, or add it anywhere and run `gofmt -w client_test.go` — `gofmt` orders the std imports `context`, `encoding/base64`, `encoding/json`, …).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd modules/jira && go test ./pkg/jira/ -run 'TestSearchPage_sendsTokenAndSurfacesNext|TestSearch_firstPageOmitsToken' -v`
Expected: FAIL — this is a **package compile failure** (`SearchPage` and `SearchResult.NextPageToken` do not exist until Steps 3–4), which Go reports as a failed test build. That compile-failure red state is the expected TDD failure.

- [ ] **Step 3: Add `NextPageToken` to `SearchResult`**

In `modules/jira/pkg/jira/model.go`, change the `SearchResult` type to:

```go
// SearchResult is the search envelope: mapped items, an authoritative truncation
// flag, and the token to fetch the next page (empty when last/complete).
type SearchResult struct {
	Items         []Issue `json:"items"`
	Truncated     bool    `json:"truncated"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}
```

- [ ] **Step 4: Rename `Search` → `SearchPage`, add the token, surface `NextPageToken`, re-add `Search` delegate**

In `modules/jira/pkg/jira/client.go`:

1. Change the method signature on line 143 from
   `func (c *Client) Search(ctx context.Context, jql string, limit int, exp ExpandOpts) (*SearchResult, error) {`
   to
   `func (c *Client) SearchPage(ctx context.Context, jql string, limit int, exp ExpandOpts, pageToken string) (*SearchResult, error) {`

2. Immediately after the `body := map[string]any{"jql": jql, "maxResults": limit, "fields": fields}` line, insert:

```go
	if pageToken != "" {
		body["nextPageToken"] = pageToken
	}
```

3. Change the final return (currently `return &SearchResult{Items: items, Truncated: truncated}, nil`) to:

```go
	return &SearchResult{Items: items, Truncated: truncated, NextPageToken: raw.NextPageToken}, nil
```

4. Add the thin back-compat delegate immediately above `SearchPage` (preserve the doc comment style of the file):

```go
// Search fetches the first page of a JQL search. It preserves the SP1 contract
// exactly (no nextPageToken sent); for multi-page collection use SearchAll.
func (c *Client) Search(ctx context.Context, jql string, limit int, exp ExpandOpts) (*SearchResult, error) {
	return c.SearchPage(ctx, jql, limit, exp, "")
}
```

- [ ] **Step 5: Run the new tests + the full package suite**

Run: `cd modules/jira && go test ./pkg/jira/ -v`
Expected: PASS — including the pre-existing `TestSearch_mapsItemsExpandAndTruncation` and `TestSearch_emptyJQLErrors` (back-compat intact).

- [ ] **Step 6: Commit**

```bash
git add modules/jira/pkg/jira/model.go modules/jira/pkg/jira/client.go modules/jira/pkg/jira/client_test.go
git commit -m "feat(jira): SearchPage primitive + SearchResult.NextPageToken [pg2-2x2d.7]"
```

---

### Task 2: `SearchAll` loop with constant page cap

Add the multi-page loop on top of `SearchPage`, bounded by a constant safety cap.

**Files:**

- Modify: `modules/jira/pkg/jira/client.go` (add `DefaultMaxSearchPages` const + `SearchAll`)
- Test: `modules/jira/pkg/jira/client_test.go` (new tests)

**Interfaces:**

- Consumes: `(*Client).SearchPage` (Task 1).
- Produces: `const DefaultMaxSearchPages = 100`; `(*Client).SearchAll(ctx context.Context, jql string, limit int, exp ExpandOpts, maxPages int) (*SearchResult, error)`. On completion: `Truncated=false`, `NextPageToken=""`. On cap-hit: `Truncated=true`, `NextPageToken=` first unfetched token.

- [ ] **Step 1: Write the failing tests for `SearchAll`**

Add to `modules/jira/pkg/jira/client_test.go`. The fixture server walks a 3-page chain keyed off the inbound `nextPageToken`:

```go
func paginatedSearchServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["nextPageToken"] {
		case nil, "":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-2","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p3"}`))
		case "p3":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-3","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`))
		default:
			t.Errorf("unexpected token %v", body["nextPageToken"])
		}
	}))
}

func TestSearchAll_concatenatesAllPages(t *testing.T) {
	srv := paginatedSearchServer(t)
	defer srv.Close()
	got, err := testClient(srv).SearchAll(context.Background(), "project = ENG", 100, ExpandOpts{}, DefaultMaxSearchPages)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(got.Items) != 3 || got.Items[0].Key != "ENG-1" || got.Items[2].Key != "ENG-3" {
		t.Fatalf("items: %+v", got.Items)
	}
	if got.Truncated || got.NextPageToken != "" {
		t.Errorf("complete run must be untruncated with empty token: %+v", got)
	}
}

func TestSearchAll_respectsMaxPages(t *testing.T) {
	srv := paginatedSearchServer(t)
	defer srv.Close()
	got, err := testClient(srv).SearchAll(context.Background(), "project = ENG", 100, ExpandOpts{}, 2)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(got.Items) != 2 {
		t.Errorf("want 2 items at maxPages=2, got %d", len(got.Items))
	}
	if !got.Truncated || got.NextPageToken != "p3" {
		t.Errorf("cap-hit must be truncated with the next token p3: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestSearchAll -v`
Expected: FAIL — `SearchAll undefined`, `DefaultMaxSearchPages undefined`.

- [ ] **Step 3: Implement `DefaultMaxSearchPages` + `SearchAll`**

Add to `modules/jira/pkg/jira/client.go` (below `SearchPage`):

```go
// DefaultMaxSearchPages bounds SearchAll so a runaway query cannot loop forever.
// At Atlassian's ~100-item per-page ceiling this is up to ~10k issues — generous
// for any window-bounded collector.
const DefaultMaxSearchPages = 100

// SearchAll follows nextPageToken across pages, concatenating items, until the
// last page or maxPages is reached (maxPages <= 0 falls back to the default cap).
// On full completion it returns Truncated=false and an empty NextPageToken; on a
// cap-hit it returns Truncated=true and the first unfetched token.
func (c *Client) SearchAll(ctx context.Context, jql string, limit int, exp ExpandOpts, maxPages int) (*SearchResult, error) {
	if maxPages <= 0 {
		maxPages = DefaultMaxSearchPages
	}
	all := make([]Issue, 0)
	token := ""
	for page := 0; page < maxPages; page++ {
		res, err := c.SearchPage(ctx, jql, limit, exp, token)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if res.NextPageToken == "" {
			return &SearchResult{Items: all, Truncated: false}, nil
		}
		token = res.NextPageToken
	}
	return &SearchResult{Items: all, Truncated: true, NextPageToken: token}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd modules/jira && go test ./pkg/jira/ -v`
Expected: PASS (all `TestSearchAll*` plus the existing suite).

- [ ] **Step 5: Commit**

```bash
git add modules/jira/pkg/jira/client.go modules/jira/pkg/jira/client_test.go
git commit -m "feat(jira): SearchAll page loop with constant safety cap [pg2-2x2d.7]"
```

---

### Task 3: CLI `--cursor` / `--all` flags, envelope, mutual exclusion, cap warning

Wire the two front doors into `jira search`, add the hidden `--max-pages` test seam, and emit a stderr warning on cap-hit.

**Files:**

- Modify: `modules/jira/cmd/jira/main.go:101-138` (`newSearchCmd`)
- Test: `modules/jira/cmd/jira/main_test.go` (new tests + a stderr-capturing helper)

**Interfaces:**

- Consumes: `jira.DefaultMaxSearchPages`, `(*Client).SearchPage`, `(*Client).SearchAll` (Tasks 1–2); existing `newClient`, `writeJSON`.
- Produces (CLI contract): `jira search --jql … [--limit N] [--expand …] [--cursor TOKEN] [--all]`. `--cursor`+`--all` → error, no envelope. `--all` cap-hit → partial envelope (`truncated:true`), one-line stderr warning, exit 0.

- [ ] **Step 1: Write the failing CLI tests + a stderr helper**

Add to `modules/jira/cmd/jira/main_test.go` (reuse the existing `runCLI`; add `runCLIOutErr` for the stderr assertion):

```go
func runCLIOutErr(t *testing.T, baseURL string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", baseURL)
	t.Setenv("JIRA_EMAIL", "u@x")
	t.Setenv("JIRA_API_TOKEN", "tok")
	cmd := NewRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestCLI_SearchAll_aggregatesPages(t *testing.T) {
	srv := paginatedSearchServerCLI(t)
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "search", "--jql", "project = ENG", "--all")
	if err != nil {
		t.Fatalf("search --all: %v", err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
		Next      string           `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Items) != 3 || got.Truncated || got.Next != "" {
		t.Errorf("bad aggregated envelope: %+v", got)
	}
}

func TestCLI_SearchCursorEmitsNextToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p2"}`))
	}))
	defer srv.Close()
	out, err := runCLI(t, srv.URL, "search", "--jql", "project = ENG", "--cursor", "p1")
	if err != nil {
		t.Fatalf("search --cursor: %v", err)
	}
	var got struct {
		Truncated bool   `json:"truncated"`
		Next      string `json:"next_page_token"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	if !got.Truncated || got.Next != "p2" {
		t.Errorf("cursor page must surface next_page_token=p2, truncated=true: %+v", got)
	}
}

func TestCLI_SearchCursorAndAllConflict(t *testing.T) {
	out, err := runCLI(t, "http://unused", "search", "--jql", "project = ENG", "--cursor", "p1", "--all")
	if err == nil {
		t.Fatal("want error when --cursor and --all are combined")
	}
	if out != "" {
		t.Errorf("no envelope must be written on usage error, got: %s", out)
	}
}

func TestCLI_SearchAll_capHitWarnsAndSucceeds(t *testing.T) {
	srv := paginatedSearchServerCLI(t)
	defer srv.Close()
	out, errOut, err := runCLIOutErr(t, srv.URL, "search", "--jql", "project = ENG", "--all", "--max-pages", "2")
	if err != nil {
		t.Fatalf("cap-hit must exit 0, got err: %v", err)
	}
	var got struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
	}
	_ = json.Unmarshal([]byte(out), &got)
	if len(got.Items) != 2 || !got.Truncated {
		t.Errorf("cap-hit envelope must be partial+truncated: %+v", got)
	}
	if !strings.Contains(errOut, "truncated") {
		t.Errorf("expected a stderr truncation warning, got: %q", errOut)
	}
}
```

Add a CLI-local copy of the 3-page fixture (the package-private `paginatedSearchServer` lives in `pkg/jira`, a different package). Place in `main_test.go`:

```go
func paginatedSearchServerCLI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["nextPageToken"] {
		case nil, "":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-1","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-2","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"nextPageToken":"p3"}`))
		case "p3":
			_, _ = w.Write([]byte(`{"issues":[{"key":"ENG-3","fields":{"summary":"S","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`))
		default:
			t.Errorf("unexpected token %v", body["nextPageToken"])
		}
	}))
}
```

Ensure `main_test.go` imports include `"strings"` (for the warning assertion); `bytes`, `encoding/json`, `net/http`, `net/http/httptest` are already used by the existing tests.

- [ ] **Step 2: Run to verify failure**

Run: `cd modules/jira && go test ./cmd/jira/ -run 'TestCLI_Search' -v`
Expected: FAIL — `--cursor`/`--all`/`--max-pages` flags unknown; `next_page_token`/warning behavior absent.

- [ ] **Step 3: Rewrite `newSearchCmd`**

Replace `newSearchCmd` in `modules/jira/cmd/jira/main.go` with:

```go
func newSearchCmd() *cobra.Command {
	var jql, expand, cursor string
	var limit, maxPages int
	var all bool
	c := &cobra.Command{
		Use:   "search",
		Short: "JQL search; writes {items,truncated,next_page_token?} JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(jql) == "" {
				return fmt.Errorf("jira search: --jql is required")
			}
			if all && strings.TrimSpace(cursor) != "" {
				return fmt.Errorf("jira search: --all and --cursor are mutually exclusive")
			}
			cl, cfg, err := newClient(cmd)
			if err != nil {
				return err
			}
			if limit == 0 {
				limit = cfg.DefaultLimit
			}
			var exp jira.ExpandOpts
			for _, e := range strings.Split(expand, ",") {
				switch strings.TrimSpace(e) {
				case "changelog":
					exp.Changelog = true
				case "comments":
					exp.Comments = true
				}
			}
			if all {
				res, err := cl.SearchAll(cmd.Context(), jql, limit, exp, maxPages)
				if err != nil {
					return err
				}
				if res.Truncated {
					fmt.Fprintf(cmd.ErrOrStderr(), "jira search: result truncated at max-pages=%d (%d items returned; more remain)\n", maxPages, len(res.Items))
				}
				return writeJSON(cmd, res)
			}
			res, err := cl.SearchPage(cmd.Context(), jql, limit, exp, strings.TrimSpace(cursor))
			if err != nil {
				return err
			}
			return writeJSON(cmd, res)
		},
	}
	c.Flags().StringVar(&jql, "jql", "", "JQL query (required)")
	c.Flags().IntVar(&limit, "limit", 0, "max results per page (0 = config default)")
	c.Flags().StringVar(&expand, "expand", "", "comma-separated: changelog,comments")
	c.Flags().StringVar(&cursor, "cursor", "", "fetch the single page at this nextPageToken")
	c.Flags().BoolVar(&all, "all", false, "fetch all pages (loops nextPageToken to completeness)")
	c.Flags().IntVar(&maxPages, "max-pages", jira.DefaultMaxSearchPages, "safety cap on pages fetched by --all")
	_ = c.Flags().MarkHidden("max-pages")
	return c
}
```

- [ ] **Step 4: Run the CLI tests to verify they pass**

Run: `cd modules/jira && go test ./cmd/jira/ -v`
Expected: PASS — new tests plus the pre-existing `TestCLI_Search` (default mode still `{items,truncated}`, untruncated).

- [ ] **Step 5: Commit**

```bash
git add modules/jira/cmd/jira/main.go modules/jira/cmd/jira/main_test.go
git commit -m "feat(jira): search --cursor/--all flags, paginated envelope, cap warning [pg2-2x2d.7]"
```

---

### Task 4: README docs + full validation gate

Document the new surface and run the repo-base build/check gate.

**Files:**

- Modify: `modules/jira/README.md` (document `--cursor`/`--all`, the `next_page_token` envelope key, the cap behavior)

- [ ] **Step 1: Update `modules/jira/README.md`**

First, the existing one-line `search` bullet describes the envelope as `{items,truncated}` — update that description to `{items,truncated,next_page_token?}` so it stays accurate. Then add the following subsection (match the file's existing heading/format style):

```markdown
### Pagination

`jira search` returns one page by default (`{items, truncated, next_page_token?}`).
`next_page_token` is present when more pages remain.

- `--cursor <token>` — fetch the single page at `<token>` (the `next_page_token`
  from a previous call). Lets a caller drive the loop itself.
- `--all` — loop every page internally and return the complete, concatenated
  result (`truncated:false`). Bounded by an internal safety cap; on a cap-hit the
  envelope is partial with `truncated:true` and a warning is written to stderr
  (exit 0). `--all` and `--cursor` are mutually exclusive.
```

- [ ] **Step 2: Run the full package test + vet + format check**

Run:

```bash
cd modules/jira && go test ./... && go vet ./... && gofmt -l .
```

Expected: tests PASS, vet clean, `gofmt -l .` prints **nothing** (no unformatted files).

- [ ] **Step 3: Confirm the guardrail still holds**

Run: `cd modules/jira && go test ./pkg/jira/ -run TestNoForbiddenStrings -v`
Expected: PASS (no ZR/OS/pg-pr token introduced).

- [ ] **Step 4: Build the package and run repo-base flake check**

Run (from the worktree root):

```bash
nix build .#jira && nix flake check
```

Expected: both succeed. (`nix flake check` runs the `jira-go-tests` check via `mkGoBinary` `doCheck`.)

- [ ] **Step 5: Commit**

```bash
git add modules/jira/README.md
git commit -m "docs(jira): document search pagination (--cursor/--all) [pg2-2x2d.7]"
```

---

## Self-Review

**Spec coverage:**

- §3 hybrid `--cursor` + `--all` → Task 3 ✅
- §4 `SearchResult.NextPageToken`, `SearchPage`, `Search` delegate, `SearchAll` → Tasks 1–2 ✅
- §5 `--cursor`/`--all` flags, per-page `--limit`, mutual exclusion → Task 3 ✅
- §6 envelope shape (`next_page_token` omitempty; default byte-compat) → Tasks 1, 3 (`TestCLI_Search` unchanged) ✅
- §7 constant cap, partial+stderr-warn+exit 0 on cap-hit, mid-loop error propagation (no partial envelope) → Task 2 (`SearchAll`), Task 3 (warning) ✅; mid-loop error: `SearchAll` returns `nil, err` → CLI returns err → no envelope ✅
- §9 TDD tests (httptest chain, SearchPage/Search/SearchAll/cap/CLI/mutual-exclusion), guardrail green, validation gate → Tasks 1–4 ✅

**Placeholder scan:** No TBD/TODO; every code step shows complete code. ✅

**Type consistency:** `SearchPage(ctx, jql string, limit int, exp ExpandOpts, pageToken string)` and `SearchAll(…, maxPages int)` used identically across Tasks 1–3; `SearchResult.NextPageToken`/`next_page_token` consistent; `DefaultMaxSearchPages` defined in Task 2, consumed in Task 3. ✅

**Reviewer note (decision to confirm):** the hidden `--max-pages` flag is an addition beyond the spec's literal text (§7 said "no user-facing flag"). It is hidden from `--help` and defaults to the constant, so it serves as a test seam + safety valve without becoming user-facing. Flag if you disagree — the alternative is making the cap a package-level `var` overridable only from tests.
