Now I have everything I need. Let me produce the complete implementation plan.

---

# `pn osx tcc-check` Parity Port — Implementation Plan

**Date:** 2026-06-26
**Branch target:** feature branch off main with a simple name (this repo does NOT use the ZR `username.TICKET.desc` format), e.g. `pn-osx-tcc-check-parity`
**Tracking bead:** TBD (this document becomes the bead body)
**For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

---

## Goal

Restore full behavioral parity of `pn osx tcc-check` with the original bash (`pn-osx-tcc-check.sh`). The Go stub in `internal/osx/tcc_darwin.go` runs sqlite3 twice but **discards both outputs** and prints nothing. This plan fills that gap: parse and group the query output in Go (replicating the awk logic), emit the formatted duplicate report to injected writers, and wire the CLI.

---

## Global Constraints

- **Module path:** `github.com/phillipgreenii/nix-repo-base/modules/pn`. Package `modules/pn/internal/osx/`.
- **No new Go dependencies.** Only stdlib + `cobra` + `go-toml/v2`. Adding a dep forces `go mod tidy && gomod2nix generate` (ADR 0008) — out of scope. This means **no pure-Go sqlite3 library**. `sqlite3` stays a subprocess via `exec.Runner`.
- **Prefer native Go over subprocess** wherever a Go equivalent exists. Subprocesses here: the FDA probe (`sqlite3 … "SELECT 1 FROM access LIMIT 1"`) and the duplicates query (`sqlite3 … "SELECT …"`). The awk grouping/sorting/formatting is ported to pure Go.
- **`sqlite3` must be in `runtimeDeps`.** The original `default.nix` listed `pkgs.sqlite` under both `runtimeDeps` and `testDeps`. Check `modules/pn/default.nix` and verify `sqlite3` is already present (the command was ported, not removed); if absent, add it back.
- **Darwin-only.** `tcc_darwin.go` carries `//go:build darwin`. All new files in `internal/osx/` MUST carry the same tag.
- **All commands accept `out io.Writer` and `errOut io.Writer`** injected from `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`. `Check` currently takes no writer — its signature MUST change (breaking the existing tests, which must be updated).
- **Never run real `sqlite3` or touch the real TCC.db in tests.** Use `exec.NewFakeRunner()` + `AddResponse` to script all sqlite3 calls.
- **`TCC_DB_PATH` env seam is already present** in the stub (`os.Getenv("TCC_DB_PATH")`). Keep it.
- **Per-subcommand `-v/--version`** is intentionally dropped (cobra owns versioning via `pn --version`). Do not re-add.
- **Verification gate:** `go test ./...` green AND `nix flake check` + `darwin-rebuild build --flake .` (build-only, NEVER switch) green before claiming complete.

---

## Exact Bash Behavior to Reproduce

Source: `git show d463549^:modules/pn/pn-osx-tcc-check/pn-osx-tcc-check.sh`

### 1. DB path resolution (already in stub, keep)

```bash
TCC_DB="${TCC_DB_PATH:-$HOME/Library/Application Support/com.apple.TCC/TCC.db}"
```

Priority: `CheckOptions.DBPath` → `$TCC_DB_PATH` → `$HOME/Library/Application Support/com.apple.TCC/TCC.db`.

### 2. FDA probe

```bash
sqlite3 "$TCC_DB" "SELECT 1 FROM access LIMIT 1" &>/dev/null
```

If this exits non-zero → print the warning to **`errOut`** and return `nil` (exit 0):

```
⚠️  TCC check skipped — terminal lacks Full Disk Access
   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]
```

(Bash redirects both stdout and stderr of the probe to `/dev/null`. Go: pass `exec.RunOptions{}` for the probe — the output is irrelevant; only the error matters. The warning then goes to `errOut`, not `out`.)

### 3. Duplicates query

```sql
SELECT service, client, last_modified
FROM access
WHERE client LIKE '/nix/store/%'
AND auth_value = 2
ORDER BY service, client;
```

Output format from sqlite3: `service|client|last_modified` (pipe-separated), one row per line.

Filter: `auth_value = 2` (granted/enabled). Disabled entries (`auth_value != 2`) are excluded at the SQL level.

### 4. Grouping logic (was awk, now Go)

Group by: `(service, basename(client))` — the last path component of the `client` field. This groups different store-hash versions of the same binary (e.g. `bash-5.2p37` and `bash-5.3p3` both have basename `bash`).

Within each group:

- Track all client paths and their `last_modified` timestamps.
- The entry with the highest `last_modified` integer is **current**.
- All others are **stale**. `stale_count = len(group) - 1`.

A group is a duplicate only if `len(group) > 1`.

Sort groups for output: primary sort by `service` ascending, secondary by `bin_name` ascending (matching the bash insertion-sort on `key_list`).

### 5. Output format — EXACT BYTES (the parity bar)

**When duplicates exist:**

```
⚠️  TCC Duplicate Report
━━━━━━━━━━━━━━━━━━━━━━━━

<for each duplicate group, sorted by service then bin_name>
<display_service>:
  <bin_name> — <total> entries (<stale_count> stale)
    ✓ <current_client_path> (current)
    ✗ <stale_client_path_1> (stale)
    ✗ <stale_client_path_2> (stale)
    ...

To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.
```

Notes on exact bytes:

- Header line: `⚠️  TCC Duplicate Report` (U+26A0 U+FE0F, two spaces)
- Separator: `━━━━━━━━━━━━━━━━━━━━━━━━` (24 × U+2501 BOX DRAWINGS HEAVY HORIZONTAL)
- Blank line after separator
- `display_service`: looked up in a fixed map; unknown services use the raw `service` string. Known mappings:
  - `kTCCServiceListenEvent` → `kTCCServiceListenEvent (Input Monitoring)`
  - `kTCCServiceCamera` → `kTCCServiceCamera (Camera)`
  - `kTCCServiceMicrophone` → `kTCCServiceMicrophone (Microphone)`
  - `kTCCServiceAccessibility` → `kTCCServiceAccessibility (Accessibility)`
  - `kTCCServiceSystemPolicyAllFiles` → `kTCCServiceSystemPolicyAllFiles (Full Disk Access)`
