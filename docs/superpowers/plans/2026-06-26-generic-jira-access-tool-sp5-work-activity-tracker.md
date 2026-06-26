# SP5 — Migrate work-activity-tracker off the 410'd Jira Endpoint

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `work-activity-tracker`'s broken `aiohttp POST /rest/api/3/search` call with an async
`subprocess` call to `jira search --jql "…" --limit 100`, parse the `{items,truncated}` JSON envelope,
and map its **already-flattened** fields onto the same downstream graph emissions WAT produces today
(activities, entities, relationships). Delete WAT's own REST plumbing and its macOS-keychain credential
code (`credentials.py`). Creds now live entirely in the `jira` CLI's edge config (SP2).

**Architecture:** The new `JiraActivitySource` replaces `_load_token` + `_get_issues` + `aiohttp` with a
single `async def _run_jira_search(jql) -> list[Issue]` that shells out via the existing
`CommandExecutor.run_command`, parses the JSON, and returns a typed list. `_process_issue` is rewritten
to accept a flat `Issue`-shaped dict (keys match the generic envelope: `key`, `summary`, `status`,
`issuetype`, `priority`, `project`, `created`, `updated`, `url`, `reporter{email,account_id,display_name}`,
`assignee{…}`) instead of the Atlassian nested shape (keys were `fields.status.name`, etc.). The JQL
construction in `_build_jql` is preserved unchanged. Zero ZR strings remain in WAT.

**Tech stack:** Python (asyncio), `pytest`, `pytest-asyncio`; no new runtime deps. `aiohttp` is removed
from `pyproject.toml` (if WAT is the only consumer — verify first). `credentials.py` is deleted after
confirming no other module imports it.

**Spec:** `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` §10 SP5, §3 UJ-6.

**Dependency:** SP2 MUST be deployed (the `jira` binary on PATH, ZR-configured) before WAT SP5 runs
in production. WAT code stays generic — it calls `jira`; tenant/creds are SP2's concern.

## Global Constraints

- **Location:** `phillipgreenii-nix-support-apps/packages/work-activity-tracker/`.
- **Source file:** `src/work_activity_tracker/features/jira/source.py` — rewrite in-place; do NOT rename.
- **Tests:** `tests/unit/features/test_jira_source.py` — rewrite to mock `CommandExecutor.run_command`
  returning a `{items,truncated}` JSON fixture; delete the `@patch("aiohttp.ClientSession.post")` mocks.
- **Gate:** `./check-all.sh` (ruff, mypy, import-linter, pytest, coverage `fail_under=65`) MUST pass.
- **Import-linter Feature independence contract:** `work_activity_tracker.features.jira` MUST NOT import
  from any other feature. Verified by the existing contract — no change needed, only confirm it still
  passes after the rewrite.
- **No ZR strings** anywhere in WAT source (no `ziprecruiter`, `zr-jira`, `atlassian.net`). The old
  `keychain_service` config key referenced `work-activity-tracker-jira`; that string disappears.
- **Preserve downstream graph:** every `gather_session.collect_activity`, `discover_entity`,
  `discover_relationship` call site MUST emit the same entity types, activity types, and relationship
  types as today. Regression is unacceptable.
- **No `--expand`:** comments are vestigial in WAT (per spec §10 SP5). Do NOT request `--expand`.
- **Truncation awareness:** WAT historically requested `maxResults=100`; pass `--limit 100`. If the
  response has `truncated=true`, log a warning (do not silently drop data).
- **Configuration changes:** `server_url` and `user_email` are no longer needed at the WAT level
  once credentials move to the `jira` CLI. The `__init__` signature SHOULD be simplified (see
  Open Design Decisions). For this plan, keep `server_url` optional (default `""`) so existing
  config files do not break immediately; emit a deprecation log if present. `keychain_service`
  config key is removed.
- **Binary name:** WAT calls `"jira"` on PATH. See Open Design Decisions for the configurable
  binary path option — this plan does NOT implement configurability (keep it `"jira"`; revisit in
  follow-up).
- **Commits:** work is on branch `sp5-work-activity-tracker` in `phillipgreenii-nix-support-apps`.
  Do not push. Verify branch with `git branch --show-current` before any commit.

## Field Mapping Reference

The generic CLI envelope uses **flat** keys; the old Atlassian shape used nested `fields.*` dicts.
The mapping an implementer MUST apply in `_process_issue`:

| WAT today (Atlassian nested)            | Generic CLI envelope (flat)        | Notes                                      |
| --------------------------------------- | ---------------------------------- | ------------------------------------------ |
| `issue_data["key"]`                     | `item["key"]`                      | unchanged                                  |
| `fields["summary"]`                     | `item["summary"]`                  | unchanged                                  |
| `fields["status"]["name"]`              | `item["status"]`                   | already a string                           |
| `fields["issuetype"]["name"]`           | `item["issuetype"]`                | already a string                           |
| `fields["priority"]["name"]`            | `item["priority"]`                 | already a string (may be absent → `""`)    |
| `fields["project"]["key"]`              | `item["project"]`                  | already a string (project key only)        |
| `fields["project"]["name"]`             | _(absent)_                         | use `item["project"]` as both key and name |
| `fields["created"]`                     | `item["created"]`                  | unchanged (ISO 8601 string)                |
| `fields["updated"]`                     | `item["updated"]`                  | unchanged (ISO 8601 string)                |
| `f"{self.jira_url}/browse/{issue_key}"` | `item["url"]`                      | URL now provided by CLI                    |
| `fields["reporter"]["emailAddress"]`    | `item["reporter"]["email"]`        | key rename: `emailAddress` → `email`       |
| `fields["reporter"]["accountId"]`       | `item["reporter"]["account_id"]`   | key rename: `accountId` → `account_id`     |
| `fields["reporter"]["displayName"]`     | `item["reporter"]["display_name"]` | key rename: `displayName` → `display_name` |
| same for `assignee`                     | same pattern                       |                                            |

