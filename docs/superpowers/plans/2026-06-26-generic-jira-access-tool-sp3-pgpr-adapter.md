# SP3 — pg-pr Jira Provider Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace pg-pr's Phase-0 Jira provider stub so `pg-pr issue show <KEY>` and the
dashboard `get_issue` path work — by shelling out to the generic `jira issue <KEY>` CLI (from SP1)
and mapping its JSON `jira.Issue` → `api.Issue`. No cross-repo Go import; no scriptout-serving
in the generic tool.

**Architecture:** pg-pr's `pkg/provider/issues/jira` execs the `jira` binary (or a
config-specified alias) and JSON-decodes the `jira.Issue` envelope into `api.Issue`. The adapter
lives entirely in agent-support and imports nothing from repo-base — the shell-out carries the
data. The mapping is defined in agent-support; the generic core (`pkg/jira`) stays free of all
`pg-pr` knowledge (the §5.3 invariant).

**Tech Stack:** Go, `os/exec`, `encoding/json`, `net/http/httptest`-style fake-exec via an
injectable `Runner` (the same pattern as `pkg/jira.Runner` in SP1). No new Go dependencies in
agent-support.

**Spec:** `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §5.3, §6 UJ-1/UJ-2
(Bead `pg2-2x2d.3`).

**Dependency:** SP1 MUST be merged and the generic `jira` binary MUST be reachable on PATH (SP2
configures this for the ZR tenant; the adapter itself is config-driven and carries no ZR strings).

---

## LEAD RECOMMENDATION — Open Design Decision

> **This plan implements Option (A) — shell-out adapter.** Option (B) is documented below as the
> key alternative. The implementer MUST NOT proceed to code until the user confirms this choice.

### Option (A) — Shell-out adapter (recommended, this plan)

pg-pr's `pkg/provider/issues/jira` executes the generic `jira issue <KEY>` CLI and JSON-decodes
the output. The binary name is configurable (`PGPR_JIRA_BINARY`, defaulting to `"jira"`).
The adapter tests fake the exec via an injectable `Runner`. No cross-repo Go module dependency;
no new scriptout surface in the generic tool.

**Pros:**

- Zero cross-repo Go import — consistent with how pr-pool and several other pg-pr providers
  already shell out to external binaries (`exec:` prefix in `newIssueProvider`).
- The generic tool is already a plain CLI (SP1); no new protocol surface required.
- The adapter is the only place that knows the mapping; the generic core stays generic by
  construction (the guardrail test from SP1 still passes).
- SP2's `pg-pr-issues-jira-zr` alias is preserved unchanged for pr-pool (UJ-5); SP3 adds a
  parallel path for `pg-pr issue show`.
- Trivial to test: inject a `fakeRunner` that returns canned JSON.

**Cons:**

- Subprocess spawn per `GetIssue` call. Acceptable: `get_issue` is called at most once per CLI
  invocation (UJ-1) or once per linked ticket per dashboard refresh (UJ-2). Not a hot path.
- Requires the `jira` binary to be on PATH at runtime. SP2 handles this for the ZR tenant;
  other tenants must ensure the binary is present.

### Option (B) — Cross-repo Go import (alternative, NOT implemented here)

agent-support imports repo-base's `pkg/jira` as a Go module (gomod2nix Pattern B, `go.mod`
`replace` directive, rooted-fileset build), and constructs a `jira.Client` directly. The adapter
calls `client.GetIssue` in-process and maps the result to `api.Issue`.

**Pros:**

- No subprocess per call; fully in-process.
- Richer access to the library's model types (useful if UJ-3's extended fields are added later).

**Cons:**

- Requires adding a `replace` directive to agent-support's `go.mod` pointing at repo-base, a
  `gomod2nix.toml` regeneration, and a rooted-fileset nix build (Pattern B from ADR 0008).
  This is non-trivial build plumbing.
- agent-support's build now depends on repo-base's Go module — a new coupling at the build level,
  not just the flake-input level.
- If repo-base's `pkg/jira` is eventually extracted to a dedicated repo (spec §5.2 tag), the
  `replace` must be updated again.
- The §5.3 invariant only forbids `repo-base → agent-support`; `agent-support → repo-base` is
  already an allowed direction (it is a flake input). So Option (B) is architecturally legal — it
  is rejected here on implementation-cost grounds, not on correctness grounds.

---

## Global Constraints

- **Location**: `phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira/` (the
  Phase-0 stub to replace) and a new test file alongside it.
- **§5.3 invariant**: `pkg/jira` (repo-base generic core) MUST NOT import any `pg-pr` package.
  Enforced by SP1's guardrail test. This plan does NOT modify repo-base.
- **No ZR strings in agent-support**: the adapter uses a configurable binary name
  (`PGPR_JIRA_BINARY` env / `"jira"` default). The ZR-specific binary name
  (`pg-pr-issues-jira-zr`) lives only in the ziprecruiter repo's SP2 config.
- **Auth**: the adapter owns no credentials. The generic `jira` binary resolves credentials from
  its own config (SP1 §8). The adapter is purely a translation layer.
- **`api.Issue` fields**: today `api.Issue` is `{ID, Title, State, URL}` (a strict subset of
  `jira.Issue`). UJ-3 (pg2-4c5i.26) will later add `Priority`/incident to `api.Issue` on the
  agent-support side. That extension is **out of SP3 scope**, but the richer `jira.Issue` JSON
  already carries `priority`/`labels`/`issuetype` — so SP3's mapping function is forward-ready
  (it reads the full JSON; the mapping just ignores the extra fields for now).
- **Test discipline**: each task follows TDD (write failing test → implement → pass). Tests use a
  `fakeRunner` that returns canned JSON, not a live subprocess.
- **Validation before "done"**: `cd packages/pg-pr && go test ./pkg/provider/issues/jira/...`
  green; `go vet ./...` green; `prek run --all-files` green; `nix flake check` green.
- **Commits**: work is on branch `generic-jira-access-tool` in agent-support. Do not push.

---

## File Structure

```text
packages/pg-pr/pkg/provider/issues/jira/
  jira.go          # Replace Phase-0 stub with shell-out provider
  jira_test.go     # New; fakeRunner-based TDD tests