- Entry line: `  <bin_name> — <total> entries (<stale_count> stale)` (2-space indent; `—` is U+2014 EM DASH, spaces on both sides)
- Current: `    ✓ <path> (current)` (4-space indent; U+2713 CHECK MARK)
- Stale: `    ✗ <path> (stale)` (4-space indent; U+2717 BALLOT X)
- Current entries printed first (in original collection order, which tracks insertion from the `ORDER BY service, client` query result), then stale entries in the same order
- Blank line after each group
- Trailing cleanup line: `To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.`
- The entire output ends with a newline (the final `print ""` and the cleanup line both get newlines from awk's `print`)

**When no duplicates:**

```
✅ No TCC duplicates found
```

(U+2705 WHITE HEAVY CHECK MARK, one space)

All output (both cases) goes to **`out`**.

---

## Gap Analysis

File: `modules/pn/internal/osx/tcc_darwin.go`

| Line  | Defect             | Description                                                                                                                                                                                                                      |
| ----- | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 62    | Discarded output   | `_, err := t.runner.Run(ctx, "sqlite3", …, exec.RunOptions{})` for the probe — `RunOptions{}` is fine here (output intentionally ignored), but the warning is never written to any writer because there is no `errOut` parameter |
| 65    | Missing warning    | On FDA probe failure: `return nil` with no output — the bash warning is never emitted                                                                                                                                            |
| 70    | Discarded output   | `_, err := t.runner.Run(ctx, "sqlite3", …, exec.RunOptions{})` for the query — the sqlite3 output (`res.Stdout`) is captured in `_` and never parsed                                                                             |
| 70–73 | Missing formatting | No awk-equivalent grouping logic; no output of the duplicate report or the "no duplicates" message                                                                                                                               |
| 47    | Missing writers    | `Check(ctx context.Context, opts CheckOptions) error` — no `out` or `errOut` parameter; CLI passes nothing from cobra                                                                                                            |

`internal/cli/osx_darwin.go` line 33: `osx.New(exec.NewRealRunner()).Check(context.Background(), osx.CheckOptions{})` — passes no writers; must become `Check(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), osx.CheckOptions{})` after the signature change.

---

## Target Design

### Signature change

```go
func (t *TCC) Check(ctx context.Context, out, errOut io.Writer, opts CheckOptions) error
```

- `out` receives the duplicate report or "no duplicates" message.
- `errOut` receives the FDA-not-granted warning.

### New types

```go
// tccEntry is one row from the sqlite3 duplicates query.
type tccEntry struct {
    service      string
    client       string
    lastModified int64
}

// tccGroup is a set of entries sharing the same (service, binName) key.
type tccGroup struct {
    service       string
    binName       string
    entries       []tccEntry   // in query-result (insertion) order
    newestClient  string       // the client path with highest lastModified
    newestMod     int64
}
```

### New functions (all unexported, pure Go, unit-testable without Runner)

- `parseTCCRows(stdout []byte) []tccEntry` — splits sqlite3 pipe-delimited output into `tccEntry` slices; skips malformed lines; parses `last_modified` as int64 (treat non-numeric as 0).
- `groupTCCEntries(entries []tccEntry) []tccGroup` — groups by `(service, binName)` where `binName = path.Base(client)`; tracks newest per group; returns only groups with `len(entries) > 1`; sorts output slice by `(service, binName)` ascending.
- `serviceDisplayName(service string) string` — looks up the fixed map; returns the raw service string if not found.
- `formatTCCReport(groups []tccGroup) string` — produces the exact-byte formatted report string (header + separator + groups + cleanup line). Called only when `len(groups) > 0`.

### Orchestration (`Check`)

1. Resolve `dbPath` (same logic as stub).
2. Probe: `t.runner.Run(ctx, "sqlite3", []string{dbPath, "SELECT 1 FROM access LIMIT 1"}, exec.RunOptions{})`. On error: `fmt.Fprintf(errOut, "⚠️  TCC check skipped — terminal lacks Full Disk Access\n   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]\n")`, return `nil`.
3. Query: `t.runner.Run(ctx, "sqlite3", []string{dbPath, query}, exec.RunOptions{})`. On error: return wrapped error.
4. `entries := parseTCCRows(res.Stdout)`.
5. `groups := groupTCCEntries(entries)`.
6. If `len(groups) == 0`: `fmt.Fprintln(out, "✅ No TCC duplicates found")`. Else: `fmt.Fprint(out, formatTCCReport(groups))`.
7. Return `nil`.

### CLI change

`internal/cli/osx_darwin.go` `osxTCCCheckCmd()`:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    return osx.New(exec.NewRealRunner()).Check(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), osx.CheckOptions{})
},
```

---

## File Structure

All files in `modules/pn/internal/osx/`; all carry `//go:build darwin`.

- **Modify:** `tcc_darwin.go` — add `io` import; change `Check` signature; implement grouping/formatting by calling the new helpers; add the FDA warning write.
- **Create:** `tcc_format_darwin.go` — `parseTCCRows`, `groupTCCEntries`, `serviceDisplayName`, `formatTCCReport`. Pure functions, no Runner dependency → independently unit-testable.
- **Modify:** `tcc_test.go` — update existing tests for new signature; add grouping/formatting/golden tests.
- **Create:** `tcc_format_test_darwin.go` — unit tests for the pure helpers (parse, group, format).
- **Modify:** `internal/cli/osx_darwin.go` — pass `cmd.OutOrStdout(), cmd.ErrOrStderr()` to `Check`.

No Nix file changes needed IF `sqlite3` is already in `runtimeDeps` of `modules/pn/default.nix`. Verify this before closing. The old `pn-osx-tcc-check/default.nix` listed `pkgs.sqlite` under `runtimeDeps` and `testDeps`; if absent from the Go package's nix file, add it.

---

## TDD Task Breakdown

### Task 0: Signature + writer wiring (no logic yet)

**Files:** `tcc_darwin.go`, `tcc_test.go`, `internal/cli/osx_darwin.go`