**Identity fallback (reporter/assignee id):** WAT today uses
`reporter.get("emailAddress", reporter.get("accountId"))` as the person entity ID. In the generic
envelope, prefer `email` then fall back to `account_id`. The fallback logic MUST be preserved.

## File Structure

```text
packages/work-activity-tracker/
  src/work_activity_tracker/features/jira/
    source.py          ← REWRITE (drop aiohttp/keychain; add CommandExecutor shell-out)
    __init__.py        ← no change
  src/work_activity_tracker/utils/
    credentials.py     ← DELETE (after verifying no other importer)
    subprocess.py      ← no change (CommandExecutor stays)
  tests/unit/features/
    test_jira_source.py  ← REWRITE (mock CommandExecutor, not aiohttp)
  pyproject.toml         ← MODIFY (remove aiohttp if sole consumer; remove credentials dep)
```

---

### Task 1: Audit dependencies and create the test fixture

**Files:**

- Read: `pyproject.toml` — confirm whether `aiohttp` is used anywhere besides `jira/source.py`
- Read: `src/work_activity_tracker/` — grep for `credentials` imports to confirm sole usage
- Create: the JSON fixture constant that the new tests will use

**Interfaces:**

- Produces: a `SAMPLE_SEARCH_RESULT` fixture dict (inline in the test file, not a separate file) that
  exactly mirrors the `{items,truncated}` JSON the `jira` CLI emits; used by Tasks 2–4.

- [ ] **Step 1: Confirm `aiohttp` usage scope**

  Run:

  ```bash
  grep -r "aiohttp" \
    /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/work-activity-tracker/src
  ```

  Expected: only `features/jira/source.py`. If other files import `aiohttp`, do NOT remove it from
  `pyproject.toml` in Task 5 — note in plan and remove only from `source.py`.

- [ ] **Step 2: Confirm `credentials.py` usage scope**

  Run:

  ```bash
  grep -r "credentials\|KeychainCredentials" \
    /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/work-activity-tracker/src
  ```

  Expected: only `features/jira/source.py`. If other files import `KeychainCredentials`, do NOT
  delete `credentials.py` — note and leave it in place.

- [ ] **Step 3: Define the shared fixture constant**

  The fixture captures the generic CLI envelope for one issue with reporter + assignee + project.
  This will be embedded in `test_jira_source.py` (Task 2). Exact shape:

  ```python
  SAMPLE_SEARCH_RESULT = {
      "items": [
          {
              "key": "TEST-123",
              "summary": "Implement new feature",
              "status": "In Progress",
              "issuetype": "Story",
              "labels": [],
              "url": "https://test.atlassian.net/browse/TEST-123",
              "priority": "High",
              "project": "TEST",
              "created": "2024-10-01T10:00:00.000+0000",
              "updated": "2024-10-02T14:30:00.000+0000",
              "reporter": {
                  "email": "reporter@example.com",
                  "account_id": "reporter-account-id",
                  "display_name": "Reporter Name",
              },
              "assignee": {
                  "email": "assignee@example.com",
                  "account_id": "assignee-account-id",
                  "display_name": "Assignee Name",
              },
          }
      ],
      "truncated": False,
  }
  ```

  Note: this fixture maps exactly to the fields produced by `jira search` per `pkg/jira/model.go`
  (`Issue` struct with `json:"…"` tags). No Atlassian-nested shapes appear here.

---

### Task 2: Rewrite `test_jira_source.py` (tests first — TDD)

**Files:**

- Rewrite: `tests/unit/features/test_jira_source.py`

**Interfaces:**

- Consumes: `JiraActivitySource` (new interface, defined in Task 3), `CommandExecutor`,
  `GatherSessionAggregate`.
- Produces: a complete test suite that mocks `CommandExecutor.run_command` returning
  `SAMPLE_SEARCH_RESULT` JSON and asserts the SAME activities/entities/relationships as today.

The tests MUST cover:

1. Initialization validation (missing `server_url` is now optional/deprecated; `user_email`
   is removed — config only needs `binary` optionally).
2. JQL construction: date window with and without `project_keys`.
3. `_run_jira_search` success: mock `run_command` → fixture JSON → returns list of `dict`.
4. `_run_jira_search` CLI non-zero exit: raises `RuntimeError`.
5. `_run_jira_search` malformed JSON: raises `RuntimeError`.
6. `_run_jira_search` `truncated=True`: logs warning, still returns items.
7. `_process_issue` happy path: asserts `collect_activity` (type `jira_issue_updated`), `discover_entity`
   (types `jira_issue`, `jira_project`, `person` ×2), `discover_relationship` (≥3).
8. `_process_issue` outside date range: no events emitted.
9. `_process_issue` missing assignee: only reporter person + 2 relationships.
10. `retrieve_for_gather` end-to-end: mock `run_command`, assert session populated.