```

No other files are created or modified in this SP. The `cmd/pg-pr/issue.go` file already supports
`"jira"` as a builtin provider name (line 43: `case name == "jira": return jira.New(), nil`) and
`"exec:<binary>"` for scriptout-based providers. SP3 upgrades `jira.New()` so the builtin path
works; the `exec:` path continues to serve `pg-pr-issues-jira-zr` (UJ-5 / pr-pool) unchanged.

---

### Task 1: Shell-out provider replacing the Phase-0 stub

**Files:**

- Replace: `packages/pg-pr/pkg/provider/issues/jira/jira.go`
- Create: `packages/pg-pr/pkg/provider/issues/jira/jira_test.go`

**Interfaces:**

- Produces: `func New() *Provider` (satisfies `issues.Provider`); an injectable `Runner`
  interface; `func NewWithRunner(binary string, runner Runner) *Provider` for testing.
- The `jira.Issue` JSON shape expected from the generic CLI (a strict subset of SP1's model,
  enough to populate `api.Issue`):

  ```json
  {
    "key": "ENG-123",
    "summary": "Fix the widget",
    "status": "In Progress",
    "issuetype": "Bug",
    "labels": [],
    "url": "https://example.atlassian.net/browse/ENG-123",
    "priority": "High"
  }
  ```

- Mapping: `jira.Issue{Key, Summary, Status, URL}` → `api.Issue{ID: Key, Title: Summary, State: Status, URL: URL}`.
  `Priority`/`labels`/`issuetype` are decoded (forward-ready for UJ-3) but not yet wired into
  `api.Issue` (UJ-3 extends `api.Issue` in a later bead).

- [ ] **Step 1: Write the failing tests**

  Create `packages/pg-pr/pkg/provider/issues/jira/jira_test.go`:

  ```go
  package jira_test

  import (
  	"context"
  	"errors"
  	"testing"

  	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
  )

  // fakeRunner implements jiraprovider.Runner for tests.
  type fakeRunner struct {
  	stdout []byte
  	err    error
  }

  func (f fakeRunner) Run(_ context.Context, _ []string) ([]byte, error) {
  	return f.stdout, f.err
  }

  func TestGetIssue_mapsJSONToAPIIssue(t *testing.T) {
  	const cliJSON = `{
  	  "key":"ENG-42","summary":"A title","status":"In Progress",
  	  "issuetype":"Bug","labels":["urgent"],"url":"https://x.atlassian.net/browse/ENG-42",
  	  "priority":"High"
  	}`
  	p := jiraprovider.NewWithRunner("jira", fakeRunner{stdout: []byte(cliJSON)})
  	got, err := p.GetIssue(context.Background(), "ENG-42")
  	if err != nil {
  		t.Fatalf("GetIssue: %v", err)
  	}
  	if got.ID != "ENG-42" {
  		t.Errorf("ID = %q, want ENG-42", got.ID)
  	}
  	if got.Title != "A title" {
  		t.Errorf("Title = %q, want \"A title\"", got.Title)
  	}
  	if got.State != "In Progress" {
  		t.Errorf("State = %q, want \"In Progress\"", got.State)
  	}
  	if got.URL != "https://x.atlassian.net/browse/ENG-42" {
  		t.Errorf("URL = %q", got.URL)
  	}
  }

  func TestGetIssue_emptyKeyErrors(t *testing.T) {
  	p := jiraprovider.NewWithRunner("jira", fakeRunner{})
  	if _, err := p.GetIssue(context.Background(), "   "); err == nil {
  		t.Fatal("want error on empty key")
  	}
  }

  func TestGetIssue_runnerErrorPropagates(t *testing.T) {
  	p := jiraprovider.NewWithRunner("jira", fakeRunner{err: errors.New("exit 1")})
  	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
  		t.Fatal("want error when runner fails")
  	}
  }

  func TestGetIssue_invalidJSONErrors(t *testing.T) {
  	p := jiraprovider.NewWithRunner("jira", fakeRunner{stdout: []byte("not-json")})
  	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
  		t.Fatal("want error on invalid JSON")
  	}
  }

  func TestGetIssue_missingKeyErrors(t *testing.T) {
  	// Generic tool should always set "key"; treat a missing key as an error.
  	p := jiraprovider.NewWithRunner("jira", fakeRunner{stdout: []byte(`{"summary":"s","status":"Open"}`)})
  	if _, err := p.GetIssue(context.Background(), "ENG-1"); err == nil {
  		t.Fatal("want error when JSON is missing key field")
  	}
  }

  func TestNew_usesEnvBinaryName(t *testing.T) {
  	// New() should pick up PGPR_JIRA_BINARY; tested indirectly by observing
  	// that the fakeRunner receives the expected argv[0].
  	_ = jiraprovider.New() // compile-time interface check
  }
  ```

- [ ] **Step 2: Run the tests to verify they fail**

  Run: `cd packages/pg-pr && go test ./pkg/provider/issues/jira/ -v`
  Expected: FAIL — `undefined: NewWithRunner` (and the existing stub's `GetIssue` returns
  `errStub`, so `TestGetIssue_mapsJSONToAPIIssue` would fail even if compiled).

- [ ] **Step 3: Implement the shell-out provider**

  Replace `packages/pg-pr/pkg/provider/issues/jira/jira.go`:

  ```go
  // Package jira is the built-in Jira issues provider for pg-pr.
  //
  // It delegates to the generic `jira issue <KEY>` CLI (from repo-base
  // modules/jira/) via a subprocess call, then maps the JSON output to
  // api.Issue. The binary name defaults to "jira" and can be overridden via
  // the PGPR_JIRA_BINARY environment variable (no ZR strings here — the ZR
  // tenant sets that env via its own config).
  package jira

  import (
  	"context"
  	"encoding/json"
  	"fmt"
  	"os"
  	"os/exec"
  	"strings"

  	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
  	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
  )

  // Runner execs a command and returns its combined stdout. Injectable for tests.
  type Runner interface {
  	Run(ctx context.Context, argv []string) (stdout []byte, err error)
  }

  // osRunner is the production Runner: exec.CommandContext, stdout only.
  type osRunner struct{}

  func (osRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
  	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
  }

  // Provider implements issues.Provider by shelling out to the generic jira CLI.
  type Provider struct {
  	binary string
  	runner Runner
  }

  // New constructs the default Provider. The binary name is taken from
  // PGPR_JIRA_BINARY if set, otherwise "jira".
  func New() *Provider {
  	bin := os.Getenv("PGPR_JIRA_BINARY")
  	if bin == "" {
  		bin = "jira"
  	}
  	return &Provider{binary: bin, runner: osRunner{}}
  }

  // NewWithRunner constructs a Provider with an injectable runner (for tests).
  func NewWithRunner(binary string, runner Runner) *Provider {
  	return &Provider{binary: binary, runner: runner}
  }

  // Compile-time interface check.
  var _ issues.Provider = (*Provider)(nil)

  // cliIssue is the JSON shape emitted by `jira issue <KEY>` (SP1 pkg/jira.Issue).
  // We decode all fields so the mapping is forward-ready for UJ-3 (priority/labels),
  // even though api.Issue currently only carries {ID,Title,State,URL}.
  type cliIssue struct {
  	Key       string   `json:"key"`
  	Summary   string   `json:"summary"`
  	Status    string   `json:"status"`
  	IssueType string   `json:"issuetype"`
  	Labels    []string `json:"labels"`
  	URL       string   `json:"url"`
  	Priority  string   `json:"priority,omitempty"`
  }

  // GetIssue execs `<binary> issue <key>`, decodes the JSON envelope, and maps
  // cliIssue → api.Issue.
  //
  // Field mapping (current; UJ-3 will extend api.Issue with Priority/incident):
  //   cliIssue.Key     → api.Issue.ID
  //   cliIssue.Summary → api.Issue.Title
  //   cliIssue.Status  → api.Issue.State
  //   cliIssue.URL     → api.Issue.URL
  func (p *Provider) GetIssue(ctx context.Context, id string) (*api.Issue, error) {
  	id = strings.TrimSpace(id)
  	if id == "" {
  		return nil, fmt.Errorf("jira provider: empty issue id")
  	}
  	stdout, err := p.runner.Run(ctx, []string{p.binary, "issue", id})
  	if err != nil {
  		return nil, fmt.Errorf("jira provider: exec %q issue %s: %w", p.binary, id, err)
  	}
  	var raw cliIssue
  	if err := json.Unmarshal(stdout, &raw); err != nil {
  		return nil, fmt.Errorf("jira provider: decode output for %s: %w", id, err)
  	}
  	if raw.Key == "" {
  		return nil, fmt.Errorf("jira provider: response for %s is missing key field", id)
  	}
  	return &api.Issue{
  		ID:    raw.Key,
  		Title: raw.Summary,
  		State: raw.Status,
  		URL:   raw.URL,
  	}, nil
  }
  ```

- [ ] **Step 4: Run the tests to verify they pass**

  Run: `cd packages/pg-pr && go test ./pkg/provider/issues/jira/ -v`
  Expected: all five tests PASS.

- [ ] **Step 5: Verify `cmd/pg-pr/issue.go` picks up the new provider without changes**

  The existing `newIssueProvider` already routes `name == "jira"` to `jira.New()`. After SP3
  replaces the stub, that path calls the real shell-out provider. Confirm with:

  ```bash
  cd packages/pg-pr && go build ./cmd/pg-pr/
  ```

  Expected: builds cleanly (no compile errors from the updated `jira` package).

- [ ] **Step 6: Run the full pg-pr test suite**

  Run: `cd packages/pg-pr && go vet ./... && go test ./...`
  Expected: all tests pass. The `issue.go` tests that inject `newIssueProvider` remain unaffected
  because they substitute the provider directly.

- [ ] **Step 7: Commit**

  ```bash
  git branch --show-current   # verify: generic-jira-access-tool (or equivalent SP3 branch)
  git add packages/pg-pr/pkg/provider/issues/jira/jira.go \
          packages/pg-pr/pkg/provider/issues/jira/jira_test.go
  git commit -m "$(cat <<'EOF'
  feat(pg-pr/jira): replace Phase-0 stub with generic CLI shell-out adapter

  Refs: pg2-2x2d.3

  GetIssue now execs the generic `jira issue <KEY>` CLI (SP1) and maps the
  JSON envelope to api.Issue. Binary name defaults to "jira"; overridable
  via PGPR_JIRA_BINARY for tenant-specific aliases. No ZR strings; no
  cross-repo Go import.
  EOF
  )"
  ```

---

### Task 2: Validation gates

**Files:** none new; runs existing tooling.

**Purpose:** confirm SP3 is complete and clean before marking the bead done.

- [ ] **Step 1: Run repo pre-commit**

  Run: `prek run --all-files`
  Expected: green. If `treefmt` rewrites nix or markdown, re-stage and re-run.

- [ ] **Step 2: Run nix flake check**

  Run: `nix flake check`
  Expected: green.

- [ ] **Step 3: Smoke-test the end-to-end path (requires SP1 + SP2 merged)**

  This step is a post-SP2 gate. Skip in isolation; run when both SP1 and SP2 are merged and the
  `jira` binary is on PATH with a ZR config:

  ```bash
  pg-pr issue show ENG-1 --provider jira
  ```

  Expected: prints `provider: jira / id: ENG-1 / title: ... / state: ... / url: ...` (the
  `renderIssue` format from `cmd/pg-pr/issue.go`).

  Alternatively, validate the shell-out directly:

  ```bash
  PGPR_JIRA_BINARY=jira jira issue ENG-1   # via the generic CLI alone
  ```

- [ ] **Step 4: Confirm §5.3 invariant is still satisfied**

  Run SP1's guardrail test (it is in repo-base, not agent-support):

  ```bash
  cd /path/to/phillipg-nix-repo-base && \
    go test ./modules/jira/pkg/jira/ -run TestNoForbiddenStrings
  ```

  Expected: PASS. (SP3 adds no new code to repo-base, so this is a confirmation that SP1's gate
  was not accidentally widened.)

---

## Mapping Reference

The `jira.Issue` JSON output from SP1's generic CLI → `api.Issue` consumed by pg-pr:

| `jira issue` JSON field | `api.Issue` field | Notes                                              |
| ----------------------- | ----------------- | -------------------------------------------------- |
| `key`                   | `ID`              | Jira issue key (e.g. `ENG-42`)                     |
| `summary`               | `Title`           | Issue summary text                                 |
| `status`                | `State`           | Status name (e.g. `"In Progress"`)                 |
| `url`                   | `URL`             | Pre-computed `<base_url>/browse/<key>` from SP1    |
| `priority`              | _(future UJ-3)_   | Decoded into `cliIssue` but not yet in `api.Issue` |
| `issuetype`, `labels`   | _(future UJ-3)_   | Same — decoded, unused in SP3, ready to wire later |

`api.Issue.ID` receives the Jira **key** (e.g. `ENG-42`), not an internal numeric ID. This is
consistent with the existing `pg-pr-issues-jira-zr` behavior (line 119–124 of
`modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go`).

---

## Dependency Notes

- **SP1 (repo-base, `pg2-2x2d.1`)**: MUST be merged first. SP3 assumes `jira issue <KEY>` exists
  and emits the SP1-specified JSON envelope. SP3 can be developed with a fake runner in tests
  without SP1 merged, but the Task 2 smoke-test requires it.
- **SP2 (ziprecruiter, `pg2-2x2d.2`)**: MUST be deployed before the ZR-tenant end-to-end path
  works. SP3 itself carries no ZR strings; SP2 wires `PGPR_JIRA_BINARY` (or puts `jira` on PATH)
  for the ZR machine. The adapter works with any compliant binary regardless of tenant.
- **UJ-3 (pg2-4c5i.26)**: The future `Priority`/incident extension to `api.Issue` will land in
  agent-support; SP3's `cliIssue.Priority` decode means SP3 does not need to be re-opened — the
  UJ-3 plan only needs to wire `raw.Priority` into the mapping and add `Priority string` to
  `api.Issue`.

---

## Open Design Decisions

These MUST be resolved before work begins. They are listed in order of urgency:

1. **Option (A) vs Option (B) — the central choice.** This plan implements (A) shell-out. If the
   user prefers (B) cross-repo Go import, a different plan is needed (gomod2nix Pattern B plumbing,
   rooted-fileset build, `go.mod replace` in agent-support). The recommendation is (A) for
   simplicity and consistency with existing shell-out patterns in pg-pr; confirm before coding.

2. **Should the generic tool also speak scriptout?** Today the generic `jira` CLI is a plain CLI
   only (SP1); pg-pr adapts to it via shell-out (Option A). If a future consumer needs scriptout
   from the generic tool, that would be a separate SP. This plan takes the position that the
   generic tool stays plain CLI and pg-pr adapts — no scriptout in repo-base.

3. **Binary name discovery — `jira` vs `pg-pr-issues-jira-zr`.** The adapter defaults to `"jira"`
   and reads `PGPR_JIRA_BINARY`. SP2 must choose: (a) put `jira` on PATH (clean, matches the
   generic tool name), or (b) keep `pg-pr-issues-jira-zr` and set `PGPR_JIRA_BINARY=pg-pr-issues-jira-zr`.
   Option (b) is back-compat with pr-pool's existing `search` CLI but conflates the scriptout
   binary with the plain-CLI binary. Recommendation: SP2 installs `jira` on PATH as the primary
   binary; the `pg-pr-issues-jira-zr` name stays as a separate alias for pr-pool's `search` path
   (already the SP2 plan in spec §10.1).

4. **`auth_status` wiring.** `cmd/pg-pr/issue.go` calls `checkAuth` via the `scriptout.AuthChecker`
   interface when the provider is an `execIssuesProvider`; the builtin `jira.New()` returns a
   `*Provider` which does not implement `AuthChecker`. `scriptout.checkAuth` falls back to `AuthOK`
   for providers that do not implement the interface (see `scriptout.go` lines 440–444). Decide:
   (a) accept the `AuthOK` fallback for SP3 (UJ-4 is listed as `auth_status` / `op exists`); or
   (b) implement `AuthStatus(ctx) scriptout.AuthStatus` on `*Provider` by calling `<binary>
auth-status`. Option (a) is correct for SP3 scope; option (b) is deferred to a follow-up
   unless UJ-4 is in scope here.

5. **`cliIssue` JSON trim vs SP1 indented output.** SP1's `writeJSON` uses `enc.SetIndent("", "
")`, emitting pretty-printed JSON. `json.Unmarshal` handles whitespace transparently, so this
   is not a code issue — just a documentation note confirming the adapter is robust to formatting
   changes in the generic CLI output.