**Goal:** Compile with the new signature; existing tests pass with updated call sites.

- [ ] **Step 1:** Update `Check` signature to `(ctx context.Context, out, errOut io.Writer, opts CheckOptions) error`. Add `"io"` import.
- [ ] **Step 2:** In `Check`, change the FDA-probe block to: on error, `fmt.Fprintf(errOut, "⚠️  TCC check skipped — terminal lacks Full Disk Access\n   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]\n"); return nil`.
- [ ] **Step 3:** Keep the query call for now (still discards `res.Stdout`). Add `_ = res` so the variable isn't unused. The function still returns `nil` at the end.
- [ ] **Step 4:** Update `tcc_test.go` — all three existing tests pass `&bytes.Buffer{}, &bytes.Buffer{}` as `out`/`errOut`. Add import `"bytes"`.
- [ ] **Step 5:** Add `TestCheck_NoFDAWritesWarning`:

```go
func TestCheck_NoFDAWritesWarning(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{ExitCode: 1}, errors.New("FDA not granted"))

    var out, errOut bytes.Buffer
    if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
        t.Fatalf("Check should return nil; got %v", err)
    }
    if !strings.Contains(errOut.String(), "TCC check skipped") {
        t.Errorf("warning not on errOut; got: %q", errOut.String())
    }
    if out.Len() != 0 {
        t.Errorf("out should be empty on FDA skip; got: %q", out.String())
    }
}
```

- [ ] **Step 6:** Update `internal/cli/osx_darwin.go` to pass `cmd.OutOrStdout(), cmd.ErrOrStderr()`.
- [ ] **Step 7:** `go test ./internal/osx/ -v` → PASS; `go build ./...` → ok.
- [ ] **Step 8:** Commit — `feat(pn/osx/tcc): add out/errOut writers to Check signature`

---

### Task 1: Pure helpers — parse, group, format

**Files:** `tcc_format_darwin.go`, `tcc_format_test_darwin.go`

**Goal:** Implement and test all pure logic (no Runner). These functions are the heart of the port.

#### Step 1: Write failing tests in `tcc_format_test_darwin.go`