- [ ] **Step 1: Write the failing tests**

  Replace `tests/unit/features/test_jira_source.py` with:

  ```python
  """Unit tests for the subprocess-backed JIRA activity source."""

  import json
  from datetime import UTC, datetime
  from typing import Any
  from unittest.mock import AsyncMock, MagicMock, patch

  import pytest

  from work_activity_tracker.aggregates.gather_session import GatherSessionAggregate
  from work_activity_tracker.features.jira.source import JiraActivitySource
  from work_activity_tracker.utils.subprocess import CommandResult

  # ---------------------------------------------------------------------------
  # Fixture: generic CLI envelope (mirrors pkg/jira SearchResult JSON)
  # ---------------------------------------------------------------------------

  SAMPLE_SEARCH_RESULT: dict[str, Any] = {
      "items": [
          {
              "key": "TEST-123",
              "summary": "Implement new feature",
              "status": "In Progress",
              "issuetype": "Story",
              "labels": [],
              "url": "https://test.atlassian.net/browse/TEST-123",
              "priority": "High",
              "project": "TEST",
              "created": "2024-10-01T10:00:00.000+0000",
              "updated": "2024-10-02T14:30:00.000+0000",
              "reporter": {
                  "email": "reporter@example.com",
                  "account_id": "reporter-account-id",
                  "display_name": "Reporter Name",
              },
              "assignee": {
                  "email": "assignee@example.com",
                  "account_id": "assignee-account-id",
                  "display_name": "Assignee Name",
              },
          }
      ],
      "truncated": False,
  }

  SAMPLE_SEARCH_RESULT_JSON = json.dumps(SAMPLE_SEARCH_RESULT)


  def _ok_result(stdout: str) -> CommandResult:
      """Build a successful CommandResult."""
      return CommandResult(stdout=stdout, stderr="", exit_code=0, command=["jira"])


  def _fail_result(stderr: str = "error") -> CommandResult:
      """Build a failed CommandResult."""
      return CommandResult(stdout="", stderr=stderr, exit_code=1, command=["jira"])


  # ---------------------------------------------------------------------------
  # Helpers
  # ---------------------------------------------------------------------------


  @pytest.fixture
  def jira_config() -> dict[str, Any]:
      """Minimal config — server_url and user_email are legacy/optional now."""
      return {
          "project_keys": ["TEST"],
      }


  @pytest.fixture
  def jira_source(jira_config: dict[str, Any]) -> JiraActivitySource:
      """Source with no network access."""
      return JiraActivitySource(config=jira_config)


  @pytest.fixture
  def session() -> GatherSessionAggregate:
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)
      return GatherSessionAggregate(
          session_id="test-session",
          date_range_start=start,
          date_range_end=end,
          started_at=datetime.now(UTC),
      )


  # ---------------------------------------------------------------------------
  # Initialization
  # ---------------------------------------------------------------------------


  def test_initialization_minimal() -> None:
      """Source can be constructed with empty config."""
      source = JiraActivitySource(config={})
      assert source.project_keys == []


  def test_initialization_with_project_keys() -> None:
      """project_keys filter is stored."""
      source = JiraActivitySource(config={"project_keys": ["PROJ", "TEAM"]})
      assert source.project_keys == ["PROJ", "TEAM"]


  def test_initialization_server_url_deprecated(caplog: Any) -> None:
      """server_url in config is accepted but emits a deprecation warning."""
      import logging

      with caplog.at_level(logging.WARNING, logger="work_activity_tracker.features.jira.source"):
          source = JiraActivitySource(
              config={"server_url": "https://test.atlassian.net", "user_email": "u@x"}
          )
      assert source is not None  # did not raise
      assert any("deprecated" in r.message.lower() or "server_url" in r.message for r in caplog.records)


  # ---------------------------------------------------------------------------
  # JQL construction
  # ---------------------------------------------------------------------------


  def test_build_jql_date_only(jira_source: JiraActivitySource) -> None:
      """JQL without project_keys has only the date window."""
      source = JiraActivitySource(config={})
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)
      jql = source._build_jql(start, end)
      assert "updated >= '2024-10-01'" in jql
      assert "updated <= '2024-10-07'" in jql
      assert "project" not in jql


  def test_build_jql_with_project_keys(jira_source: JiraActivitySource) -> None:
      """JQL with project_keys appends project filter."""
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)
      jql = jira_source._build_jql(start, end)
      assert "project in (TEST)" in jql


  def test_build_jql_multiple_projects() -> None:
      source = JiraActivitySource(config={"project_keys": ["A", "B"]})
      jql = source._build_jql(
          datetime(2024, 1, 1, tzinfo=UTC), datetime(2024, 1, 7, tzinfo=UTC)
      )
      assert "project in (A, B)" in jql


  # ---------------------------------------------------------------------------
  # _run_jira_search
  # ---------------------------------------------------------------------------


  @pytest.mark.asyncio
  async def test_run_jira_search_success(jira_source: JiraActivitySource) -> None:
      """Successful CLI call returns list of item dicts."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_ok_result(SAMPLE_SEARCH_RESULT_JSON))
      jira_source._executor = mock_executor

      items = await jira_source._run_jira_search("project = TEST")

      assert len(items) == 1
      assert items[0]["key"] == "TEST-123"
      assert items[0]["status"] == "In Progress"   # flat string, NOT nested dict
      assert items[0]["reporter"]["email"] == "reporter@example.com"


  @pytest.mark.asyncio
  async def test_run_jira_search_builds_correct_command(jira_source: JiraActivitySource) -> None:
      """Command sent to executor is `jira search --jql <jql> --limit 100`."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_ok_result(SAMPLE_SEARCH_RESULT_JSON))
      jira_source._executor = mock_executor

      jql = "project = TEST"
      await jira_source._run_jira_search(jql)

      call_args = mock_executor.run_command.call_args
      command: list[str] = call_args[0][0]
      assert command[0] == "jira"
      assert "search" in command
      assert "--jql" in command
      assert jql in command
      assert "--limit" in command
      assert "100" in command


  @pytest.mark.asyncio
  async def test_run_jira_search_cli_failure_raises(jira_source: JiraActivitySource) -> None:
      """Non-zero exit from `jira` raises RuntimeError."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_fail_result("auth error"))
      jira_source._executor = mock_executor

      with pytest.raises(RuntimeError, match="jira search"):
          await jira_source._run_jira_search("project = TEST")


  @pytest.mark.asyncio
  async def test_run_jira_search_malformed_json_raises(jira_source: JiraActivitySource) -> None:
      """Unparseable stdout raises RuntimeError."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_ok_result("not json"))
      jira_source._executor = mock_executor

      with pytest.raises(RuntimeError, match="parse"):
          await jira_source._run_jira_search("project = TEST")


  @pytest.mark.asyncio
  async def test_run_jira_search_truncated_logs_warning(
      jira_source: JiraActivitySource, caplog: Any
  ) -> None:
      """truncated=True is logged as a warning; items are still returned."""
      import logging

      truncated_result = dict(SAMPLE_SEARCH_RESULT)
      truncated_result["truncated"] = True
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(
          return_value=_ok_result(json.dumps(truncated_result))
      )
      jira_source._executor = mock_executor

      with caplog.at_level(logging.WARNING):
          items = await jira_source._run_jira_search("project = TEST")

      assert len(items) == 1
      assert any("truncated" in r.message.lower() for r in caplog.records)


  # ---------------------------------------------------------------------------
  # _process_issue  (flat envelope → same graph as before)
  # ---------------------------------------------------------------------------


  @pytest.mark.asyncio
  async def test_process_issue_emits_activity(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """_process_issue emits one jira_issue_updated activity."""
      item = SAMPLE_SEARCH_RESULT["items"][0]
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      assert len(session.activities_collected) == 1
      act_type, _ = session.activities_collected[0]
      assert act_type == "jira_issue_updated"


  @pytest.mark.asyncio
  async def test_process_issue_activity_attributes(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Activity attributes carry the flattened fields (not nested Atlassian shapes)."""
      item = SAMPLE_SEARCH_RESULT["items"][0]
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      # Retrieve the attributes dict from the session
      # GatherSessionAggregate.activities_collected stores (type, attributes) tuples
      _, attrs = session.activities_collected[0]
      assert attrs["issue_key"] == "TEST-123"
      assert attrs["issue_type"] == "Story"        # was fields["issuetype"]["name"]
      assert attrs["status"] == "In Progress"      # was fields["status"]["name"]
      assert attrs["priority"] == "High"           # was fields["priority"]["name"]
      assert "url" in attrs
      assert attrs["url"] == "https://test.atlassian.net/browse/TEST-123"


  @pytest.mark.asyncio
  async def test_process_issue_entity_types(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """discover_entity called for jira_issue, jira_project, and person (reporter+assignee)."""
      item = SAMPLE_SEARCH_RESULT["items"][0]
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      entity_types = {et for et, _ in session.entities_discovered}
      assert "jira_issue" in entity_types
      assert "jira_project" in entity_types
      assert "person" in entity_types


  @pytest.mark.asyncio
  async def test_process_issue_relationships(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Three relationships emitted: issue→project, reporter→issue, assignee→issue."""
      item = SAMPLE_SEARCH_RESULT["items"][0]
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      assert len(session.relationships_discovered) >= 3


  @pytest.mark.asyncio
  async def test_process_issue_reporter_identity_email_preferred(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Person entity ID uses email when present (email preferred over account_id)."""
      item = SAMPLE_SEARCH_RESULT["items"][0]
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      person_ids = {eid for et, eid in session.entities_discovered if et == "person"}
      assert "reporter@example.com" in person_ids
      assert "assignee@example.com" in person_ids


  @pytest.mark.asyncio
  async def test_process_issue_reporter_identity_account_id_fallback(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Person entity ID falls back to account_id when email is absent."""
      item = dict(SAMPLE_SEARCH_RESULT["items"][0])
      item["reporter"] = {"email": "", "account_id": "acct-999", "display_name": "No Email"}
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      person_ids = {eid for et, eid in session.entities_discovered if et == "person"}
      assert "acct-999" in person_ids


  @pytest.mark.asyncio
  async def test_process_issue_outside_date_range(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Issue updated before start_date is silently skipped."""
      item = dict(SAMPLE_SEARCH_RESULT["items"][0])
      item["updated"] = "2024-09-01T10:00:00.000+0000"   # before range
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      assert len(session.activities_collected) == 0


  @pytest.mark.asyncio
  async def test_process_issue_no_assignee(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Missing assignee emits only reporter person + 2 relationships (issue→project, reporter→issue)."""
      item = dict(SAMPLE_SEARCH_RESULT["items"][0])
      item = {**item, "assignee": None}
      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source._process_issue(session, item, start, end)

      person_ids = {eid for et, eid in session.entities_discovered if et == "person"}
      assert "reporter@example.com" in person_ids
      assert "assignee@example.com" not in person_ids
      assert len(session.relationships_discovered) == 2


  # ---------------------------------------------------------------------------
  # retrieve_for_gather  (end-to-end)
  # ---------------------------------------------------------------------------


  @pytest.mark.asyncio
  async def test_retrieve_for_gather_end_to_end(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """Full retrieve_for_gather flow using mocked CommandExecutor."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_ok_result(SAMPLE_SEARCH_RESULT_JSON))
      jira_source._executor = mock_executor

      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source.retrieve_for_gather(session, start, end)

      assert len(session.activities_collected) > 0
      assert len(session.entities_discovered) > 0
      assert len(session.relationships_discovered) > 0


  @pytest.mark.asyncio
  async def test_retrieve_for_gather_passes_correct_jql(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """retrieve_for_gather passes the JQL WAT builds to the CLI."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_ok_result(SAMPLE_SEARCH_RESULT_JSON))
      jira_source._executor = mock_executor

      start = datetime(2024, 10, 1, tzinfo=UTC)
      end = datetime(2024, 10, 7, tzinfo=UTC)

      await jira_source.retrieve_for_gather(session, start, end)

      call_args = mock_executor.run_command.call_args
      command: list[str] = call_args[0][0]
      full_command = " ".join(command)
      assert "updated >= '2024-10-01'" in full_command
      assert "project in (TEST)" in full_command


  @pytest.mark.asyncio
  async def test_retrieve_for_gather_cli_error_propagates(
      jira_source: JiraActivitySource,
      session: GatherSessionAggregate,
  ) -> None:
      """CLI failure raises and does not silently skip."""
      mock_executor = MagicMock()
      mock_executor.run_command = AsyncMock(return_value=_fail_result("401 unauthorized"))
      jira_source._executor = mock_executor

      with pytest.raises(RuntimeError):
          await jira_source.retrieve_for_gather(
              session,
              datetime(2024, 10, 1, tzinfo=UTC),
              datetime(2024, 10, 7, tzinfo=UTC),
          )
  ```

  > **Note on `session.activities_collected` / `entities_discovered` access:** the tests above use
  > `session.activities_collected` as a list of `(activity_type, attrs)` tuples — match the actual
  > `GatherSessionAggregate` API (read `aggregates/gather_session.py` before implementing and adjust
  > if the stored shape differs). The existing tests do the same; mirror their access pattern exactly.

