# SP4 — pr-pool Jira-Issues Verification Implementation Plan

> **For agentic workers:** This plan is VERIFICATION-ONLY. No pr-pool production code changes are
> expected. Read every cited file before acting. Do NOT modify any file outside of what is
> explicitly stated. If code changes are needed (alias PATH gap), they MUST be confirmed by the
> user first.

**Goal:** Confirm that pr-pool's `jira-issues` query type works end-to-end with the generic `jira`
tool shipped by SP1/SP2, using the `pg-pr-issues-jira-zr` alias SP2 preserves. No pr-pool code
change is expected; this sprint closes AC (d) of `pg2-5b4l`.

**Architecture context:** pr-pool calls `pg-pr-issues-jira-zr search --jql … --limit 100` via
`OSCommander` and unmarshals a `jiraSearchEnvelope{Items,Truncated}`. SP2 installs the generic
`jira` binary and adds a `pg-pr-issues-jira-zr` alias (or symlink) pointing to it. SP4 verifies
the alias preserves the exact flag names and that the widened envelope is a strict superset of what
pr-pool already parses.

**Spec:** `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §10 (SP4 precondition note).

**Bead:** `pg2-5b4l` (pr-pool jira-issues verification); also tracked as `pg2-2x2d.4`.

**Precondition:** SP1 and SP2 MUST be complete (generic `jira` binary built and installed; ZR edge
config generated; `pg-pr-issues-jira-zr` alias in place) before this plan is executed.

## Global Constraints

- **READ-ONLY on pr-pool**: no code changes to
  `phillipgreenii-nix-agent-support/packages/pr-pool/` unless an explicit PATH fix is required (see
  Task 3).
- **No new tests in pr-pool unless a gap is found**: existing tests in `internal/query/issues_test.go`
  already cover the mapping contract; add tests only if a real incompatibility surfaces.
- **No commit without passing gates**: `nix flake check` on agent-support MUST be green before
  closing the bead.

## Byte-Compatibility Matrix

The precondition requires both sides to match exactly. This table documents the verified contract:

### Flag names

| pr-pool calls (issues.go:159)        | Generic `jira search` CLI (cmd/jira/main.go:134-136) | Match?    |
| ------------------------------------ | ---------------------------------------------------- | --------- |
| `pg-pr-issues-jira-zr` (binary name) | `jira` (via alias from SP2)                          | via alias |
| subcommand `search`                  | subcommand `search`                                  | EXACT     |
| `--jql <string>`                     | `--jql <string>` (cobra StringVar)                   | EXACT     |
| `--limit 100`                        | `--limit <int>` (cobra IntVar)                       | EXACT     |

No other flags are passed by pr-pool. The generic tool's additional flags (`--expand`,
`--config`) are optional and unused by pr-pool; they do not affect the contract.

### Envelope keys

| pr-pool's `jiraSearchEnvelope` (issues.go:150-153) | Generic `SearchResult` (pkg/jira/model.go) | pr-pool's `jiraSearchItem` (issues.go:138-145) | Generic `Issue` (pkg/jira/model.go)     | Match? |
| -------------------------------------------------- | ------------------------------------------ | ---------------------------------------------- | --------------------------------------- | ------ |
| `items []jiraSearchItem` → `json:"items"`          | `Items []Issue` → `json:"items"`           | —                                              | —                                       | EXACT  |
| `truncated bool` → `json:"truncated"`              | `Truncated bool` → `json:"truncated"`      | —                                              | —                                       | EXACT  |
| —                                                  | —                                          | `key string` → `json:"key"`                    | `Key string` → `json:"key"`             | EXACT  |
| —                                                  | —                                          | `summary string` → `json:"summary"`            | `Summary string` → `json:"summary"`     | EXACT  |
| —                                                  | —                                          | `status string` → `json:"status"`              | `Status string` → `json:"status"`       | EXACT  |
| —                                                  | —                                          | `issuetype string` → `json:"issuetype"`        | `IssueType string` → `json:"issuetype"` | EXACT  |
| —                                                  | —                                          | `labels []string` → `json:"labels"`            | `Labels []string` → `json:"labels"`     | EXACT  |
| —                                                  | —                                          | `url string` → `json:"url"`                    | `URL string` → `json:"url"`             | EXACT  |

The generic `Issue` type carries additional optional fields (`priority`, `project`, `created`,
`updated`, `reporter`, `assignee`, `changelog`, `comments`) with `omitempty` tags. Go's
`encoding/json` silently ignores unknown keys on unmarshal, so pr-pool's `jiraSearchItem` struct
receives only the fields it declares; the extra fields are dropped. **The envelope is a strict
additive superset — backward compatibility is guaranteed by the Go JSON spec, not by runtime
coincidence.**

The only pr-pool field that requires the key to be present is `key` (checked explicitly at
issues.go:174-176); `summary`/`status`/`issuetype`/`labels`/`url` are mapped to `item.Metadata`
but their absence does not error.

---

### Task 1: Static byte-compatibility audit

**Files read:**

- `phillipgreenii-nix-agent-support/packages/pr-pool/internal/query/issues.go` (read)
- `phillipg-nix-repo-base/modules/jira/cmd/jira/main.go` (read — shipped by SP1)
- `phillipg-nix-repo-base/modules/jira/pkg/jira/model.go` (read — shipped by SP1)

**Purpose:** Mechanically confirm the flag names and JSON struct tags match. This is a read-only
audit; the Byte-Compatibility Matrix above is the artifact. Document any discrepancy found.

- [ ] **Step 1: Diff flag names**

  Read `issues.go` lines 155-163 (the `argv` build). Read `cmd/jira/main.go` lines 101-138
  (the `search` cobra command flag declarations). Confirm:
  - Binary name: `argv[0]` = `"pg-pr-issues-jira-zr"` (resolved via SP2 alias to `jira`).
  - Subcommand: `argv[1]` = `"search"`.
  - `--jql` flag: present on both sides; string type; passed as a single element
    (`argv[2]="--jql"`, `argv[3]=<jql string>`).
  - `--limit` flag: present on both sides; integer type; passed as `argv[4]="--limit"`,
    `argv[5]="100"` (strconv.Itoa(jiraListLimit) where jiraListLimit=100).

  Expected result: all four match EXACTLY. If any flag name or type differs, document the
  discrepancy and halt — a pr-pool code change or a SP2 alias script would be required.

- [ ] **Step 2: Diff JSON struct tags**

  Compare the JSON struct tags for the six per-item fields pr-pool reads
  (`key`, `summary`, `status`, `issuetype`, `labels`, `url`) against `pkg/jira/model.go`'s
  `Issue` struct. Confirm the envelope-level `items` / `truncated` tags also match.

  Expected result: all eight tags match EXACTLY (per the matrix above).

- [ ] **Step 3: Confirm additive-only widening**

  Verify that every field in the generic `Issue` type beyond pr-pool's six is tagged
  `json:",omitempty"`. Specifically check: `priority`, `project`, `created`, `updated`,
  `reporter`, `assignee`, `changelog`, `comments`.

  Expected result: all additional fields carry `omitempty`. A field without `omitempty` would
  emit a zero-value JSON key even when empty; while still harmless for pr-pool (unknown keys are
  ignored), it contradicts the spec invariant and should be noted.

---

### Task 2: End-to-end mapping test via fake binary

**Files read (no modifications):**

- `phillipgreenii-nix-agent-support/packages/pr-pool/internal/query/issues_test.go`
- `phillipgreenii-nix-agent-support/packages/pr-pool/internal/query/issues.go`

**Purpose:** Confirm that pr-pool correctly maps items from the widened envelope. The existing
`TestJiraIssues_mapsEnvelopeAndBuildsArgs` test already injects a fake `Commander` emitting a
two-item envelope. This task verifies it covers the new envelope shape (widened items with extra
fields present) and adds a single complementary test case if needed.

- [ ] **Step 1: Read the existing test**

  Read `issues_test.go` lines 96-124 (`TestJiraIssues_mapsEnvelopeAndBuildsArgs`). The fake
  output is:

  ```json
  {
    "items": [
      {
        "key": "PROJ-1",
        "summary": "Do X",
        "status": "To Do",
        "issuetype": "Bug",
        "labels": ["a", "b"],
        "url": "https://x/browse/PROJ-1"
      },
      {
        "key": "PROJ-2",
        "summary": "Do Y",
        "status": "In Progress",
        "issuetype": "Task",
        "labels": [],
        "url": "https://x/browse/PROJ-2"
      }
    ],
    "truncated": false
  }
  ```

  Confirm the test asserts: `ID = "PROJ-1"`, `Type = "jira-issue"`, `Title = "Do X"`,
  and that metadata carries `key`, `project`, `issuetype`, `status`, `url`.

- [ ] **Step 2: Evaluate widened-envelope coverage**

  The current fake payload omits the optional fields (`priority`, `project` at JSON level,
  `created`, `updated`, `reporter`, `assignee`, `changelog`, `comments`). The widened SP1 `jira