```go
//go:build darwin

package osx

import (
    "strings"
    "testing"
)

func TestParseTCCRows_Basic(t *testing.T) {
    input := []byte(
        "kTCCServiceListenEvent|/nix/store/old111-sleepwatcher/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/new222-sleepwatcher/bin/sleepwatcher|2000\n",
    )
    rows := parseTCCRows(input)
    if len(rows) != 2 {
        t.Fatalf("want 2 rows, got %d", len(rows))
    }
    if rows[0].service != "kTCCServiceListenEvent" || rows[0].lastModified != 1000 {
        t.Fatalf("row[0] wrong: %+v", rows[0])
    }
    if rows[1].lastModified != 2000 {
        t.Fatalf("row[1] lastModified wrong: %+v", rows[1])
    }
}

func TestParseTCCRows_SkipsMalformed(t *testing.T) {
    input := []byte("only_one_field\nok|/nix/store/x/bin/x|999\n")
    rows := parseTCCRows(input)
    if len(rows) != 1 {
        t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
    }
}

func TestParseTCCRows_Empty(t *testing.T) {
    if got := parseTCCRows([]byte{}); len(got) != 0 {
        t.Fatalf("want empty, got %v", got)
    }
}

func TestGroupTCCEntries_NoDuplicates(t *testing.T) {
    entries := []tccEntry{
        {service: "kTCCServiceListenEvent", client: "/nix/store/aaa-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
        {service: "kTCCServiceListenEvent", client: "/nix/store/bbb-other/bin/tool", lastModified: 2000},
    }
    if got := groupTCCEntries(entries); len(got) != 0 {
        t.Fatalf("want no groups (unique bins), got %v", got)
    }
}

func TestGroupTCCEntries_DetectsDuplicates(t *testing.T) {
    entries := []tccEntry{
        {service: "kTCCServiceListenEvent", client: "/nix/store/old111-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
        {service: "kTCCServiceListenEvent", client: "/nix/store/old222-sleepwatcher/bin/sleepwatcher", lastModified: 2000},
        {service: "kTCCServiceListenEvent", client: "/nix/store/new333-sleepwatcher/bin/sleepwatcher", lastModified: 3000},
    }
    groups := groupTCCEntries(entries)
    if len(groups) != 1 {
        t.Fatalf("want 1 group, got %d", len(groups))
    }
    g := groups[0]
    if len(g.entries) != 3 {
        t.Fatalf("want 3 entries, got %d", len(g.entries))
    }
    if g.newestClient != "/nix/store/new333-sleepwatcher/bin/sleepwatcher" {
        t.Fatalf("wrong newestClient: %q", g.newestClient)
    }
    if g.binName != "sleepwatcher" {
        t.Fatalf("wrong binName: %q", g.binName)
    }
}

func TestGroupTCCEntries_GroupsByBasenameAcrossVersions(t *testing.T) {
    // bash: "bin_name = path_parts[n]" — last component only.
    // bash-5.2p37 and bash-5.3p3 both have basename "bash" and same service → one group of 4.
    entries := []tccEntry{
        {service: "kTCCServiceMicrophone", client: "/nix/store/aaa-bash-5.2p37/bin/bash", lastModified: 1000},
        {service: "kTCCServiceMicrophone", client: "/nix/store/bbb-bash-5.2p37/bin/bash", lastModified: 2000},
        {service: "kTCCServiceMicrophone", client: "/nix/store/ccc-bash-5.3p3/bin/bash", lastModified: 3000},
        {service: "kTCCServiceMicrophone", client: "/nix/store/ddd-bash-5.3p3/bin/bash", lastModified: 4000},
    }
    groups := groupTCCEntries(entries)
    if len(groups) != 1 {
        t.Fatalf("want 1 group, got %d", len(groups))
    }
    if len(groups[0].entries) != 4 {
        t.Fatalf("want 4 entries, got %d", len(groups[0].entries))
    }
    if groups[0].newestClient != "/nix/store/ddd-bash-5.3p3/bin/bash" {
        t.Fatalf("wrong newestClient: %q", groups[0].newestClient)
    }
}

func TestGroupTCCEntries_SortedByServiceThenBinName(t *testing.T) {
    entries := []tccEntry{
        {service: "kTCCServiceListenEvent", client: "/nix/store/a-z/bin/z", lastModified: 1},
        {service: "kTCCServiceListenEvent", client: "/nix/store/b-z/bin/z", lastModified: 2},
        {service: "kTCCServiceCamera",      client: "/nix/store/a-cam/bin/camera", lastModified: 1},
        {service: "kTCCServiceCamera",      client: "/nix/store/b-cam/bin/camera", lastModified: 2},
    }
    groups := groupTCCEntries(entries)
    if len(groups) != 2 {
        t.Fatalf("want 2 groups, got %d", len(groups))
    }
    // kTCCServiceCamera < kTCCServiceListenEvent lexicographically
    if groups[0].service != "kTCCServiceCamera" {
        t.Fatalf("first group should be Camera, got %q", groups[0].service)
    }
    if groups[1].service != "kTCCServiceListenEvent" {
        t.Fatalf("second group should be ListenEvent, got %q", groups[1].service)
    }
}

func TestServiceDisplayName_KnownAndUnknown(t *testing.T) {
    cases := []struct{ in, want string }{
        {"kTCCServiceListenEvent", "kTCCServiceListenEvent (Input Monitoring)"},
        {"kTCCServiceCamera", "kTCCServiceCamera (Camera)"},
        {"kTCCServiceMicrophone", "kTCCServiceMicrophone (Microphone)"},
        {"kTCCServiceAccessibility", "kTCCServiceAccessibility (Accessibility)"},
        {"kTCCServiceSystemPolicyAllFiles", "kTCCServiceSystemPolicyAllFiles (Full Disk Access)"},
        {"kTCCServiceUnknownService", "kTCCServiceUnknownService"},
    }
    for _, c := range cases {
        if got := serviceDisplayName(c.in); got != c.want {
            t.Errorf("serviceDisplayName(%q) = %q, want %q", c.in, got, c.want)
        }
    }
}

func TestFormatTCCReport_GoldenSingleGroup(t *testing.T) {
    groups := []tccGroup{
        {
            service:      "kTCCServiceListenEvent",
            binName:      "sleepwatcher",
            entries: []tccEntry{
                {service: "kTCCServiceListenEvent", client: "/nix/store/old-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
                {service: "kTCCServiceListenEvent", client: "/nix/store/new-sleepwatcher/bin/sleepwatcher", lastModified: 2000},
            },
            newestClient: "/nix/store/new-sleepwatcher/bin/sleepwatcher",
            newestMod:    2000,
        },
    }
    got := formatTCCReport(groups)

    // Golden: validate exact structure
    wantLines := []string{
        "⚠️  TCC Duplicate Report",
        "━━━━━━━━━━━━━━━━━━━━━━━━",
        "",
        "kTCCServiceListenEvent (Input Monitoring):",
        "  sleepwatcher \u2014 2 entries (1 stale)",
        "    \u2713 /nix/store/new-sleepwatcher/bin/sleepwatcher (current)",
        "    \u2717 /nix/store/old-sleepwatcher/bin/sleepwatcher (stale)",
        "",
        "To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.",
    }
    for _, line := range wantLines {
        if !strings.Contains(got, line) {
            t.Errorf("missing %q in output:\n%s", line, got)
        }
    }
}

func TestFormatTCCReport_GoldenMultipleGroups(t *testing.T) {
    groups := []tccGroup{
        {
            service: "kTCCServiceCamera", binName: "camera",
            entries: []tccEntry{
                {service: "kTCCServiceCamera", client: "/nix/store/a-cam/bin/camera", lastModified: 1000},
                {service: "kTCCServiceCamera", client: "/nix/store/b-cam/bin/camera", lastModified: 2000},
            },
            newestClient: "/nix/store/b-cam/bin/camera", newestMod: 2000,
        },
        {
            service: "kTCCServiceListenEvent", binName: "sleepwatcher",
            entries: []tccEntry{
                {service: "kTCCServiceListenEvent", client: "/nix/store/x-sw/bin/sleepwatcher", lastModified: 500},
                {service: "kTCCServiceListenEvent", client: "/nix/store/y-sw/bin/sleepwatcher", lastModified: 600},
            },
            newestClient: "/nix/store/y-sw/bin/sleepwatcher", newestMod: 600,
        },
    }
    got := formatTCCReport(groups)
    if !strings.Contains(got, "kTCCServiceCamera (Camera):") {
        t.Errorf("missing Camera group header")
    }
    if !strings.Contains(got, "kTCCServiceListenEvent (Input Monitoring):") {
        t.Errorf("missing ListenEvent group header")
    }
    // Both groups appear, cleanup line appears once at the end.
    if strings.Count(got, "To clean up:") != 1 {
        t.Errorf("cleanup line should appear exactly once")
    }
}

// Golden byte-exact test — locks the full output for the single-group case.
// If this fails, the output format has drifted from the bash.
func TestFormatTCCReport_ExactBytes(t *testing.T) {
    groups := []tccGroup{
        {
            service: "kTCCServiceListenEvent", binName: "sleepwatcher",
            entries: []tccEntry{
                {service: "kTCCServiceListenEvent", client: "/nix/store/old-sw/bin/sleepwatcher", lastModified: 1000},
                {service: "kTCCServiceListenEvent", client: "/nix/store/new-sw/bin/sleepwatcher", lastModified: 2000},
            },
            newestClient: "/nix/store/new-sw/bin/sleepwatcher", newestMod: 2000,
        },
    }
    want := "⚠️  TCC Duplicate Report\n" +
        "━━━━━━━━━━━━━━━━━━━━━━━━\n" +
        "\n" +
        "kTCCServiceListenEvent (Input Monitoring):\n" +
        "  sleepwatcher \u2014 2 entries (1 stale)\n" +
        "    \u2713 /nix/store/new-sw/bin/sleepwatcher (current)\n" +
        "    \u2717 /nix/store/old-sw/bin/sleepwatcher (stale)\n" +
        "\n" +
        "To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n"
    if got := formatTCCReport(groups); got != want {
        t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, got)
    }
}
```