- [ ] **Step 2: Run tests to verify they fail**

  ```bash
  cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/work-activity-tracker
  uv run pytest tests/unit/features/test_jira_source.py -x 2>&1 | head -40
  ```

  Expected: FAIL — `ImportError` or `AttributeError` because `source.py` still uses the old interface
  (`_load_token`, `aiohttp`, etc.) and the new test calls `_build_jql`, `_run_jira_search`, etc.

---

### Task 3: Rewrite `source.py`

**Files:**

- Rewrite: `src/work_activity_tracker/features/jira/source.py`

**Interfaces:**

- Removes: `aiohttp` import, `KeychainCredentials` import, `_load_token`, `jira_token` attribute,
  `jira_url` attribute, `jira_email` attribute, `keychain_service` attribute.
- Adds: `CommandExecutor` import, `_executor` attribute, `_build_jql(start, end) -> str` method,
  `_run_jira_search(jql) -> list[dict]` async method.
- Preserves: `_process_issue`, `_parse_jira_timestamp`, `retrieve_for_gather` signatures
  (except `_process_issue` now accepts the flat envelope dict).
- Produces: a `JiraActivitySource` that emits the same activities/entities/relationships as today
  but fetches via `jira search` subprocess.

- [ ] **Step 1: Write the new `source.py`**

  Replace `src/work_activity_tracker/features/jira/source.py` with:

  ```python
  """JIRA activity source via the generic `jira` CLI subprocess.

  Collects:
  - Issues updated in a date window
  - Per-issue: project entity, reporter and assignee person entities, relationships

  Credentials and tenant configuration are handled by the `jira` CLI's own
  edge configuration (SP2). WAT does not read credentials and contains no
  ZipRecruiter-specific values.

  Configuration:
    features:
      jira:
        enabled: true
        project_keys:          # Optional filter
          - PROJ
          - TEAM

  Deprecated (accepted but ignored; will be removed in a future release):
    server_url:   formerly the Jira base URL (now the CLI's concern)
    user_email:   formerly the Jira user email (now the CLI's concern)
    keychain_service:  formerly the macOS keychain service name (removed)

  Prerequisites:
  - The `jira` generic CLI MUST be on PATH and configured (SP2).
  """

  import json
  import logging
  from datetime import UTC, datetime
  from typing import Any
  from uuid import uuid4

  from work_activity_tracker.utils.hashing import hash_relationship
  from work_activity_tracker.utils.subprocess import CommandExecutor

  logger = logging.getLogger(__name__)

  _DEPRECATED_KEYS = ("server_url", "user_email", "keychain_service")


  class JiraActivitySource:
      """JIRA activity source backed by the generic `jira` CLI."""

      def __init__(
          self,
          config: dict[str, Any] | None = None,
          executor: CommandExecutor | None = None,
      ):
          """Initialize JIRA source.

          Args:
              config: Configuration dictionary. Only `project_keys` is consumed;
                  `server_url`, `user_email`, and `keychain_service` are deprecated
                  and logged as warnings when present.
              executor: CommandExecutor instance (optional, created if not provided).
          """
          config = config or {}
          self._executor = executor or CommandExecutor()
          self.project_keys: list[str] = config.get("project_keys", [])

          for key in _DEPRECATED_KEYS:
              if config.get(key):
                  logger.warning(
                      "JIRA source: config key %r is deprecated and ignored. "
                      "Tenant config and credentials are now managed by the `jira` CLI. "
                      "Remove it from your config.yaml.",
                      key,
                  )

      def _build_jql(self, start_date: datetime, end_date: datetime) -> str:
          """Build JQL query for the date window and optional project filter.

          Args:
              start_date: Start of date range (inclusive).
              end_date: End of date range (inclusive).

          Returns:
              JQL string ready for `jira search --jql`.
          """
          jql_parts = [
              f"updated >= '{start_date.strftime('%Y-%m-%d')}'"
              f" AND updated <= '{end_date.strftime('%Y-%m-%d')}'"
          ]
          if self.project_keys:
              projects = ", ".join(self.project_keys)
              jql_parts.append(f"project in ({projects})")
          return " AND ".join(jql_parts)

      async def _run_jira_search(self, jql: str) -> list[dict[str, Any]]:
          """Run `jira search --jql <jql> --limit 100` and return the items list.

          Args:
              jql: JQL query string.

          Returns:
              List of item dicts from the `{items, truncated}` envelope.

          Raises:
              RuntimeError: If the CLI exits non-zero or stdout is not valid JSON.
          """
          command = ["jira", "search", "--jql", jql, "--limit", "100"]
          result = await self._executor.run_command(command, timeout=60.0)

          if not result.success:
              raise RuntimeError(
                  f"jira search failed (exit {result.exit_code}): {result.stderr.strip()}"
              )

          try:
              data: dict[str, Any] = json.loads(result.stdout)
          except json.JSONDecodeError as exc:
              raise RuntimeError(
                  f"jira search: failed to parse CLI output as JSON: {exc}\n"
                  f"stdout was: {result.stdout[:200]}"
              ) from exc

          if data.get("truncated"):
              logger.warning(
                  "JIRA: search returned truncated results for JQL %r. "
                  "Some issues in the date window may be missing. "
                  "Consider narrowing the date range or adding project filters.",
                  jql,
              )

          return data.get("items", [])

      async def retrieve_for_gather(
          self,
          gather_session: Any,
          start_date: datetime,
          end_date: datetime,
      ) -> None:
          """Retrieve JIRA activities and emit events to the gather session.

          Args:
              gather_session: GatherSessionAggregate to emit events to.
              start_date: Start of date range.
              end_date: End of date range.
          """
          jql = self._build_jql(start_date, end_date)
          logger.info("JIRA: Collecting issues from %s to %s", start_date, end_date)
          logger.debug("JIRA: JQL = %s", jql)

          try:
              issues = await self._run_jira_search(jql)
              logger.info("JIRA: Found %d issues", len(issues))
              for issue in issues:
                  await self._process_issue(gather_session, issue, start_date, end_date)
          except Exception as exc:
              logger.error("JIRA: Error collecting issues: %s", exc)
              raise

      async def _process_issue(
          self,
          gather_session: Any,
          item: dict[str, Any],
          start_date: datetime,
          end_date: datetime,
      ) -> None:
          """Process a single issue item from the generic CLI envelope and emit events.

          The `item` dict uses the flat generic envelope (keys: key, summary, status,
          issuetype, priority, project, created, updated, url, reporter{email,
          account_id, display_name}, assignee{…}) — NOT the Atlassian nested shape.

          Args:
              gather_session: GatherSessionAggregate to emit events to.
              item: Flat issue dict from `jira search` output.
              start_date: Start of date range (for date-range guard).
              end_date: End of date range.
          """
          issue_key: str = item["key"]

          # Parse timestamps
          updated = self._parse_jira_timestamp(item.get("updated"))
          created = self._parse_jira_timestamp(item.get("created"))

          # Only process if updated falls within the requested window
          if not updated or not (start_date <= updated <= end_date):
              return

          # -----------------------------------------------------------------
          # Activity
          # -----------------------------------------------------------------
          activity_id = str(uuid4())
          gather_session.collect_activity(
              activity_id=activity_id,
              activity_type="jira_issue_updated",
              activity_timestamp=updated,
              source_id="jira",
              primary_entity_type="jira_issue",
              primary_entity_id=issue_key,
              primary_entity_name=item.get("summary", issue_key),
              additional_refs=[],
              attributes={
                  "issue_key": issue_key,
                  "issue_type": item.get("issuetype", "Unknown"),
                  "status": item.get("status", "Unknown"),
                  "priority": item.get("priority", "Unknown"),
                  "created": created.isoformat() if created else None,
                  "updated": updated.isoformat(),
                  "url": item.get("url", ""),
              },
          )

          # -----------------------------------------------------------------
          # Issue entity
          # -----------------------------------------------------------------
          gather_session.discover_entity(
              entity_type="jira_issue",
              entity_id=issue_key,
              entity_name=item.get("summary", issue_key),
              discovered_via_activity=activity_id,
              discovery_context="primary",
              discovery_source="jira",
              discovery_timestamp=datetime.now(UTC),
          )

          # -----------------------------------------------------------------
          # Project entity + relationship
          # -----------------------------------------------------------------
          project_key: str | None = item.get("project") or None
          if project_key:
              gather_session.discover_entity(
                  entity_type="jira_project",
                  entity_id=project_key,
                  entity_name=project_key,   # generic envelope has key only; use as name
                  discovered_via_activity=activity_id,
                  discovery_context="issue_project",
                  discovery_source="jira",
                  discovery_timestamp=datetime.now(UTC),
              )
              rel_hash = hash_relationship(
                  "jira_issue", issue_key, "jira_project", project_key, "belongs_to"
              )
              gather_session.discover_relationship(
                  relationship_hash=rel_hash,
                  left_entity_type="jira_issue",
                  left_entity_id=issue_key,
                  right_entity_type="jira_project",
                  right_entity_id=project_key,
                  relationship_type="belongs_to",
                  discovered_via_activity=activity_id,
                  discovery_context="issue_metadata",
                  discovery_timestamp=datetime.now(UTC),
              )

          # -----------------------------------------------------------------
          # Reporter entity + relationship
          # -----------------------------------------------------------------
          reporter = item.get("reporter") or {}
          reporter_id = reporter.get("email") or reporter.get("account_id")
          if reporter_id:
              gather_session.discover_entity(
                  entity_type="person",
                  entity_id=reporter_id,
                  entity_name=reporter.get("display_name", reporter_id),
                  discovered_via_activity=activity_id,
                  discovery_context="issue_reporter",
                  discovery_source="jira",
                  discovery_timestamp=datetime.now(UTC),
              )
              rel_hash = hash_relationship(
                  "person", reporter_id, "jira_issue", issue_key, "reported"
              )
              gather_session.discover_relationship(
                  relationship_hash=rel_hash,
                  left_entity_type="person",
                  left_entity_id=reporter_id,
                  right_entity_type="jira_issue",
                  right_entity_id=issue_key,
                  relationship_type="reported",
                  discovered_via_activity=activity_id,
                  discovery_context="issue_metadata",
                  discovery_timestamp=datetime.now(UTC),
              )

          # -----------------------------------------------------------------
          # Assignee entity + relationship
          # -----------------------------------------------------------------
          assignee = item.get("assignee") or {}
          assignee_id = assignee.get("email") or assignee.get("account_id")
          if assignee_id:
              gather_session.discover_entity(
                  entity_type="person",
                  entity_id=assignee_id,
                  entity_name=assignee.get("display_name", assignee_id),
                  discovered_via_activity=activity_id,
                  discovery_context="issue_assignee",
                  discovery_source="jira",
                  discovery_timestamp=datetime.now(UTC),
              )
              rel_hash = hash_relationship(
                  "person", assignee_id, "jira_issue", issue_key, "assigned_to"
              )
              gather_session.discover_relationship(
                  relationship_hash=rel_hash,
                  left_entity_type="person",
                  left_entity_id=assignee_id,
                  right_entity_type="jira_issue",
                  right_entity_id=issue_key,
                  relationship_type="assigned_to",
                  discovered_via_activity=activity_id,
                  discovery_context="issue_metadata",
                  discovery_timestamp=datetime.now(UTC),
              )

      def _parse_jira_timestamp(self, timestamp_str: str | None) -> datetime | None:
          """Parse Jira / generic CLI ISO 8601 timestamp.

          Args:
              timestamp_str: ISO 8601 string (e.g. "2024-10-02T14:30:00.000+0000").

          Returns:
              Timezone-aware datetime or None.
          """
          if not timestamp_str:
              return None
          try:
              return datetime.fromisoformat(timestamp_str.replace("Z", "+00:00"))
          except Exception as exc:
              logger.warning("Failed to parse Jira timestamp %r: %s", timestamp_str, exc)
              return None
  ```