search` output WILL include these fields for some issues.

  Determine whether an additional test case is needed. The question is: does pr-pool correctly
  ignore the extra fields and still map items?

  Decision rule: `encoding/json`'s contract guarantees unknown fields are ignored on
  `Unmarshal`; no runtime test is strictly required. HOWEVER, adding one test case with a
  widened fake payload provides a regression guard that the struct tag names remain consistent
  across future refactors of either side.

  **Recommended:** add `TestJiraIssues_widenedEnvelopeDropsExtraFields` to `issues_test.go`
  only if the SP4 executor judges it worth the two lines of future maintenance. This is an
  optional deflake step, NOT a required code change. See Open Design Decision #3.

- [ ] **Step 3: Run existing pr-pool jira tests**

  Run: `cd phillipgreenii-nix-agent-support/packages/pr-pool && go test ./internal/query/ -v -run TestJiraIssues`

  Expected: all existing `TestJiraIssues_*` tests PASS. If any fail, that is a regression from
  a prior SP and MUST be investigated before proceeding.

- [ ] **Step 4: (conditional) Add the widened-envelope test case**

  If the Step 2 decision is "add the test", append to `issues_test.go`:

  ```go
  // TestJiraIssues_widenedEnvelopeDropsExtraFields confirms that extra keys in the
  // SP1-widened envelope (priority, project, created, updated, reporter, assignee, etc.)
  // are silently dropped and do not affect item mapping.
  func TestJiraIssues_widenedEnvelopeDropsExtraFields(t *testing.T) {
  	cmd := &recordingCmd{out: []byte(`{"items":[
  	  {"key":"PROJ-3","summary":"Do Z","status":"Done","issuetype":"Story","labels":["c"],
  	   "url":"https://x/browse/PROJ-3","priority":"High","project":"PROJ",
  	   "created":"2026-01-01T00:00:00.000+0000","updated":"2026-01-02T00:00:00.000+0000",
  	   "reporter":{"email":"r@x","account_id":"a1","display_name":"R"},
  	   "assignee":{"email":"a@x","account_id":"a2","display_name":"A"}}
  	],"truncated":false}`)}
  	items, err := JiraIssues{Project: "PROJ"}.Run(context.Background(), Env{Cmd: cmd})
  	if err != nil {
  		t.Fatal(err)
  	}
  	if len(items) != 1 || items[0].ID != "PROJ-3" || items[0].Title != "Do Z" {
  		t.Errorf("widened envelope: unexpected items: %+v", items)
  	}
  	if items[0].Metadata["key"] != "PROJ-3" || items[0].Metadata["status"] != "Done" {
  		t.Errorf("widened envelope: metadata wrong: %+v", items[0].Metadata)
  	}
  }
  ```

  Then run: `cd phillipgreenii-nix-agent-support/packages/pr-pool && go test ./internal/query/ -run TestJiraIssues`

  Expected: all PASS including the new case.

---

### Task 3: Verify `pg-pr-issues-jira-zr` is on pr-pool's runtime PATH

**Files read:**

- `phillipgreenii-nix-agent-support/packages/pr-pool/default.nix`

**Purpose:** pr-pool's `OSCommander` resolves `pg-pr-issues-jira-zr` (the first element of its
`argv` slice) via `exec.LookPath` at runtime. The binary MUST be on PATH when pr-pool runs.
Currently `default.nix` wraps pr-pool's PATH with only `ccpool`, `bd`, and `pg-pr` — the
`pg-pr-issues-jira-zr` alias is NOT in `makeBinPath`.

- [ ] **Step 1: Confirm the current PATH wrap**

  Read `packages/pr-pool/default.nix` lines 36-43 (the `postInstall` wrapProgram block).
  Confirm the `makeBinPath` list is `[ ccpool bd pg-pr ]` with no entry for the jira binary.

- [ ] **Step 2: Determine where `pg-pr-issues-jira-zr` lives after SP2**

  After SP2, `pg-pr-issues-jira-zr` is a name installed by the ZR edge (in
  `phillipg-nix-ziprecruiter`). Determine which of the following SP2 chose:

  a. A symlink/alias in the same package output as the generic `jira` binary (repo-base
  `pkgs.jira`), meaning adding `pkgs.jira` to pr-pool's `makeBinPath` is sufficient.
  b. A separate wrapper script installed by the ZR edge's home-manager module, meaning
  the alias is on the user's profile PATH but NOT inside any package's `bin/` — and
  therefore pr-pool's wrapProgram PATH gap does not need to change (the user's PATH at
  runtime covers it).
  c. Something else.

  Read the SP2 plan or the ziprecruiter module to resolve this. The answer determines
  whether a pr-pool `default.nix` change is needed.

- [ ] **Step 3: Act on the PATH determination**

  **Case (b) — user PATH covers it:** no pr-pool code change needed. Document in this bead's
  closing note that `pg-pr-issues-jira-zr` is resolved from user PATH at runtime and that this
  is acceptable because pr-pool is always run from a user shell session with the ZR profile
  active.

  **Case (a) — package bin needed:** propose (but do NOT apply without user confirmation) adding
  the jira package to pr-pool's `makeBinPath`. The diff would be:

  ```nix
  # in packages/pr-pool/default.nix, add the jira package as a parameter:
  #   { lib, mkGoApp, makeWrapper, ccpool, bd, pg-pr, jira }:
  # and extend makeBinPath:
  #   lib.makeBinPath [ ccpool bd pg-pr jira ]
  ```

  This requires passing `jira` through the callPackage chain in the agent-support `flake.nix`.
  Confirm with the user before applying.

  **Case (c):** surface the finding and halt for human decision.

---

### Task 4: Live end-to-end smoke test (post-SP2 deploy)

**Purpose:** With SP2 deployed (alias on PATH, ZR config in place), exercise pr-pool's
`jira-issues` query through the real binary. This is a manual verification step, not an automated
test.

- [ ] **Step 1: Confirm the alias resolves**

  Run:

  ```bash
  which pg-pr-issues-jira-zr
  pg-pr-issues-jira-zr search --jql "project = ZR AND resolution = Unresolved ORDER BY created ASC" --limit 5
  ```

  Expected: exits 0, prints a JSON object matching `{"items":[…],"truncated":…}` with each item
  carrying at minimum a `"key"` field.

- [ ] **Step 2: Exercise via pr-pool run-query**

  Configure a minimal pr-pool role with a `jira-issues` query (project or explicit JQL) and
  run:

  ```bash
  pr-pool run-query --role <role-name>
  ```

  Expected: pr-pool returns items (or an empty list if the JQL matches nothing) and does NOT
  emit `executable file not found in $PATH` or `not yet implemented`.

- [ ] **Step 3: Confirm truncation warning path**

  If the query returns exactly 100 items, confirm the `slog.Warn("jira-issues query truncated…")`
  log line appears. This is low-priority; skip if the test project has fewer than 100 open issues.

---

### Task 5: Close bead and record findings

**Purpose:** Update `pg2-5b4l` with the SP4 outcome and close it.

- [ ] **Step 1: Record findings in bead**

  Run `bd update pg2-5b4l` to add a comment with:
  - Whether the byte-compatibility matrix held (Task 1 result).
  - Whether any new test was added (Task 2 result).
  - How the PATH gap was resolved (Task 3 result).
  - Live smoke test outcome (Task 4 result).

- [ ] **Step 2: Close the bead**

  If all four AC items in `pg2-5b4l` now hold:

  ```bash
  bd close pg2-5b4l
  ```

  If any AC item still fails, update the bead with the blocker and leave it open.

- [ ] **Step 3: Record on pg2-2x2d.4**

  Run `bd update pg2-2x2d.4` to note SP4 complete and reference the `pg2-5b4l` closure.

---

## Validation Before "Done"

- `cd phillipgreenii-nix-agent-support/packages/pr-pool && go test ./internal/query/` MUST be green.
- `nix flake check` on agent-support MUST pass (if any `default.nix` was modified in Task 3).
- Live smoke test (Task 4) MUST confirm `jira-issues` returns items end-to-end.

---

## Open Design Decisions

### ODD-1: Keep `pg-pr-issues-jira-zr` alias vs migrate pr-pool to call `jira` directly

**Options:**

- **A — Keep alias (recommended for SP4):** SP2 preserves `pg-pr-issues-jira-zr` as an alias
  or symlink to `jira`. pr-pool's `argv[0]` stays `"pg-pr-issues-jira-zr"` in `issues.go`. Zero
  pr-pool code change. The alias is a compatibility shim; it can be dropped in a later cleanup
  sprint once all references are migrated.
- **B — Rename in pr-pool now:** change `argv[0]` from `"pg-pr-issues-jira-zr"` to `"jira"` in
  `issues.go`. Simpler runtime PATH (just `jira`); removes the alias dependency. Requires a
  pr-pool code change and a `go test` + commit in agent-support. Makes SP2's alias unnecessary
  for this consumer.

**Recommendation:** choose Option A for SP4 (verification only, no code change). Schedule Option
B as a follow-on cleanup bead once SP3 also migrates (so both pg-pr and pr-pool move to `jira` in
one sweep).

### ODD-2: Add `pg-pr-issues-jira-zr` / `jira` to pr-pool's nix `makeBinPath`

**Context:** `packages/pr-pool/default.nix` wraps PATH with `[ ccpool bd pg-pr ]`. The
`pg-pr-issues-jira-zr` alias is NOT in this list. At runtime, pr-pool relies on the alias being
present in the user's ambient PATH (from the ZR home-manager profile).

**Options:**

- **A — Add the package to makeBinPath:** hermetic; pr-pool works even if the user's profile
  PATH is not set up. Requires knowing which nix package provides the alias after SP2 and
  threading it through the callPackage chain in `flake.nix`.
- **B — Leave as ambient PATH dependency:** simpler; consistent with how pr-pool already
  relies on `bd` being on PATH in some invocation contexts. Acceptable if pr-pool is only ever
  run from a fully-loaded user shell with the ZR profile active.

**Decision needed from user before Task 3 applies any change.**

### ODD-3: Whether to add a widened-envelope regression test in pr-pool

**Context:** The existing `TestJiraIssues_mapsEnvelopeAndBuildsArgs` test uses a minimal fake
envelope (only the six fields pr-pool reads). Adding `TestJiraIssues_widenedEnvelopeDropsExtraFields`
(Task 2 Step 4) provides a concrete regression guard that Go's `encoding/json` behaviour is
relied upon correctly and that the struct tags on both sides remain aligned.

**Options:**

- **A — Add the test:** +~25 lines in `issues_test.go`; maintenance burden is low (the fake
  payload is a string literal). Recommended if any ambiguity about the JSON-unknown-fields
  contract was observed during audit.
- **B — Skip the test:** the Go spec guarantees the behaviour; the matrix audit (Task 1) is
  sufficient documentation. No pr-pool code change.

**Recommendation:** decide during Task 2 Step 2 based on whether the audit found any ambiguity.

### ODD-4: Whether to update the `pg-pr-issues-jira-zr` reference in `issues.go` comments

**Context:** `issues.go` line 104 says `// JiraIssues lists unresolved issues by running
\`pg-pr-issues-jira-zr search --jql <jql>\``and line 147 says`// jiraSearchEnvelope is the
stdout contract of \`pg-pr-issues-jira-zr search\``. These comments will become stale if ODD-1
Option B (rename to `jira`) is chosen.

**Recommendation:** defer comment updates to the same commit that renames the binary in `argv`.
Do not update comments while keeping the alias binary name in `argv[0]`.

### ODD-5: Deflaking the existing pr-pool jira test against the live binary

**Context:** `pg2-5b4l` noted that the jira-issues query was previously calling the wrong
binary (`jira` = ankitpokhrel/jira-cli, not installed). After SP2, the correct binary
(`pg-pr-issues-jira-zr` alias → generic `jira`) IS available. The existing unit tests use a
fake Commander and are already deterministic. The live Task 4 smoke test is the only step
touching the real binary.

If the smoke test reveals flakiness (e.g. token expiry, network timeouts causing intermittent
failures in a future CI integration), a `--timeout` flag or a dedicated `auth-status` preflight
step in the jira-issues query runner SHOULD be considered. This is not a SP4 deliverable but
should be tracked as a follow-on bead if observed.