- [ ] **Step 2:** Run `go test ./internal/osx/ -v` → FAIL (undefined symbols).
- [ ] **Step 3:** Implement `tcc_format_darwin.go`:

```go
//go:build darwin

package osx

import (
    "fmt"
    "path/filepath"
    "sort"
    "strconv"
    "strings"
)

// serviceLabels maps TCC service identifiers to human-readable display labels.
// Unknown services use the raw service string.
var serviceLabels = map[string]string{
    "kTCCServiceListenEvent":        "kTCCServiceListenEvent (Input Monitoring)",
    "kTCCServiceCamera":             "kTCCServiceCamera (Camera)",
    "kTCCServiceMicrophone":         "kTCCServiceMicrophone (Microphone)",
    "kTCCServiceAccessibility":      "kTCCServiceAccessibility (Accessibility)",
    "kTCCServiceSystemPolicyAllFiles": "kTCCServiceSystemPolicyAllFiles (Full Disk Access)",
}

// serviceDisplayName returns the human-readable label for a TCC service,
// or the raw service name if unknown.
func serviceDisplayName(service string) string {
    if label, ok := serviceLabels[service]; ok {
        return label
    }
    return service
}

// tccEntry is one row from the sqlite3 duplicates query.
type tccEntry struct {
    service      string
    client       string
    lastModified int64
}

// tccGroup is a set of entries sharing the same (service, binName) key.
type tccGroup struct {
    service      string
    binName      string
    entries      []tccEntry // insertion order (preserves ORDER BY service, client from query)
    newestClient string
    newestMod    int64
}

// parseTCCRows parses pipe-delimited sqlite3 output into tccEntry values.
// Malformed lines (fewer than 3 pipe-separated fields) are silently skipped.
// last_modified is parsed as int64; non-numeric values become 0.
func parseTCCRows(stdout []byte) []tccEntry {
    var out []tccEntry
    for _, line := range strings.Split(string(stdout), "\n") {
        line = strings.TrimRight(line, "\r")
        if line == "" {
            continue
        }
        parts := strings.Split(line, "|")
        if len(parts) < 3 {
            continue
        }
        ts, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
        out = append(out, tccEntry{
            service:      parts[0],
            client:       parts[1],
            lastModified: ts,
        })
    }
    return out
}

// groupTCCEntries groups entries by (service, basename(client)), retains only
// groups with more than one entry (i.e. duplicates), and sorts the result by
// (service, binName) ascending — matching the bash insertion-sort on key_list.
func groupTCCEntries(entries []tccEntry) []tccGroup {
    type key struct{ service, binName string }
    order := []key{} // insertion order for stable iteration
    byKey := map[key]*tccGroup{}

    for _, e := range entries {
        binName := filepath.Base(e.client)
        k := key{e.service, binName}
        g, ok := byKey[k]
        if !ok {
            g = &tccGroup{service: e.service, binName: binName}
            byKey[k] = g
            order = append(order, k)
        }
        g.entries = append(g.entries, e)
        if e.lastModified > g.newestMod {
            g.newestMod = e.lastModified
            g.newestClient = e.client
        }
    }

    var groups []tccGroup
    for _, k := range order {
        g := byKey[k]
        if len(g.entries) > 1 {
            groups = append(groups, *g)
        }
    }

    // Sort by service ascending, then binName ascending (bash: services[k1] > services[k2])
    sort.Slice(groups, func(i, j int) bool {
        if groups[i].service != groups[j].service {
            return groups[i].service < groups[j].service
        }
        return groups[i].binName < groups[j].binName
    })

    return groups
}

// formatTCCReport formats the full duplicate report for len(groups) > 0.
// The returned string ends with a newline.
func formatTCCReport(groups []tccGroup) string {
    var sb strings.Builder

    sb.WriteString("⚠️  TCC Duplicate Report\n")
    sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━\n")
    sb.WriteString("\n")

    for _, g := range groups {
        stale := len(g.entries) - 1
        display := serviceDisplayName(g.service)
        fmt.Fprintf(&sb, "%s:\n", display)
        fmt.Fprintf(&sb, "  %s \u2014 %d entries (%d stale)\n", g.binName, len(g.entries), stale)

        // Current first, then stales — in original insertion order within each pass
        for _, e := range g.entries {
            if e.client == g.newestClient {
                fmt.Fprintf(&sb, "    \u2713 %s (current)\n", e.client)
            }
        }
        for _, e := range g.entries {
            if e.client != g.newestClient {
                fmt.Fprintf(&sb, "    \u2717 %s (stale)\n", e.client)
            }
        }
        sb.WriteString("\n")
    }

    sb.WriteString("To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n")
    return sb.String()
}
```

- [ ] **Step 4:** Run `go test ./internal/osx/ -run 'TestParseTCC|TestGroupTCC|TestServiceDisplay|TestFormatTCC' -v` → PASS.
- [ ] **Step 5:** Commit — `feat(pn/osx/tcc): pure parse/group/format helpers`

---

### Task 2: Wire helpers into Check + full integration tests

**Files:** `tcc_darwin.go`, `tcc_test.go`

**Goal:** `Check` uses the parse/group/format helpers; `tcc_test.go` gains integration and golden-output tests.

- [ ] **Step 1:** Update `Check` to call `parseTCCRows` / `groupTCCEntries` / `formatTCCReport`:

```go
func (t *TCC) Check(ctx context.Context, out, errOut io.Writer, opts CheckOptions) error {
    dbPath := opts.DBPath
    if dbPath == "" {
        dbPath = os.Getenv("TCC_DB_PATH")
    }
    if dbPath == "" {
        home, err := os.UserHomeDir()
        if err != nil {
            return fmt.Errorf("locate home dir: %w", err)
        }
        dbPath = filepath.Join(home, defaultTCCDBSuffix)
    }

    // FDA probe — failure means terminal lacks Full Disk Access.
    if _, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, "SELECT 1 FROM access LIMIT 1"}, exec.RunOptions{}); err != nil {
        fmt.Fprintf(errOut, "⚠️  TCC check skipped — terminal lacks Full Disk Access\n   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]\n")
        return nil
    }

    const query = "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"
    res, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, query}, exec.RunOptions{})
    if err != nil {
        return fmt.Errorf("tcc duplicates query: %w", err)
    }

    entries := parseTCCRows(res.Stdout)
    groups := groupTCCEntries(entries)
    if len(groups) == 0 {
        fmt.Fprintln(out, "✅ No TCC duplicates found")
    } else {
        fmt.Fprint(out, formatTCCReport(groups))
    }
    return nil
}
```

- [ ] **Step 2:** Add integration tests to `tcc_test.go`:

```go
func TestCheck_NoDuplicatesPrintsClean(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, const query}, exec.Result{Stdout: []byte(
        "kTCCServiceListenEvent|/nix/store/aaa-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceCamera|/nix/store/bbb-cam/bin/camera|2000\n",
    )}, nil)

    var out, errOut bytes.Buffer
    if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
        t.Fatalf("Check: %v", err)
    }
    if !strings.Contains(out.String(), "No TCC duplicates found") {
        t.Errorf("want no-duplicates message, got: %q", out.String())
    }
    if errOut.Len() != 0 {
        t.Errorf("errOut should be empty, got: %q", errOut.String())
    }
}

func TestCheck_DuplicatesPrintsReport(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, const query}, exec.Result{Stdout: []byte(
        "kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
    )}, nil)

    var out, errOut bytes.Buffer
    if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
        t.Fatalf("Check: %v", err)
    }
    got := out.String()
    for _, want := range []string{
        "TCC Duplicate Report",
        "kTCCServiceListenEvent (Input Monitoring):",
        "sleepwatcher",
        "2 entries (1 stale)",
        "(current)",
        "(stale)",
        "remove stale entries manually",
    } {
        if !strings.Contains(got, want) {
            t.Errorf("missing %q in output:\n%s", want, got)
        }
    }
}

// Golden byte-exact integration test — locks full end-to-end output.
func TestCheck_GoldenDuplicates(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, const query}, exec.Result{Stdout: []byte(
        "kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
    )}, nil)

    want := "⚠️  TCC Duplicate Report\n" +
        "━━━━━━━━━━━━━━━━━━━━━━━━\n" +
        "\n" +
        "kTCCServiceListenEvent (Input Monitoring):\n" +
        "  sleepwatcher \u2014 2 entries (1 stale)\n" +
        "    \u2713 /nix/store/new-sw/bin/sleepwatcher (current)\n" +
        "    \u2717 /nix/store/old-sw/bin/sleepwatcher (stale)\n" +
        "\n" +
        "To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n"

    var out, errOut bytes.Buffer
    if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
        t.Fatalf("Check: %v", err)
    }
    if got := out.String(); got != want {
        t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, got)
    }
}

func TestCheck_EmptyQueryOutput(t *testing.T) {
    // Query returns no rows (no nix store entries at all) → no-duplicates
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, const query}, exec.Result{Stdout: []byte{}}, nil)

    var out bytes.Buffer
    if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
        t.Fatalf("Check: %v", err)
    }
    if !strings.Contains(out.String(), "No TCC duplicates found") {
        t.Errorf("empty output should produce no-duplicates message; got: %q", out.String())
    }
}
```

> Implementer note: the `const query` in test references must use the exact query string defined in `tcc_darwin.go`. Extract it to a package-level `const tccDuplicatesQuery = "…"` that both the production code and tests can reference.

- [ ] **Step 3:** Run `go test ./internal/osx/ -v` → all PASS.
- [ ] **Step 4:** Run `go build ./...` → ok.
- [ ] **Step 5:** Commit — `feat(pn/osx/tcc): wire grouping/formatting into Check; golden output tests`

---

### Task 3: Port remaining bats test cases

**Files:** `tcc_test.go`

**Goal:** Cover all scenarios from `test-pn-osx-tcc-check.bats` that aren't already covered.

- [ ] **Step 1:** Add the following tests (mapping from bats):