- [ ] **Step 2: Run tests to verify they pass**

  ```bash
  cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/work-activity-tracker
  uv run pytest tests/unit/features/test_jira_source.py -v 2>&1 | tail -30
  ```

  Expected: all tests PASS.

- [ ] **Step 3: Run quick check**

  ```bash
  ./check-all.sh --quick
  ```

  Expected: ruff, mypy, import-linter, unit tests all pass.

  > **mypy note:** `CommandExecutor` is already typed in `subprocess.py`. The new `source.py`
  > imports it directly. If mypy complains about `gather_session: Any`, that is intentional (WAT
  > uses `Any` for the aggregate in the existing code — match the existing pattern).

- [ ] **Step 4: Commit**

  ```bash
  git branch --show-current   # must be sp5-work-activity-tracker
  git add \
    src/work_activity_tracker/features/jira/source.py \
    tests/unit/features/test_jira_source.py
  git commit -m "feat(jira): replace aiohttp/keychain with jira CLI subprocess (SP5)

  Refs: <ticket from branch name>"
  ```

---

### Task 4: Delete `credentials.py` and clean up `pyproject.toml`

**Files:**

- Delete: `src/work_activity_tracker/utils/credentials.py` (if verified sole-usage in Task 1)
- Modify: `pyproject.toml` (remove `aiohttp` from `dependencies` if sole-usage confirmed in Task 1)