```go
// Bats: "marks newest as current: higher last_modified gets checkmark"
func TestCheck_NewestMarkedCurrent(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    got := out.String()
    if !strings.Contains(got, "new-sw") || !strings.Contains(got, "(current)") {
        t.Error("newest path should be marked current")
    }
    // Verify old-sw appears as stale, not current
    lines := strings.Split(got, "\n")
    for _, l := range lines {
        if strings.Contains(l, "old-sw") && strings.Contains(l, "(current)") {
            t.Error("old-sw should not be current")
        }
    }
}

// Bats: "multiple services: both service names appear"
func TestCheck_MultipleServices(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceCamera|/nix/store/a-cam/bin/camera|1000\n" +
        "kTCCServiceCamera|/nix/store/b-cam/bin/camera|2000\n" +
        "kTCCServiceListenEvent|/nix/store/a-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/b-sw/bin/sleepwatcher|2000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    got := out.String()
    if !strings.Contains(got, "kTCCServiceCamera") {
        t.Error("Camera service missing")
    }
    if !strings.Contains(got, "kTCCServiceListenEvent") {
        t.Error("ListenEvent service missing")
    }
}

// Bats: "non-nix entries ignored: paths outside /nix/store produce no output"
// (This is enforced at the SQL level — the query has WHERE client LIKE '/nix/store/%'.
// Test that a query returning zero rows yields the no-duplicates message.)
func TestCheck_NonNixEntriesIgnoredAtQueryLevel(t *testing.T) {
    // The SQL filter is tested by returning empty stdout (as if no rows matched)
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte{}}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    if !strings.Contains(out.String(), "No TCC duplicates found") {
        t.Error("empty rows should yield no-duplicates message")
    }
}

// Bats: "groups different versions of same binary together" (4 bash entries)
func TestCheck_FourEntriesTwoVersionsOneGroup(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceMicrophone|/nix/store/aaa-bash-5.2p37/bin/bash|1000\n" +
        "kTCCServiceMicrophone|/nix/store/bbb-bash-5.2p37/bin/bash|2000\n" +
        "kTCCServiceMicrophone|/nix/store/ccc-bash-5.3p3/bin/bash|3000\n" +
        "kTCCServiceMicrophone|/nix/store/ddd-bash-5.3p3/bin/bash|4000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    got := out.String()
    if !strings.Contains(got, "4 entries") {
        t.Errorf("want 4 entries in one group; got:\n%s", got)
    }
    if !strings.Contains(got, "3 stale") {
        t.Errorf("want 3 stale; got:\n%s", got)
    }
    if !strings.Contains(got, "ddd-bash-5.3p3") || !strings.Contains(got, "(current)") {
        t.Error("ddd-bash-5.3p3 should be current (highest lastModified)")
    }
}

// Bats: "cleanup instructions: duplicates found includes System Preferences and remove stale entries manually"
func TestCheck_CleanupInstructionsPresent(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceListenEvent|/nix/store/a-sw/bin/sleepwatcher|1000\n" +
        "kTCCServiceListenEvent|/nix/store/b-sw/bin/sleepwatcher|2000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    got := out.String()
    if !strings.Contains(got, "System Preferences") {
        t.Error("missing System Preferences in cleanup line")
    }
    if !strings.Contains(got, "remove stale entries manually") {
        t.Error("missing 'remove stale entries manually' in cleanup line")
    }
}

// Bats: "disabled entries excluded: all-disabled duplicates produce no output"
// auth_value != 2 are excluded by the WHERE clause → returns empty → no-duplicates
func TestCheck_DisabledEntriesExcludedAtQueryLevel(t *testing.T) {
    // Same as NonNixEntriesIgnored — both are WHERE-clause filters;
    // FakeRunner returns empty stdout simulating the SQL filter.
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte{}}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    if !strings.Contains(out.String(), "No TCC duplicates found") {
        t.Error("disabled entries should yield no-duplicates message")
    }
}

// Bats: "mixed enabled/disabled with single enabled produces no output"
// Only 1 enabled entry after WHERE filter → no duplicate group
func TestCheck_SingleEnabledEntryNoDuplicate(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    // One row: only the auth_value=2 entry passes the WHERE clause
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceCamera|/nix/store/bbb-bash-5.3p3/bin/bash|2000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    if !strings.Contains(out.String(), "No TCC duplicates found") {
        t.Error("single enabled entry should yield no-duplicates message")
    }
}

// Bats: "only enabled duplicates are reported" (2 enabled, 1 disabled → 2-entry group)
func TestCheck_OnlyEnabledDuplicatesReported(t *testing.T) {
    f := exec.NewFakeRunner()
    f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
    // SQL returns only the 2 auth_value=2 rows
    f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
        "kTCCServiceCamera|/nix/store/bbb-bash-5.3p3/bin/bash|2000\n" +
        "kTCCServiceCamera|/nix/store/ccc-bash-5.3p3/bin/bash|3000\n",
    )}, nil)

    var out bytes.Buffer
    New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB})
    got := out.String()
    if !strings.Contains(got, "2 entries") {
        t.Errorf("want 2 entries; got:\n%s", got)
    }
    if !strings.Contains(got, "1 stale") {
        t.Errorf("want 1 stale; got:\n%s", got)
    }
    if strings.Contains(got, "aaa-bash") {
        t.Error("disabled aaa-bash entry should not appear (filtered by SQL)")
    }
}
```

> Note on bats' "disabled entries excluded" tests: the bash uses `WHERE auth_value = 2` in the SQL query. The Go port preserves the same query verbatim. The FakeRunner simulates the SQL result; it is the implementer's responsibility to verify the actual query string sent to `sqlite3` matches the bash (`auth_value = 2`, not some other value).

- [ ] **Step 2:** Run `go test ./internal/osx/ -v` → all PASS.
- [ ] **Step 3:** Commit — `test(pn/osx/tcc): port remaining bats cases to Go`

---

### Task 4: Verify nix packaging

**Files:** `modules/pn/default.nix` (if sqlite3 is missing from runtimeDeps)