**Interfaces:**

- Removes: `work_activity_tracker.utils.credentials` module (only if no other importer found).
- Removes: `aiohttp` runtime dependency (only if no other importer found).
- The import-linter `"Utils are low-level"` contract does NOT list `credentials.py` by name —
  removing the file keeps the contract satisfied.

- [ ] **Step 1: Delete `credentials.py` (conditional on Task 1 Step 2)**

  If Task 1 Step 2 confirmed no other importer:

  ```bash
  git rm src/work_activity_tracker/utils/credentials.py
  ```

  If another importer exists: skip this step and note it as a follow-up.

- [ ] **Step 2: Remove `aiohttp` from `pyproject.toml` (conditional on Task 1 Step 1)**

  If Task 1 Step 1 confirmed no other `aiohttp` user:

  Using `tomlq` / `tq` (per workspace rules, prefer structured tools over sed):

  ```bash
  # Read current deps, remove aiohttp, write back
  tq -i '.project.dependencies |= map(select(startswith("aiohttp") | not))' \
    pyproject.toml
  ```

  Alternatively, edit manually: remove the `"aiohttp>=3.9.0"` line from
  `[project.dependencies]` in `pyproject.toml`.

  Then regenerate the lock file:

  ```bash
  uv lock
  ```

- [ ] **Step 3: Run full check**

  ```bash
  ./check-all.sh
  ```

  Expected: all checks green (ruff, mypy, import-linter, pytest unit + integration, coverage
  `fail_under=65`).

  > **Coverage note:** removing `credentials.py` (which was covered by the deleted keychain tests)
  > and replacing it with a simpler `source.py` should maintain or improve coverage. If coverage
  > drops below 65%, add tests for the new `_build_jql` / `_parse_jira_timestamp` edge cases
  > (already covered by Tasks 2's tests in most scenarios).

- [ ] **Step 4: Commit**

  ```bash
  git branch --show-current   # must be sp5-work-activity-tracker
  git add pyproject.toml uv.lock
  # Only if deleted:
  git add -u src/work_activity_tracker/utils/credentials.py
  git commit -m "chore(jira): remove aiohttp dep and keychain credentials module (SP5)

  Refs: <ticket from branch name>"
  ```

---

### Task 5: Final gate + self-review

**Files:**

- No new files.

**Interfaces:**

- Gate: `./check-all.sh` green (full, not `--quick`).
- Confirms: the import-linter Feature independence contract still passes (jira feature imports
  only `aggregates`, `utils`, `hashing`; not other features).
- Confirms: no `aiohttp`, `KeychainCredentials`, `security`, `keychain_service`, `work-activity-tracker-jira`
  remain in `features/jira/source.py`.

- [ ] **Step 1: Guardrail grep**

  ```bash
  grep -n "aiohttp\|KeychainCredentials\|security\|keychain_service\|work-activity-tracker-jira\|ziprecruiter\|zr-jira" \
    src/work_activity_tracker/features/jira/source.py
  ```

  Expected: no output. If any match is found, remove it before proceeding.

- [ ] **Step 2: Run full check**

  ```bash
  ./check-all.sh
  ```

  Expected: all green. If coverage is the only failure, see note in Task 4 Step 3.

- [ ] **Step 3: Verify downstream graph parity**

  Re-run the specific graph-parity test:

  ```bash
  uv run pytest tests/unit/features/test_jira_source.py \
    -k "entity_types or relationships or activity" -v
  ```

  Expected: all PASS — confirms `jira_issue`, `jira_project`, `person` entity types,
  `jira_issue_updated` activity type, and ≥3 relationships per issue.

- [ ] **Step 4: Final commit (if any fixup needed)**

  If Step 1 or 2 required additional changes:

  ```bash
  git branch --show-current
  git add <changed files>
  git commit -m "fix(jira): SP5 guardrail cleanup

  Refs: <ticket from branch name>"
  ```

---

## Self-Review

**Spec coverage** (against `2026-06-26-generic-jira-access-tool-design.md` §10 SP5, §3 UJ-6):

- §10 SP5: replace `aiohttp` call to deprecated `/search` with `subprocess` call to `jira search` — Task 3. ✓
- §10 SP5: delete own REST + keychain code — Tasks 3, 4. ✓
- §10 SP5: preserve activity/entity-mapping layer — Task 3 (`_process_issue` rewritten with same
  emissions), Task 2 (tests assert same graph). ✓
- §10 SP5: no `--expand` (comments vestigial) — `_run_jira_search` does not pass `--expand`. ✓
- §3 UJ-6: `summary, status, issuetype, priority, project, created, updated, reporter{…}, assignee{…}`
  all mapped via Field Mapping Reference table. ✓
- §1.1 generic constraint: no ZR string in WAT — guardrail grep in Task 5 Step 1. ✓
- SP5 dependency on SP2 noted in Global Constraints; WAT code calls `"jira"` on PATH only. ✓
- `check-all.sh` gate (ruff/mypy/import-linter/pytest/coverage) — Task 4 Step 3 + Task 5 Step 2. ✓

**Deleted surface:**

- `_load_token` (async keychain call)
- `jira_token` attribute
- `jira_url` attribute (WAT no longer constructs the browse URL — it comes from `item["url"]`)
- `jira_email` attribute
- `keychain_service` attribute
- `aiohttp` import and HTTP POST block in `_get_issues`
- `KeychainCredentials` import in `source.py`
- `credentials.py` module entirely (if sole-usage confirmed)

**New surface:**

- `_build_jql(start, end) -> str` — the JQL builder, extracted from `_get_issues`
- `_run_jira_search(jql) -> list[dict]` — the subprocess shell-out
- Deprecation log for legacy config keys (`server_url`, `user_email`, `keychain_service`)

---

## Open Design Decisions

1. **Binary name and location:** this plan hard-codes `"jira"` as the command name (found on PATH via
   the nix home-manager module installed by SP2). An alternative is a config key `binary: "jira"` in
   WAT's config, allowing a different binary path without an SP2 dependency. Recommended: ship with
   hard-coded `"jira"` for now; add configurability only if a second tenant or non-PATH scenario
   arises. WAT is already PATH-dependent for `git`, `gh`, etc.

2. **Keychain migration — WAT's old `work-activity-tracker-jira` entry:** WAT no longer reads the
   keychain. SP2 MUST configure the `jira` CLI to use a unified `zr-jira` entry (or the same
   `work-activity-tracker-jira` entry under a new service name). Whether SP2 re-uses the existing
   entry or asks users to re-create it under `zr-jira` is an SP2 decision. From WAT's perspective the
   old keychain entry becomes orphaned; it SHOULD be documented in SP2 release notes.

3. **Identity: email vs. account_id parity:** the generic envelope includes both `email` and
   `account_id` in the `User` struct. WAT today prefers `emailAddress` and falls back to `accountId`.
   This plan preserves that semantic: prefer `email`, fall back to `account_id`. However, if SP2
   configures a ZR Jira tenant where users have both fields, the person entity IDs will be email
   addresses (as before), keeping the graph stable. If a tenant omits email (e.g., Jira Server),
   `account_id` becomes the ID — a graph discontinuity for existing WAT stores. This edge case is
   noted; no mitigation is implemented here (YAGNI: ZR always has email).

4. **Pagination / truncation:** WAT historically passed `maxResults=100` with no pagination. This plan
   preserves that limit (`--limit 100`) and logs a warning on `truncated=true`. If truncation becomes
   a real problem (high-velocity sprints), WAT would need to loop with cursor-based pagination — but
   the generic CLI does not yet expose `nextPageToken` as a `--after` flag. Pagination is deferred;
   the warning is the safety net. Track as a follow-up bead if the warning fires in production.

5. **`server_url` / `user_email` migration for existing config files:** existing WAT `config.yaml`
   files contain `server_url` and `user_email` under `features.jira`. After SP5 those keys are unused
   by WAT (the CLI owns them). This plan emits a deprecation warning but does NOT raise an error.
   The SP2 operator guide SHOULD document that users MUST remove these keys and configure the `jira`
   CLI instead. A future cleanup PR MAY make them errors (after a grace period); that is not SP5 scope.