- [ ] **Step 1:** Check `modules/pn/default.nix` for `runtimeDeps`. If `pkgs.sqlite` (the package providing `sqlite3`) is absent, add it. The old `pn-osx-tcc-check/default.nix` had:

  ```nix
  runtimeDeps = [ pkgs.sqlite ];
  testDeps    = [ pkgs.sqlite ];
  ```

  The Go binary packaging in `modules/pn/default.nix` may use a different mechanism (e.g. `buildInputs` or a `runtimeDeps` list depending on `mkGoBinary`'s interface). Add `pkgs.sqlite` if absent.

- [ ] **Step 2:** Commit if changed — `fix(pn): add sqlite3 to runtimeDeps for osx tcc-check`

---

### Task 5: Full verification gate

- [ ] **Step 1:** `cd modules/pn && go test ./... -v` → all PASS.
- [ ] **Step 2:** `go vet ./...` → clean.
- [ ] **Step 3:** From repo root: `nix flake check` → PASS (builds pn, runs Go tests in the derivation).
- [ ] **Step 4:** Build-only activation check (NEVER switch): `darwin-rebuild build --flake .` → PASS.
- [ ] **Step 5: Manual smoke (read-only):**
  - `nix run .#pn -- osx tcc-check` — if terminal has FDA, observe either the duplicate report or "No TCC duplicates found" printed to stdout. If FDA is not granted, observe the warning on stderr and exit 0.
  - Do NOT run against a real TCC.db in CI or automated contexts.
- [ ] **Step 6:** Open PR via `integrate-branch`.

---

## Test Strategy Summary

| Test                                                    | File                        | What it covers                                                                  |
| ------------------------------------------------------- | --------------------------- | ------------------------------------------------------------------------------- |
| `TestNew_ExposesRunner` (existing)                      | `tcc_test.go`               | constructor                                                                     |
| `TestCheck_ProbesThenQueries` (update)                  | `tcc_test.go`               | two sqlite3 calls, updated for new signature                                    |
| `TestCheck_NoFDAExitsCleanly` (update)                  | `tcc_test.go`               | FDA probe failure → nil error                                                   |
| `TestCheck_NoFDAWritesWarning` (new T0)                 | `tcc_test.go`               | warning goes to errOut, out empty                                               |
| `TestCheck_PropagatesQueryError` (existing, update sig) | `tcc_test.go`               | query error propagates                                                          |
| `TestParseTCCRows_*`                                    | `tcc_format_test_darwin.go` | pipe parsing, malformed skipped, empty                                          |
| `TestGroupTCCEntries_*`                                 | `tcc_format_test_darwin.go` | no-dupe → empty, dupe → group, basename version collapse, sort order            |
| `TestServiceDisplayName_*`                              | `tcc_format_test_darwin.go` | 5 known + 1 unknown service                                                     |
| `TestFormatTCCReport_GoldenSingleGroup`                 | `tcc_format_test_darwin.go` | substring validation                                                            |
| `TestFormatTCCReport_GoldenMultipleGroups`              | `tcc_format_test_darwin.go` | two groups, cleanup once                                                        |
| `TestFormatTCCReport_ExactBytes`                        | `tcc_format_test_darwin.go` | full byte-exact golden (parity bar)                                             |
| `TestCheck_NoDuplicatesPrintsClean`                     | `tcc_test.go`               | end-to-end no-dupe                                                              |
| `TestCheck_DuplicatesPrintsReport`                      | `tcc_test.go`               | end-to-end duplicate                                                            |
| `TestCheck_GoldenDuplicates`                            | `tcc_test.go`               | end-to-end byte-exact golden                                                    |
| `TestCheck_EmptyQueryOutput`                            | `tcc_test.go`               | empty stdout → no-dupe                                                          |
| All bats-ported cases (T3)                              | `tcc_test.go`               | newest-current, multi-service, disabled/enabled, version grouping, cleanup text |

---

## Constraints and Risks

### No new dependencies

- `sqlite3` stays a subprocess via `exec.Runner`. No pure-Go sqlite library. `go.mod` must not change; no `gomod2nix.toml` regeneration needed.
- `path/filepath` (`filepath.Base`) is stdlib — no new import beyond what's already used.

### Darwin-only

- All files in `internal/osx/` carry `//go:build darwin`. This is non-negotiable: TCC.db only exists on macOS.
- The `tcc_format_darwin.go` helper file must also carry `//go:build darwin` even though its logic is pure Go, because it defines types (`tccEntry`, `tccGroup`) used by `tcc_darwin.go`.

### sqlite3 runtimeDeps

- The original bash package listed `pkgs.sqlite` under `runtimeDeps` and `testDeps` in its `default.nix`. Verify `modules/pn/default.nix` provides `sqlite3` on PATH at runtime. If the `mkGoBinary` builder doesn't include it, add it (Task 4).

### Query string must be extracted as a named constant

- Both production code and tests reference the duplicates query string. To prevent test drift, define `const tccDuplicatesQuery = "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"` in `tcc_darwin.go` and use it in both the `Check` call and the FakeRunner `AddResponse` args.

### auth_value semantics

- `auth_value = 2` means "granted" in TCC. The original bash query uses this exact filter. Do not change it. The bats tests for "disabled entries excluded" rely on the SQL doing the filtering — no Go-level filtering of `auth_value` is needed.

### Current entry with multiple ties

- If two entries have identical `last_modified`, the awk tracks whichever was seen last (overwrites). The Go port does the same (an `>` comparison, not `>=`). This is an edge case not covered in the bash tests.

### Never sudo/real sqlite in tests

- All sqlite3 calls go through FakeRunner. No test should create a real TCC.db or call `sqlite3` binary. The `testDB = "/tmp/test-TCC.db"` constant in the existing test is a path used as a FakeRunner key only — no real file is created.

---

## Acceptance Criteria

The bead is complete when ALL of the following are true:

1. **`go test ./internal/osx/ -v` passes** with green output for all tests including the new format/group/golden tests.
2. **`go test ./... -v` passes** (no regressions in other packages).
3. **`go vet ./...` is clean.**
4. **`nix flake check` passes.**
5. **`darwin-rebuild build --flake .` passes** (build-only).
6. **`pn osx tcc-check` with FDA granted** produces either:
   - `✅ No TCC duplicates found` (if no `/nix/store/…` duplicates with `auth_value=2`)
   - Or the formatted duplicate report with header, separator, group blocks, and cleanup line — exactly as the bash produced.
7. **`pn osx tcc-check` without FDA** exits 0, prints nothing to stdout, and prints the warning to stderr.
8. **Output is not withheld:** the function writes to the injected `out`/`errOut` writers (cobra's `cmd.OutOrStdout()`/`cmd.ErrOrStderr()`), not discarded.
9. **`TestFormatTCCReport_ExactBytes` passes** — the golden test locks byte-for-byte parity with the bash output format (em dash U+2014, check mark U+2713, ballot X U+2717, heavy horizontal U+2501, the exact spacing and structure).
10. **No new Go module dependencies** (`go.mod` and `gomod2nix.toml` are unchanged).
11. **All bats test scenarios are ported** to Go tests in `tcc_test.go` and pass.

---

## Key File Paths

- Stub (to modify): `modules/pn/internal/osx/tcc_darwin.go`
- Tests (to update): `modules/pn/internal/osx/tcc_test.go`
- New pure helpers: `modules/pn/internal/osx/tcc_format_darwin.go`
- New pure helper tests: `modules/pn/internal/osx/tcc_format_test_darwin.go` (note: Go test files must end in `_test.go`; the build tag variant is `tcc_format_darwin_test.go`)
- CLI wiring (to update): `modules/pn/internal/cli/osx_darwin.go`
- Exec seam reference: `modules/pn/internal/exec/fake.go`
- Store plan (pattern reference): `docs/superpowers/plans/2026-06-26-pn-store-parity.md`
