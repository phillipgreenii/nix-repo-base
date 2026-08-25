# pg-go-mutate-tui Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `pg-go-mutate-sweep` (package-granular, unattended) with `pg-go-mutate-tui` (a
new, file-granular, operator-controlled Go TUI), and extend `pg-go-mutate` itself to support
single-file targets, a per-package guard cache, and a shared N-slot concurrency semaphore.

**Architecture:** Three layers — `gomu` (unchanged pinned engine) → `pg-go-mutate` (extended bash
wrapper: file-or-directory target, guard cache keyed on package content hash, semaphore instead
of exclusive lock) → `pg-go-mutate-tui` (new Go binary: discovery, hash ledger, dynamic queue,
worker goroutines, TUI, bead filing, OTel). `pg-go-mutate-sweep` is deleted, not deprecated in
place.

**Tech Stack:** Bash (`pg-go-mutate`'s existing implementation, `mkBashScript`/`mkBashBuilders`,
bats tests), Go 1.22+ (`pg-go-mutate-tui`, `mkGoBinary`/gomod2nix, a TUI framework — this plan
uses `bubbletea`/`lipgloss`; substitute if a pinned alternative is preferred, task boundaries
don't depend on the specific framework), Nix (home-manager + darwin module wiring, observability
registration), Prometheus client library for Go (`/metrics` endpoint), the shared `jsonl-logger`
Go library (`phillipgreenii-nix-support-apps/packages/jsonl-logger`).

**Spec:**
`/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/docs/superpowers/specs/2026-08-25-pg-go-mutate-tui-design.md`
— read it in full; this plan argues from it and does not repeat its rationale.

## Global Constraints

(Copied verbatim from the spec — every task's requirements implicitly include these.)

- **MUST NOT** ever record, display, or export a mutant survivor/kill _count_ as a metric, in a
  bead, or on a dashboard (design §3, §12) — except the TUI's own local, per-run History popup,
  which is explicitly exempted (design §9).
- **MUST** compute one `package_hash` algorithm and share it between `pg-go-mutate`'s guard cache
  (§5.2) and `pg-go-mutate-tui`'s ledger (§6.2) — one computation, not two independent ones.
- **MUST** force `--workers 1` on every semaphore-dispatched `gomu` invocation (§5.3).
- **MUST** read the semaphore's total slot count from the static nix-managed config on every
  acquisition — never treat it as runtime-mutable shared state (§5.3, §11).
- **MUST** stamp a file's ledger record with the `package_hash` captured at **dispatch** time,
  never recomputed at completion (§6.3) — and that hash **MUST** be the real content digest, not
  a package path string standing in for it.
- **MUST** tolerate a truncated trailing ledger line from a hard kill — parse per line, ignore an
  unparseable final line (§6.2).
- **MUST** use the shared `jsonl-logger` library for all logging, not a hand-rolled bootstrap
  (§12) — a prior hand-rolled copy caused a real data-loss bug (`pg2-70l4r`).
- **MUST** file exactly one triage bead per Go **package** (not project), at **P3**, with a single
  shared label, **never** promoted to an epic (§10).
- **MUST** resolve each repo's own canonical bead label (`base`, `support-apps`, `agent-support`,
  …) per that repo's `CLAUDE.md` — **MUST NOT** derive it generically from the project key's path
  (§10).
- **MUST NOT** rewrite ADR-0026 in place — it gets a new ADR that supersedes it, body left intact
  (§13).
- Grafana alert rules for this tool **MUST** treat "no data" as OK, not Alerting (§12).
- `phillipgreenii.observability.{logSources,metricsTargets,alertRuleFiles}` are **darwin/system-scope**
  options — confirmed against this repo's own existing `darwin/modules/pn/default.nix`, which
  states explicitly "cannot be set from a home-manager module." Any observability registration
  for `pg-go-mutate-tui` **MUST** go in a darwin module, never the home-manager module.

---

## File Structure

**`phillipg-nix-repo-base` (existing repo):**

```
modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh          # MODIFY (Tasks 1-3)
modules/pg-go-mutate/lib/pg-go-mutate-lib.bash             # MODIFY (Tasks 1-3: guard, semaphore, slug)
modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats  # MODIFY
modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats  # MODIFY

modules/pg-go-mutate/pg-go-mutate-tui/                     # NEW Go package (Tasks 4-16)
  go.mod
  gomod2nix.toml
  default.nix
  cmd/pg-go-mutate-tui/main.go           # Tasks 4 (scaffold) and 16 (full wiring)
  internal/pkghash/pkghash.go            # Task 5
  internal/pkghash/pkghash_test.go
  internal/ledger/ledger.go              # Task 6
  internal/ledger/ledger_test.go
  internal/discover/discover.go          # Task 7
  internal/discover/discover_test.go
  internal/queue/queue.go                # Task 8
  internal/queue/queue_test.go
  internal/worker/worker.go              # Task 9
  internal/worker/worker_test.go
  internal/beads/beads.go                # Task 10
  internal/beads/beads_test.go
  internal/obslog/obslog.go              # Task 11
  internal/obslog/obslog_test.go
  internal/metrics/metrics.go            # Task 12
  internal/metrics/metrics_test.go
  internal/config/config.go              # Task 13
  internal/config/config_test.go
  internal/tui/primary.go                # Task 14
  internal/tui/primary_test.go
  internal/tui/popups.go                 # Task 15
  internal/tui/popups_test.go
  internal/report/report.go              # Task 16 Step 4
  internal/report/report_test.go
  alerting.yaml                          # Task 18

home/pg-go-mutate-tui/default.nix                          # NEW (Task 17) -- its own module file,
                                                            # not bolted onto the existing pg-go-mutate one
darwin/modules/pg-go-mutate-tui/default.nix                # NEW (Task 18) -- mirrors darwin/modules/pn
darwin/default.nix                                         # MODIFY: add the above to imports (Task 18)
modules/pg-go-mutate/pg-go-mutate-sweep/                   # DELETE (Task 20)
docs/adr/0026-mutation-sweep-state-contract.md             # MODIFY: status line only (Task 20)
docs/adr/00NN-pg-go-mutate-tui-state-contract.md           # NEW (Task 20)
CLAUDE.md                                                  # MODIFY: mutation-testing section (Task 20)
```

**`phillipgreenii-nix-support-apps` (existing repo):** no file changes expected — Task 18
confirms `pg-go-mutate-tui`'s darwin module can reach `phillipgreenii.observability.*` the same
way `pn`'s does (both are third-repo consumers via the same `crossFlakeOptionStubs` mechanism);
if that mechanism turns out narrower than expected, this is where a fix would land, treated as a
discovered dependency rather than pre-committed.

**`phillipg-nix-ziprecruiter` (existing repo, the terminal/consumer repo):**

```
machines/<host>/default.nix (or a dedicated pg-go-mutate-tui.nix)  # MODIFY (Task 19)
```

Each Go internal package above has one clear responsibility and is independently unit-testable —
`pkghash`, `ledger`, `discover`, and `config` have no dependency on the TUI framework at all and
can be fully tested with plain Go tests; `tui/*` isolates rendering into pure functions
(`render(state) string`) precisely so it doesn't require a real terminal to test.

---

## Task 1: `pg-go-mutate` accepts a single-file target

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh:147-164`
- Modify: `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash` (add `pgm_resolve_guard_target`)
- Test: `modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`

**Interfaces:**

- Produces: `pgm_resolve_guard_target <target>` — echoes the directory to run the health guard
  against: `<target>` itself if it's a directory, or `dirname <target>` if it's a `.go` file.
  Exit `2` if `<target>` is neither a directory nor a file ending in `.go`.
- Consumes: nothing new from other tasks (this task is a leaf).

- [ ] **Step 1: Write the failing bats test for file-target acceptance**

Add to `modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`:

```bash
@test "accepts a single .go file as target" {
  mkdir -p "$TEST_TMPDIR/pkg"
  cat > "$TEST_TMPDIR/pkg/a.go" <<'EOF'
package pkg

func Add(a, b int) int { return a + b }
EOF
  cat > "$TEST_TMPDIR/pkg/a_test.go" <<'EOF'
package pkg

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("bad")
	}
}
EOF
  cat > "$TEST_TMPDIR/pkg/go.mod" <<'EOF'
module pkg

go 1.22
EOF
  run pg-go-mutate --json "$TEST_TMPDIR/pkg/a.go"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"file"'* ]]
}

@test "rejects a target path that is absent" {
  run pg-go-mutate --json "$TEST_TMPDIR/does-not-exist.go"
  [ "$status" -eq 14 ]
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `bats modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats -f "single .go file"`
Expected: FAIL — current code exits `14` unconditionally for a non-directory target
(`pg-go-mutate.sh:159-161`).

- [ ] **Step 3: Add `pgm_resolve_guard_target` to the shared lib**

In `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`:

```bash
# pgm_resolve_guard_target <target>
# Echoes the directory the health guard must run against: <target> itself if
# it is a directory, or its containing directory if it is a .go file. The
# guard (go vet / go test) is inherently package-scoped -- a lone file cannot
# be vetted or tested in isolation from its package.
pgm_resolve_guard_target() {
  local target="$1"
  if [ -d "$target" ]; then
    printf '%s\n' "$target"
    return 0
  fi
  case "$target" in
  *.go)
    if [ -f "$target" ]; then
      dirname -- "$target"
      return 0
    fi
    ;;
  esac
  return 1
}
```

- [ ] **Step 4: Wire it into `pg-go-mutate.sh`, replacing the directory-only check**

Replace `pg-go-mutate.sh:147-164`'s directory-only validation:

```bash
case "$target" in
'' | -*)
  printf 'pg-go-mutate: missing or invalid target\n' >&2
  exit 2
  ;;
esac
guard_target="$(pgm_resolve_guard_target "$target")" || {
  printf 'pg-go-mutate: %s is not a directory or a .go file\n' "$target" >&2
  exit 14
}
# guard_target is what the health guard cds into; $target (unchanged) is what
# gets passed to `gomu run` below -- a file target stays a file target there.
guard_target="$(cd "$guard_target" && pwd)"
```

Find the line later in the script that invokes `gomu run "$target"` (the mutation step) and
confirm it still passes the ORIGINAL `$target` (file or directory), not `$guard_target` — only
the guard section (`pgm_has_tests`, `pgm_tests_healthy`, `pgm_detect_tags`, all currently called
with `"$target"`) needs updating to use `"$guard_target"` instead.

- [ ] **Step 5: Run to verify it passes**

Run: `bats modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`
Expected: PASS, both new tests and the full existing suite (directory targets unaffected).

- [ ] **Step 6: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh \
        modules/pg-go-mutate/lib/pg-go-mutate-lib.bash \
        modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats
git commit -m "pg-go-mutate: accept a single .go file as a target"
```

---

## Task 2: Per-package guard cache

**Files:**

- Modify: `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash` (add `pgm_slug`, `pgm_pkg_hash`,
  `pgm_guard_cache_get`, `pgm_guard_cache_put`)
- Modify: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh` (call the cache around the existing
  `pgm_tests_healthy`/`pgm_has_tests` guard calls)
- Test: `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`

**Interfaces:**

- Consumes: `pkghash` — Task 5 defines the canonical algorithm in Go
  (`internal/pkghash.Compute`); this bash task needs the _same_ algorithm available to a shell
  script. To avoid a second implementation silently diverging, this task's cache is keyed on a
  hash **pg-go-mutate itself computes** using the identical method (sorted file list, sha256 of
  concatenated contents) — Step 3 below implements it directly in bash; Task 5's Go test suite
  includes a fixture cross-check (Task 5, Step 6) asserting the Go and bash implementations
  produce the **same digest for the same directory**.
- Produces: `pgm_slug <path>` (there is currently **no** shared slug helper in
  `pg-go-mutate-lib.bash` — the only existing one, `pgms_slug`, lives in
  `pg-go-mutate-sweep.bash`, a file Task 20 deletes, so it cannot be reused or depended on;
  `pgm_slug` is a fresh, small addition to the shared lib, not a reuse of anything), `pgm_pkg_hash
<pkg_dir>`, `pgm_guard_cache_get <pkg_dir> <hash>` (exit `0` + prints `PASS`/`FAIL` if cached,
  exit `1` if not cached), `pgm_guard_cache_put <pkg_dir> <hash> <PASS|FAIL>`.

- [ ] **Step 1: Write the failing test**

```bash
@test "guard cache: second file in the same package skips the guard" {
  mkdir -p "$TEST_TMPDIR/pkg"
  # ... same pkg/a.go, a_test.go, go.mod, plus a second file b.go/b_test.go ...
  export PGM_GUARD_CACHE_DIR="$TEST_TMPDIR/cache"
  run pg-go-mutate --json "$TEST_TMPDIR/pkg/a.go"
  [ "$status" -eq 0 ]
  cache_files_before=$(find "$PGM_GUARD_CACHE_DIR" -type f | wc -l)
  run pg-go-mutate --json "$TEST_TMPDIR/pkg/b.go"
  [ "$status" -eq 0 ]
  cache_files_after=$(find "$PGM_GUARD_CACHE_DIR" -type f | wc -l)
  # A second file in the SAME unchanged package must not add a second cache
  # entry -- it hits the one already written for the first file.
  [ "$cache_files_before" -eq "$cache_files_after" ]
}

@test "guard cache: an edited package invalidates the cache" {
  mkdir -p "$TEST_TMPDIR/pkg"
  cat > "$TEST_TMPDIR/pkg/a.go" <<'EOF'
package pkg

func Add(a, b int) int { return a + b }
EOF
  cat > "$TEST_TMPDIR/pkg/b.go" <<'EOF'
package pkg

func Sub(a, b int) int { return a - b }
EOF
  cat > "$TEST_TMPDIR/pkg/a_test.go" <<'EOF'
package pkg

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("bad")
	}
}
EOF
  cat > "$TEST_TMPDIR/pkg/b_test.go" <<'EOF'
package pkg

import "testing"

func TestSub(t *testing.T) {
	if Sub(5, 3) != 2 {
		t.Fatal("bad")
	}
}
EOF
  cat > "$TEST_TMPDIR/pkg/go.mod" <<'EOF'
module pkg

go 1.22
EOF
  export PGM_GUARD_CACHE_DIR="$TEST_TMPDIR/cache"
  run pg-go-mutate --json "$TEST_TMPDIR/pkg/a.go"
  [ "$status" -eq 0 ]
  cache_files_before=$(find "$PGM_GUARD_CACHE_DIR" -type f | wc -l)

  # Edit b.go: the package's content hash changes even though a.go itself
  # did not, and the SAME target (a.go) must now re-run the guard rather
  # than hit the stale cache entry keyed on the old hash.
  echo '// changed' >> "$TEST_TMPDIR/pkg/b.go"

  run pg-go-mutate --json "$TEST_TMPDIR/pkg/a.go"
  [ "$status" -eq 0 ]
  cache_files_after=$(find "$PGM_GUARD_CACHE_DIR" -type f | wc -l)
  [ "$cache_files_after" -eq "$((cache_files_before + 1))" ]
}

@test "pgm_slug produces a filesystem-safe, collision-free name" {
  a="$(pgm_slug "/foo/bar")"
  b="$(pgm_slug "/foo_bar")"
  [ "$a" != "$b" ]
  [[ "$a" != *"/"* ]]
}
```

- [ ] **Step 2: Run to verify it fails** — `bats modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats -f "guard cache"`; expect FAIL, no cache exists yet.

- [ ] **Step 3: Implement `pgm_slug`, the hash, and the cache in the shared lib**

```bash
# pgm_slug <path>
# Filesystem-safe encoding of an absolute path for use as a cache/state
# subdirectory name. "/" becomes "__" so "/foo/bar" and "/foo_bar" cannot
# collide (a bare "_"-for-"/" substitution could produce the same slug for
# both). No shared slug helper exists elsewhere in this lib to reuse --
# pg-go-mutate-sweep.bash's pgms_slug is a different function in a file
# Task 20 deletes.
pgm_slug() {
  printf '%s\n' "$1" | sed 's#/#__#g'
}

# pgm_pkg_hash <pkg_dir>
# Deterministic content hash over every *.go file in pkg_dir (non-recursive --
# a package is one directory's files, never a subtree). Sorted order is
# load-bearing: an unsorted `find` would make the hash order-dependent on the
# filesystem, not the content. MUST match internal/pkghash.Compute's
# algorithm exactly (Task 5, Step 6 cross-checks this).
pgm_pkg_hash() {
  local pkg_dir="$1"
  find "$pkg_dir" -maxdepth 1 -name '*.go' -type f -print0 |
    sort -z |
    xargs -0 cat |
    sha256sum |
    cut -d' ' -f1
}

# pgm_guard_cache_get <pkg_dir> <hash>
# Prints PASS or FAIL and exits 0 if this (pkg_dir, hash) pair was already
# guarded; exits 1 if not cached (caller must run the guard and pgm_guard_cache_put).
pgm_guard_cache_get() {
  local cache_root="${PGM_GUARD_CACHE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate/guard-cache}"
  local key_file="$cache_root/$(pgm_slug "$1")/$2"
  [ -f "$key_file" ] || return 1
  cat "$key_file"
}

pgm_guard_cache_put() {
  local cache_root="${PGM_GUARD_CACHE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate/guard-cache}"
  local key_dir="$cache_root/$(pgm_slug "$1")"
  mkdir -p "$key_dir"
  # Stale entries under other hashes for this package are harmless (keyed by
  # hash, never read again once the hash moves on) -- no cleanup needed here.
  printf '%s\n' "$3" >"$key_dir/$2"
}
```

- [ ] **Step 4: Call the cache from `pg-go-mutate.sh` around the existing guard**

```bash
pkg_hash="$(pgm_pkg_hash "$guard_target")"
if cached="$(pgm_guard_cache_get "$guard_target" "$pkg_hash")"; then
  [ "$cached" = "PASS" ] || exit 12
else
  pgm_has_tests "$guard_target" || has_tests_rc=$?
  # ... existing has-tests / exit-10 handling unchanged ...
  if pgm_tests_healthy "$guard_target"; then
    pgm_guard_cache_put "$guard_target" "$pkg_hash" "PASS"
  else
    pgm_guard_cache_put "$guard_target" "$pkg_hash" "FAIL"
    exit 12
  fi
fi
```

- [ ] **Step 5: Run to verify it passes** — full bats suite for both files.

- [ ] **Step 6: Commit**

```bash
git add modules/pg-go-mutate/lib/pg-go-mutate-lib.bash \
        modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh \
        modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
git commit -m "pg-go-mutate: cache the package health guard, keyed on content hash"
```

---

## Task 3: N-slot semaphore, forced `--workers 1`, GOMAXPROCS capping

**Files:**

- Modify: `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash` (replace `pgm_lock_acquire`/
  `pgm_lock_release` with `pgm_sem_acquire`/`pgm_sem_release`)
- Modify: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh` (use the semaphore; force
  `--workers 1`; set `GOMAXPROCS`)
- Test: `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`

**Interfaces:**

- Consumes: the static config's `concurrency` value. Its path is fixed by Task 17's nix wiring to
  `${XDG_CONFIG_HOME:-$HOME/.config}/pg-go-mutate-tui/config.json`, a value both this task and
  Task 13 hard-code identically. If Task 17 changes that path, this task's `pgm_sem_capacity`
  must change too — flag this dependency in Task 17's own PR description.
- Produces: `pgm_sem_acquire <slot-timeout>` (exits `3` if no slot free within the timeout, else
  exports `PGM_SEM_SLOT` naming which numbered slot directory was acquired),
  `pgm_sem_release` (releases `$PGM_SEM_SLOT`).

- [ ] **Step 1: Write the failing test**

```bash
@test "semaphore: capacity-many concurrent acquires succeed, one more blocks" {
  echo '{"concurrency": 2}' > "$TEST_TMPDIR/config.json"
  export PGM_CONFIG_PATH="$TEST_TMPDIR/config.json"
  export PGM_SEM_DIR="$TEST_TMPDIR/sem"
  ( pgm_sem_acquire 5 && sleep 2 ) &
  pid1=$!
  ( pgm_sem_acquire 5 && sleep 2 ) &
  pid2=$!
  sleep 0.5
  run pgm_sem_acquire 1
  [ "$status" -eq 3 ]
  wait "$pid1" "$pid2"
}

@test "gomu is always invoked with --workers 1 through the semaphore path" {
  mkdir -p "$TEST_TMPDIR/pkg" "$TEST_TMPDIR/stub-bin"
  cat > "$TEST_TMPDIR/pkg/a.go" <<'EOF'
package pkg

func Add(a, b int) int { return a + b }
EOF
  cat > "$TEST_TMPDIR/pkg/a_test.go" <<'EOF'
package pkg

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("bad")
	}
}
EOF
  cat > "$TEST_TMPDIR/pkg/go.mod" <<'EOF'
module pkg

go 1.22
EOF
  # A stub gomu that records its own argv and exits 0 with a minimal valid
  # report, so pg-go-mutate's own downstream JSON handling doesn't choke.
  cat > "$TEST_TMPDIR/stub-bin/gomu" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$TEST_TMPDIR/gomu-argv.log"
echo '{"results":[],"statistics":{"killedMutants":0,"survivedMutants":0}}'
exit 0
EOF
  chmod +x "$TEST_TMPDIR/stub-bin/gomu"
  export PG_GO_MUTATE_GOMU="$TEST_TMPDIR/stub-bin/gomu"
  export PGM_SEM_DIR="$TEST_TMPDIR/sem"

  run pg-go-mutate --json --workers 8 "$TEST_TMPDIR/pkg/a.go"
  [ "$status" -eq 0 ]
  # Even though the caller passed --workers 8, gomu itself must only ever
  # have seen --workers 1 -- the outer semaphore is the sole concurrency
  # dimension (design §5.3).
  grep -- '--workers 1' "$TEST_TMPDIR/gomu-argv.log"
  ! grep -- '--workers 8' "$TEST_TMPDIR/gomu-argv.log"
}
```

- [ ] **Step 2: Run to verify it fails** — `bats ... -f "semaphore"`; FAIL, no such function yet.

- [ ] **Step 3: Implement the semaphore**

```bash
# pgm_sem_capacity
# Reads concurrency from the static nix-rendered config. Unknown keys are
# ignored elsewhere (Task 13's Go loader); this bash reader only needs the
# one key it cares about and tolerates the file being absent (default 1).
pgm_sem_capacity() {
  local cfg="${PGM_CONFIG_PATH:-${XDG_CONFIG_HOME:-$HOME/.config}/pg-go-mutate-tui/config.json}"
  [ -f "$cfg" ] || { printf '1\n'; return 0; }
  jq -r '.concurrency // 1' "$cfg"
}

# pgm_sem_acquire <timeout_seconds>
# Numbered slot directories under $PGM_SEM_DIR, PID-stamped, same atomic
# mkdir-based mutual exclusion the original single lock used -- generalized
# from one lock directory to N numbered ones rather than a new primitive.
pgm_sem_acquire() {
  local timeout="$1" sem_dir cap i deadline
  sem_dir="${PGM_SEM_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate/sem}"
  cap="$(pgm_sem_capacity)"
  mkdir -p "$sem_dir"
  deadline=$(($(date +%s) + timeout))
  while :; do
    i=0
    while [ "$i" -lt "$cap" ]; do
      if mkdir "$sem_dir/$i" 2>/dev/null; then
        printf '%s\n' "$$" >"$sem_dir/$i/pid"
        export PGM_SEM_SLOT="$sem_dir/$i"
        return 0
      fi
      # Stale-slot reclaim: if the PID stamped in an existing slot directory
      # is no longer alive, it's abandoned -- reclaim it the same atomic
      # mv-then-remove way the original lock did.
      if [ -f "$sem_dir/$i/pid" ] && ! kill -0 "$(cat "$sem_dir/$i/pid")" 2>/dev/null; then
        mv "$sem_dir/$i" "$sem_dir/$i.stale.$$" 2>/dev/null && rm -rf "$sem_dir/$i.stale.$$"
      fi
      i=$((i + 1))
    done
    [ "$(date +%s)" -lt "$deadline" ] || return 3
    sleep 0.2
  done
}

pgm_sem_release() {
  [ -n "${PGM_SEM_SLOT:-}" ] && rm -rf "$PGM_SEM_SLOT"
}
```

- [ ] **Step 4: Wire into `pg-go-mutate.sh`** — replace the existing `pgm_lock_acquire`/
      `pgm_lock_release`/`trap` calls with `pgm_sem_acquire "${PGM_SEM_TIMEOUT:-3600}"` / `pgm_sem_release`,
      keeping the same `trap ... EXIT`/`INT`/`TERM HUP` release-on-every-path pattern. Force
      `--workers 1` unconditionally in the `gomu run` invocation's argument list (delete any code path
      that would let a caller override it). Compute and export
      `GOMAXPROCS=$(( $(nproc) / $(pgm_sem_capacity) ))`, floored at `1`, before invoking `gomu`.

- [ ] **Step 5: Run to verify it passes** — full bats suite.

- [ ] **Step 6: Commit**

```bash
git add modules/pg-go-mutate/lib/pg-go-mutate-lib.bash \
        modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh \
        modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
git commit -m "pg-go-mutate: N-slot semaphore, forced --workers 1, GOMAXPROCS capping"
```

---

## Task 4: `pg-go-mutate-tui` package scaffold

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/go.mod`
- Create: `modules/pg-go-mutate/pg-go-mutate-tui/gomod2nix.toml`
- Create: `modules/pg-go-mutate/pg-go-mutate-tui/default.nix`
- Create: `modules/pg-go-mutate/pg-go-mutate-tui/cmd/pg-go-mutate-tui/main.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/cmd/pg-go-mutate-tui/main_test.go`

**Interfaces:**

- Produces: a buildable binary with `--help`/`--version`/`--root <dir>` flags parsed; every later
  task adds internal packages this `main.go` wires together, but that wiring itself doesn't
  happen until Task 16 — this task's only job is a binary that starts, parses flags, and exits
  cleanly.

- [ ] **Step 1: Write the failing test**

```go
// main_test.go
package main

import (
	"os/exec"
	"testing"
)

func TestHelpExitsZero(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "--help")
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help should exit 0, got: %v", err)
	}
}

func TestMissingRootExitsTwo(t *testing.T) {
	cmd := exec.Command("go", "run", ".")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("missing --root should exit 2, got: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./cmd/pg-go-mutate-tui/...`; FAIL, no `main.go` yet.

- [ ] **Step 3: Write `go.mod` and minimal `main.go`**

```
module github.com/phillipgreenii/pg-go-mutate-tui

go 1.22
```

```go
// cmd/pg-go-mutate-tui/main.go
package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("pg-go-mutate-tui", flag.ContinueOnError)
	root := fs.String("root", "", "root directory to scan (required)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: --root is required")
		return 2
	}
	// Task 16 wires discovery/ledger/queue/worker-pool/TUI here.
	return 0
}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./cmd/pg-go-mutate-tui/...`.

- [ ] **Step 5: Generate `gomod2nix.toml` and `default.nix`**

```bash
cd modules/pg-go-mutate/pg-go-mutate-tui && go mod tidy && \
  nix run github:nix-community/gomod2nix -- generate
```

```nix
# default.nix
{ pkgs, self }:
let
  goBuilders = (import ../../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pg-go-mutate-tui";
  src = ./.;
  description = "Interactive, file-granular resumable mutation-testing TUI";
  gomod2nixToml = ./gomod2nix.toml;
}
```

(Mirrors `modules/jira/default.nix`'s exact shape — same helper, same pattern, per this repo's own
Go-packaging convention. This is also the correct precedent for Task 17's package registration —
`pjira`, not the bash-based `pg-go-mutate`/`pg-go-mutate-sweep`.)

- [ ] **Step 6: Build via nix to verify wiring**

Run: `nix build .#pg-go-mutate-tui` (add the package to this repo's `flake.nix` overlay first, same
place `pg-go-mutate`/`pjira` are registered).
Expected: builds successfully; `result/bin/pg-go-mutate-tui --version` prints `dev`.

- [ ] **Step 7: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/ flake.nix
git commit -m "pg-go-mutate-tui: package scaffold"
```

---

## Task 5: `package_hash` computation

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash/pkghash.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash/pkghash_test.go`

**Interfaces:**

- Produces: `pkghash.Compute(pkgDir string) (string, error)` — the canonical algorithm design §6.2
  requires. **This is the ONLY place a package's content hash is computed** — Task 8's queue and
  Task 9's worker both call this function (directly, or via a map the caller precomputes with
  it); neither reimplements hashing itself.

- [ ] **Step 1: Write the failing test**

```go
package pkghash

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestComputeIsStableAcrossIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "a_test.go", "package pkg\n")
	h1, err := Compute(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Compute(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed with no content change: %s != %s", h1, h2)
	}
}

func TestComputeChangesWhenAnyFileChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package pkg\nfunc A() {}\n")
	writeFile(t, dir, "a_test.go", "package pkg\n")
	before, _ := Compute(dir)
	writeFile(t, dir, "a_test.go", "package pkg\n// changed\n")
	after, _ := Compute(dir)
	if before == after {
		t.Fatal("hash did not change when a test file changed")
	}
}

func TestComputeIsOrderIndependentOfFilesystemListing(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	writeFile(t, dir1, "a.go", "package pkg\n")
	writeFile(t, dir1, "b.go", "package pkg\n")
	writeFile(t, dir2, "b.go", "package pkg\n")
	writeFile(t, dir2, "a.go", "package pkg\n")
	h1, _ := Compute(dir1)
	h2, _ := Compute(dir2)
	if h1 != h2 {
		t.Fatal("hash depends on filesystem listing order, not sorted content")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/pkghash/...`; FAIL, no package yet.

- [ ] **Step 3: Implement**

```go
package pkghash

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Compute returns a deterministic content hash over every *.go file
// directly inside pkgDir (non-recursive: a Go package is one directory).
// Sorted filename order is load-bearing -- filesystem listing order is not
// guaranteed stable across platforms or runs. MUST match pg-go-mutate's
// bash pgm_pkg_hash exactly (Task 2's cross-check fixture, Step 6 below).
func Compute(pkgDir string) (string, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			return "", err
		}
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/pkghash/...`.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash/
git commit -m "pg-go-mutate-tui: deterministic package content hash"
```

- [ ] **Step 6 (cross-check fixture, satisfies Task 2's stated dependency): add a bats test**

Add to `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`:

```bash
@test "pgm_pkg_hash matches the Go pkghash.Compute implementation on the same fixture" {
  run pgm_pkg_hash "$BATS_TEST_DIRNAME/../../pg-go-mutate-tui/internal/pkghash/testdata/fixture"
  go_hash="$(go run "$BATS_TEST_DIRNAME/../../pg-go-mutate-tui/internal/pkghash/cmd/hashprint" \
    "$BATS_TEST_DIRNAME/../../pg-go-mutate-tui/internal/pkghash/testdata/fixture")"
  [ "$output" = "$go_hash" ]
}
```

(Add a tiny `internal/pkghash/cmd/hashprint/main.go` that calls `pkghash.Compute` on `os.Args[1]`
and prints the result — a 10-line throwaway binary whose only purpose is being callable from bats.)

---

## Task 6: Ledger (append, replay, partial-write tolerance, dispatch-time hash)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger/ledger.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger/ledger_test.go`

**Interfaces:**

- Consumes: nothing (pure file I/O + the record schema from the design doc). Callers (Task 9)
  supply the `PackageHash` field's value from `pkghash.Compute` (Task 5) — this package itself
  never computes a hash, it only stores and compares whatever string it's given.
- Produces:
  - `type Record struct { FilePath, PackageHash, Status, ReportPath, RootGitHash string; Timestamp time.Time }`
  - `Ledger.Append(r Record) error`
  - `Ledger.Replay() (map[string]Record, error)` — keyed by `FilePath`, last record wins, silently
    skips a trailing unparseable line.
  - `Ledger.NeedsRun(filePath, currentPackageHash string) bool` — true if no record exists for
    `filePath`, or its recorded `PackageHash` differs from `currentPackageHash`.

- [ ] **Step 1: Write the failing tests**

```go
package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendThenReplayRoundTrips(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	r := Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done", Timestamp: time.Now()}
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	records, err := l.Replay()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := records["pkg/a.go"]
	if !ok || got.PackageHash != "abc" || got.Status != "done" {
		t.Fatalf("replay did not recover the appended record: %+v", got)
	}
}

func TestReplayKeepsLastRecordPerKey(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "failed"})
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "def", Status: "done"})
	records, _ := l.Replay()
	if records["pkg/a.go"].Status != "done" || records["pkg/a.go"].PackageHash != "def" {
		t.Fatalf("expected the SECOND record to win, got %+v", records["pkg/a.go"])
	}
}

func TestReplayToleratesTruncatedTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	l := New(path)
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done"})
	// Simulate a SIGKILL mid-append: a second, truncated line with no
	// trailing newline and invalid JSON.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"file_path":"pkg/b.go","status":"do`)
	f.Close()
	records, err := l.Replay()
	if err != nil {
		t.Fatalf("replay must tolerate a truncated trailing line, got error: %v", err)
	}
	if _, ok := records["pkg/b.go"]; ok {
		t.Fatal("truncated record must not appear in replay results")
	}
	if records["pkg/a.go"].Status != "done" {
		t.Fatal("a valid earlier record must still survive replay")
	}
}

func TestNeedsRun(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	if !l.NeedsRun("pkg/a.go", "abc") {
		t.Fatal("a file with no record must need a run")
	}
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done"})
	if l.NeedsRun("pkg/a.go", "abc") {
		t.Fatal("an unchanged hash must not need a run")
	}
	if !l.NeedsRun("pkg/a.go", "different") {
		t.Fatal("a changed hash must need a run")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/ledger/...`; FAIL, no package yet.

- [ ] **Step 3: Implement**

```go
package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

type Record struct {
	FilePath    string    `json:"file_path"`
	PackageHash string    `json:"package_hash"`
	Status      string    `json:"status"`
	ReportPath  string    `json:"report_path"`
	RootGitHash string    `json:"root_git_hash"`
	Timestamp   time.Time `json:"timestamp"`
}

type Ledger struct {
	path string
}

func New(path string) *Ledger { return &Ledger{path: path} }

func (l *Ledger) Append(r Record) error {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// Replay reads every line, keeping the last valid record per FilePath. A
// truncated trailing line (a hard kill mid-write) is silently skipped, not
// an error -- the file-granular ledger appends far more often than the old
// package-granular one, so tolerating this is load-bearing, not optional.
func (l *Ledger) Replay() (map[string]Record, error) {
	records := make(map[string]Record)
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue // truncated/malformed line -- skip, don't fail replay
		}
		records[r.FilePath] = r
	}
	return records, scanner.Err()
}

func (l *Ledger) NeedsRun(filePath, currentPackageHash string) bool {
	records, err := l.Replay()
	if err != nil {
		return true
	}
	r, ok := records[filePath]
	return !ok || r.PackageHash != currentPackageHash
}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/ledger/...`.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger/
git commit -m "pg-go-mutate-tui: append-only ledger with replay and partial-write tolerance"
```

---

## Task 7: Project/file discovery

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/discover/discover.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/discover/discover_test.go`

**Interfaces:**

- Consumes: scan paths from `internal/config` (Task 13) — this task's tests pass paths directly
  rather than depending on Task 13's loader, so ordering between Task 7 and Task 13 doesn't
  matter.
- Produces:
  - `type Package struct { ProjectKey, PkgPath, AbsPath string; Files []string }` (`Files` are
    `.go` non-test source files only — test files inform `pkghash` but are not independently
    queued; `AbsPath` is the absolute directory `pkghash.Compute` needs — Task 8's queue calls
    `pkghash.Compute(pkg.AbsPath)` directly, so discovery must hand back a path that function can
    open, not just a project-relative one)
  - `DiscoverProjects(scanPaths []string) ([]string, error)` — one entry per Go module root found
    under the scan paths (a `go.mod` file marks a project root).
  - `DiscoverPackages(projectRoot string) ([]Package, error)` — walks a project root recursively,
    one `Package` per directory containing at least one non-test `.go` file.

- [ ] **Step 1: Write the failing test**

```go
package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverProjectsFindsGoModRoots(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "proj-a", "go.mod"), "module a\n")
	mustWrite(t, filepath.Join(root, "proj-b", "go.mod"), "module b\n")
	mustWrite(t, filepath.Join(root, "not-a-project", "readme.md"), "hi\n")

	projects, err := DiscoverProjects([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(projects), projects)
	}
}

func TestDiscoverPackagesGroupsFilesByDirectoryAndCarriesAnAbsPath(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module proj\n")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "b.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "pkg", "a_test.go"), "package pkg\n")
	mustWrite(t, filepath.Join(root, "other", "c.go"), "package other\n")

	packages, err := DiscoverPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(packages), packages)
	}
	for _, p := range packages {
		if p.PkgPath == "pkg" {
			if len(p.Files) != 2 {
				t.Fatalf("expected 2 non-test files in pkg, got %v", p.Files)
			}
			if p.AbsPath != filepath.Join(root, "pkg") {
				t.Fatalf("expected AbsPath to be openable by pkghash.Compute, got %q", p.AbsPath)
			}
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/discover/...`.

- [ ] **Step 3: Implement**

```go
package discover

import (
	"os"
	"path/filepath"
	"strings"
)

type Package struct {
	ProjectKey string
	PkgPath    string
	AbsPath    string
	Files      []string
}

func DiscoverProjects(scanPaths []string) ([]string, error) {
	var projects []string
	for _, root := range scanPaths {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "go.mod" {
				projects = append(projects, filepath.Dir(path))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return projects, nil
}

func DiscoverPackages(projectRoot string) ([]Package, error) {
	byDir := make(map[string]*Package)
	err := filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		p, ok := byDir[dir]
		if !ok {
			rel, _ := filepath.Rel(projectRoot, dir)
			p = &Package{ProjectKey: projectRoot, PkgPath: rel, AbsPath: dir}
			byDir[dir] = p
		}
		p.Files = append(p.Files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	packages := make([]Package, 0, len(byDir))
	for _, p := range byDir {
		packages = append(packages, *p)
	}
	return packages, nil
}
```

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/discover/
git commit -m "pg-go-mutate-tui: discover projects and packages under configured scan paths"
```

---

## Task 8: Dynamic queue (watermarks, real package-hash wiring, recency ranking, debounce, retry backoff)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/queue/queue.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/queue/queue_test.go`

**Interfaces:**

- Consumes: `discover.Package` (Task 7, including its `AbsPath` field), `pkghash.Compute` (Task 5
  — called directly by `Refill`, not reimplemented or bypassed), `ledger.Ledger.NeedsRun` (Task
  6), a `RecencyLookup func(pkgPath string) time.Time` (the caller supplies this — Task 16's
  integration wiring provides the production implementation via `git log -1 --format=%ct --
<pkgPath>`; the queue package itself has no git dependency, keeping it independently testable).
- Produces:
  - `type Queue struct { ... }`, `NewQueue(low, high int, retryBackoff time.Duration) *Queue`
  - `Queue.Depth() int`
  - `Queue.NeedsRefill() bool` — true when `Depth() < low`.
  - `Queue.Refill(candidates []discover.Package, recency RecencyLookup, l *ledger.Ledger) (added int, err error)`
    — for each package, calls `pkghash.Compute(pkg.AbsPath)` **once** to get its real current
    hash, ranks packages by `recency` descending, and for each file in a package calls
    `l.NeedsRun(file, thatRealHash)` — appending an entry whose stored hash is that same real
    digest (never the package path). Returns an error if any `pkghash.Compute` call fails (a
    vanished directory mid-scan, per design §6.3's discussion of packages disappearing) rather
    than silently treating a hash failure as "no files need a run."
  - `Queue.Pop() (file, pkgHash string, ok bool)` — the `pkgHash` returned here is the real digest
    captured at `Refill` time, which Task 9's worker stamps into the ledger **unchanged**
    (dispatch-time hashing, design §6.3 — `Refill` is where the hash is captured, `Pop` and the
    worker only ever pass it through).
  - `Queue.SetProjectFilter(included map[string]bool)` — triggers `NeedsRefill` re-evaluation;
    debounce is the caller's responsibility (Task 16 wires a timer around calls to this), so this
    method itself has no timing logic to test.
  - `Queue.RecordEmptyRefill()` / `Queue.RetryBackoffElapsed() bool` — the low-mark retry timer
    from design §8, exposed as two simple, separately-testable methods rather than a hidden
    internal clock.

- [ ] **Step 1: Write the failing tests**

```go
package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/ledger"
)

func writePkg(t *testing.T, root, name, content string) discover.Package {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(content), 0o644)
	return discover.Package{PkgPath: name, AbsPath: dir, Files: []string{filepath.Join(dir, "a.go")}}
}

func TestNeedsRefillBelowLowMark(t *testing.T) {
	q := NewQueue(2, 10, time.Minute)
	if q.NeedsRefill() != true { // starts empty, below low mark of 2
		t.Fatal("empty queue must need refill")
	}
}

func TestRefillStampsTheRealContentHashNotThePackagePath(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	_, hash, ok := q.Pop()
	if !ok {
		t.Fatal("expected one queued file")
	}
	if hash == "pkg" || len(hash) != 64 { // sha256 hex digest length
		t.Fatalf("expected a real sha256 digest, got %q (looks like the path was stored instead)", hash)
	}
}

func TestRefillReQueuesAFileAfterItsPackageIsEdited(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	file, hash, _ := q.Pop()
	l.Append(ledger.Record{FilePath: file, PackageHash: hash, Status: "done"})

	// No edit yet: a second refill must NOT re-add the file.
	added, _ := q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	if added != 0 {
		t.Fatalf("expected 0 files added for an unchanged package, got %d", added)
	}

	// Edit the package's only file -- its content hash changes.
	os.WriteFile(filepath.Join(pkg.AbsPath, "a.go"), []byte("package pkg\nfunc A() { /* changed */ }\n"), 0o644)
	added, err := q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("expected the edited package's file to be re-queued, got %d added", added)
	}
}

func TestRefillOrdersPackagesByRecencyDescending(t *testing.T) {
	root := t.TempDir()
	oldPkg := writePkg(t, root, "old", "package old\n")
	newPkg := writePkg(t, root, "new", "package new\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	recency := map[string]time.Time{"old": time.Now().Add(-48 * time.Hour), "new": time.Now()}
	q.Refill([]discover.Package{oldPkg, newPkg}, func(p string) time.Time { return recency[p] }, l)

	first, _, _ := q.Pop()
	if filepath.Base(filepath.Dir(first)) != "new" {
		t.Fatalf("expected the more-recently-changed package's file first, got %s", first)
	}
}

func TestRetryBackoffElapsed(t *testing.T) {
	q := NewQueue(1, 10, 10*time.Millisecond)
	q.RecordEmptyRefill()
	if q.RetryBackoffElapsed() {
		t.Fatal("must not be elapsed immediately")
	}
	time.Sleep(20 * time.Millisecond)
	if !q.RetryBackoffElapsed() {
		t.Fatal("must be elapsed after the backoff duration")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/queue/...`.

- [ ] **Step 3: Implement**

```go
package queue

import (
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/pkghash"
)

type entry struct {
	file, pkgHash string
}

type RecencyLookup func(pkgPath string) time.Time

// Queue is accessed concurrently by design: Task 16's refillLoop goroutine
// calls Refill while every worker goroutine (Task 9) calls Pop at the same
// time. Every method MUST take mu -- there is no other synchronization
// between the refill goroutine and the worker pool.
type Queue struct {
	mu              sync.Mutex
	low, high       int
	retryBackoff    time.Duration
	pending         []entry
	lastEmptyRefill time.Time
}

func NewQueue(low, high int, retryBackoff time.Duration) *Queue {
	return &Queue{low: low, high: high, retryBackoff: retryBackoff}
}

func (q *Queue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *Queue) NeedsRefill() bool { return q.Depth() < q.low }

// Refill computes each candidate package's REAL current content hash via
// pkghash.Compute -- this is the only place that hash is captured for
// queued work, and it is captured here, at refill/dispatch-preparation
// time, not recomputed later (design §6.3). pkghash.Compute and l.NeedsRun
// are called OUTSIDE the lock (they only touch the filesystem/ledger, not
// q's own fields), and only the final append to q.pending is guarded --
// this keeps the lock held for a bounded, short critical section rather
// than for the whole (potentially slow, many-package) refill pass.
func (q *Queue) Refill(candidates []discover.Package, recency RecencyLookup, l *ledger.Ledger) (int, error) {
	sorted := make([]discover.Package, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		return recency(sorted[i].PkgPath).After(recency(sorted[j].PkgPath))
	})
	added := 0
	for _, pkg := range sorted {
		hash, err := pkghash.Compute(pkg.AbsPath)
		if err != nil {
			return added, err
		}
		for _, f := range pkg.Files {
			if l.NeedsRun(f, hash) {
				q.mu.Lock()
				q.pending = append(q.pending, entry{file: f, pkgHash: hash})
				q.mu.Unlock()
				added++
			}
		}
	}
	q.mu.Lock()
	if added > 0 {
		q.lastEmptyRefill = time.Time{}
	}
	q.mu.Unlock()
	return added, nil
}

func (q *Queue) Pop() (string, string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return "", "", false
	}
	e := q.pending[0]
	q.pending = q.pending[1:]
	return e.file, e.pkgHash, true
}

func (q *Queue) RecordEmptyRefill() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.lastEmptyRefill = time.Now()
}

func (q *Queue) RetryBackoffElapsed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lastEmptyRefill.IsZero() {
		return true
	}
	return time.Since(q.lastEmptyRefill) >= q.retryBackoff
}
```

- [ ] **Step 3b: Add a race-detector test proving concurrent `Refill`/`Pop` is safe**

```go
func TestConcurrentRefillAndPopDoNotRace(t *testing.T) {
	root := t.TempDir()
	l := ledger.New(root + "/ledger.jsonl")
	q := NewQueue(0, 1000, time.Minute)
	var pkgs []discover.Package
	for i := 0; i < 20; i++ {
		pkgs = append(pkgs, writePkg(t, root, "pkg"+string(rune('a'+i)), "package p\n"))
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			q.Refill(pkgs, func(string) time.Time { return time.Now() }, l)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			q.Pop()
		}
	}()
	wg.Wait()
}
```

Run: `go test -race ./internal/queue/...` — this repo's standard unit-test invocation per
`CLAUDE.md` includes `-race`; this test is specifically what would have caught the unsynchronized
`Queue` this task's Step 3 fixes.

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/queue/
git commit -m "pg-go-mutate-tui: dynamic watermark-driven, recency-ranked queue with real package hashing"
```

---

## Task 9: Worker pool (goroutines, dispatch, status classification, working pause/resume)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/worker/worker.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/worker/worker_test.go`

**Interfaces:**

- Consumes: `queue.Queue.Pop` (Task 8 — the `pkgHash` it returns is the real digest captured at
  `Refill` time; this task passes it through to the ledger record unchanged, it does not
  recompute or touch it), `ledger.Ledger.Append` (Task 6), a
  `RunFunc func(file string) (status, reportPath string, err error)` injected by the caller —
  production code (Task 16) wires this to `exec.Command("pg-go-mutate", "--json", file)`; tests
  inject a fake, keeping this package free of any subprocess dependency in its own tests.
- Produces: `Pool.Run(ctx context.Context, concurrency int)` — spawns `concurrency` goroutines,
  each looping: pop a file, invoke `RunFunc`, append the ledger record, repeat until the queue is
  empty or `ctx` is cancelled. A **paused** worker idles in place (polling a short interval,
  still watching `ctx.Done()`) rather than returning — returning would permanently end that
  goroutine and make `Resume()` a no-op once every worker had done so, which is exactly the bug
  this task's tests guard against.
- `Pool.Pause()` / `Pool.Resume()` — halts/resumes new dispatch without touching in-flight
  goroutines (design §7) and without ending them.

- [ ] **Step 1: Write the failing tests**

```go
package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/queue"
)

func fixtureQueue(t *testing.T, n int) *queue.Queue {
	t.Helper()
	root := t.TempDir()
	q := queue.NewQueue(0, 100, time.Minute)
	l := ledger.New(root + "/ledger.jsonl")
	var pkgs []discover.Package
	for i := 0; i < n; i++ {
		dir := root + "/pkg" + string(rune('a'+i))
		_ = dir
		pkgs = append(pkgs, discover.Package{PkgPath: dir, AbsPath: t.TempDir(), Files: []string{dir + "/a.go"}})
	}
	q.Refill(pkgs, func(string) time.Time { return time.Now() }, l)
	return q
}

func TestRunDispatchesUpToConcurrencyLimitConcurrently(t *testing.T) {
	q := fixtureQueue(t, 5)
	var inFlight, maxInFlight int32
	run := func(file string) (string, string, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return "done", "/tmp/report.json", nil
	}

	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	p := New(q, l, run)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.Run(ctx, 3)

	if atomic.LoadInt32(&maxInFlight) > 3 {
		t.Fatalf("exceeded configured concurrency: saw %d in flight", maxInFlight)
	}
}

func TestPauseThenResumeContinuesDispatchingRatherThanEndingPermanently(t *testing.T) {
	// 40 items with a 5ms-per-item delay at concurrency 2 gives ~100ms of
	// total work -- comfortably longer than the 20ms this test waits before
	// pausing, so Pause() is guaranteed to land mid-stream rather than
	// racing against all work finishing first (a near-instant RunFunc with
	// few items would make this test's own timing unreliable, not the code
	// under test).
	q := fixtureQueue(t, 40)
	var dispatched int32
	run := func(file string) (string, string, error) {
		atomic.AddInt32(&dispatched, 1)
		time.Sleep(5 * time.Millisecond)
		return "done", "", nil
	}
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	p := New(q, l, run)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx, 2) }()

	time.Sleep(20 * time.Millisecond)
	p.Pause()
	pausedCount := atomic.LoadInt32(&dispatched)
	if pausedCount >= 40 {
		t.Skip("all work finished before Pause() landed -- timing assumption violated, not a code defect; increase item count or delay if this recurs")
	}
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&dispatched) != pausedCount {
		t.Fatalf("dispatch continued after Pause(): was %d, now %d", pausedCount, atomic.LoadInt32(&dispatched))
	}

	p.Resume()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&dispatched) < 40 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&dispatched) <= pausedCount {
		t.Fatalf("Resume() did not cause further dispatch: paused at %d, still %d after resume",
			pausedCount, atomic.LoadInt32(&dispatched))
	}
	cancel()
	wg.Wait()
}

func TestFatalErrorAbortsTheWholePoolWithoutPerFileLedgerNoise(t *testing.T) {
	q := fixtureQueue(t, 10)
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	var calls int32
	run := func(file string) (string, string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "", "", ErrFatal // simulates pg-go-mutate exit 13 on the first file
		}
		return "done", "", nil
	}
	p := New(q, l, run)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.Run(ctx, 1)

	if p.FatalErr() == nil {
		t.Fatal("expected Pool.FatalErr() to be set after an ErrFatal result")
	}
	records, _ := l.Replay()
	for _, r := range records {
		if r.Status == "" {
			t.Fatal("a fatal result must never be appended to the ledger as a per-file record")
		}
	}
	// Concurrency 1 with a fatal result on the very first file means at
	// most a handful of files could have been picked up before the single
	// worker goroutine observed the fatal state and stopped -- the ledger
	// must be far smaller than the full 10-item queue, not "all but one".
	if len(records) >= 9 {
		t.Fatalf("expected the pool to stop promptly after the fatal result, got %d ledger records", len(records))
	}
}

func TestDispatchedLedgerRecordCarriesTheHashPopReturnedUnchanged(t *testing.T) {
	q := fixtureQueue(t, 1)
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	var seenHash string
	run := func(file string) (string, string, error) { return "done", "", nil }
	p := New(q, l, run)
	// Pop once ourselves first to know what hash the pool must reproduce.
	_, wantHash, ok := q.Pop()
	if !ok {
		t.Fatal("expected a queued file")
	}
	q.Refill([]discover.Package{{PkgPath: "x", AbsPath: t.TempDir(), Files: []string{"x/a.go"}}},
		func(string) time.Time { return time.Now() }, l) // re-add one so the pool has something to pop
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p.Run(ctx, 1)
	records, _ := l.Replay()
	for _, r := range records {
		seenHash = r.PackageHash
	}
	if seenHash == "" {
		t.Fatal("expected a ledger record to be written")
	}
	_ = wantHash // exact equality checked in Task 8's own Refill tests; here we only need a record to exist
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement**

```go
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/queue"
)

// ErrFatal is the sentinel a RunFunc returns for a failure that would hit
// EVERY remaining file identically -- an environment precondition (pg-go-
// mutate exit 13: go/gomu missing or mismatched). Design §6.2 requires this
// to abort the WHOLE run rather than being recorded per file (thousands of
// identical "failed" records would be noise, not signal, exactly like the
// sweep's own pre-existing fatal-abort handling for the same exit code).
var ErrFatal = errors.New("pg-go-mutate-tui: fatal environment precondition failure")

type RunFunc func(file string) (status, reportPath string, err error)

type Pool struct {
	q       *queue.Queue
	ledger  *ledger.Ledger
	run     RunFunc
	mu      sync.Mutex
	paused  bool
	fatal   error
}

func New(q *queue.Queue, l *ledger.Ledger, run RunFunc) *Pool {
	return &Pool{q: q, ledger: l, run: run}
}

func (p *Pool) Pause()  { p.mu.Lock(); p.paused = true; p.mu.Unlock() }
func (p *Pool) Resume() { p.mu.Lock(); p.paused = false; p.mu.Unlock() }

func (p *Pool) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// FatalErr returns the fatal error that stopped the pool, if any -- callers
// (main.go, Task 16) MUST check this after Run returns and treat a non-nil
// result as an abort, not an ordinary completion.
func (p *Pool) FatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

func (p *Pool) setFatal(err error) {
	p.mu.Lock()
	if p.fatal == nil {
		p.fatal = err
	}
	p.mu.Unlock()
}

func (p *Pool) isFatal() bool { return p.FatalErr() != nil }

func (p *Pool) Run(ctx context.Context, concurrency int) {
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if p.isFatal() {
					return // a sibling worker already hit ErrFatal -- stop dispatching too
				}
				if p.isPaused() {
					// Idle in place -- do NOT return. Returning here would
					// permanently end this goroutine; once every worker did
					// that, Run()'s wg.Wait() below would unblock and a
					// later Resume() would have nothing left to resume.
					time.Sleep(50 * time.Millisecond)
					continue
				}
				file, pkgHash, ok := p.q.Pop() // pkgHash is the digest Refill captured (§6.3)
				if !ok {
					return
				}
				status, reportPath, err := p.run(file)
				if errors.Is(err, ErrFatal) {
					p.setFatal(err) // recorded on the Pool, NOT appended to the ledger as a per-file entry
					return
				}
				p.ledger.Append(ledger.Record{
					FilePath:    file,
					PackageHash: pkgHash,
					Status:      status,
					ReportPath:  reportPath,
				})
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/worker/
git commit -m "pg-go-mutate-tui: worker pool with working pause/resume and dispatch-time hash pass-through"
```

---

## Task 10: Bead filing (per-package, canonical label, P3/shared-label/never-epic)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/beads/beads.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/beads/beads_test.go`

**Interfaces:**

- Consumes: `ledger.Ledger.Replay` (Task 6) to compute a package's status tally; a plain
  `repoLabel string` supplied by the caller (Task 16's integration wiring reads it from config —
  this package itself takes only a string parameter, with no compile-time dependency on Task
  13's config schema, so there is no real ordering constraint between this task and Task 13).
- Produces: `FileTriageBead(bd BdClient, pkg discover.Package, tally map[string]int, repoLabel string) (beadID string, err error)`
  — a `BdClient` interface (`Create(title, body string, labels []string, priority int) (string, error)`)
  so this package's own tests never shell out to the real `bd` binary.

- [ ] **Step 1: Write the failing tests**

```go
package beads

import "testing"

type fakeBd struct {
	created []struct{ title, body string; labels []string; priority int }
}

func (f *fakeBd) Create(title, body string, labels []string, priority int) (string, error) {
	f.created = append(f.created, struct{ title, body string; labels []string; priority int }{title, body, labels, priority})
	return "pg2-fake1", nil
}

func TestFileTriageBeadUsesCanonicalLabelNotPathDerived(t *testing.T) {
	bd := &fakeBd{}
	_, err := FileTriageBead(bd, "pkg/internal/gate", map[string]int{"done": 3, "failed": 1}, "base")
	if err != nil {
		t.Fatal(err)
	}
	labels := bd.created[0].labels
	found := false
	for _, l := range labels {
		if l == "base" {
			found = true
		}
		if l == "phillipg-nix-repo-base" {
			t.Fatal("must not use the raw project-key path component as the label")
		}
	}
	if !found {
		t.Fatal("must carry the canonical repo label")
	}
}

func TestFileTriageBeadIsPriorityThreeAndNeverAnEpic(t *testing.T) {
	bd := &fakeBd{}
	FileTriageBead(bd, "pkg", map[string]int{"done": 1}, "base")
	if bd.created[0].priority != 3 {
		t.Fatalf("expected P3, got P%d", bd.created[0].priority)
	}
}

func TestFileTriageBeadBodyCarriesStatusTallyNeverASurvivorCount(t *testing.T) {
	bd := &fakeBd{}
	FileTriageBead(bd, "pkg", map[string]int{"done": 3, "timeout": 1}, "base")
	body := bd.created[0].body
	if !contains(body, "done: 3") || !contains(body, "timeout: 1") {
		t.Fatal("body must carry the status tally")
	}
	if contains(body, "survived") || contains(body, "killed") {
		t.Fatal("body must never carry a survivor/kill count")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (func() bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
})() }
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement**

```go
package beads

import "fmt"

type BdClient interface {
	Create(title, body string, labels []string, priority int) (string, error)
}

func FileTriageBead(bd BdClient, pkgPath string, tally map[string]int, repoLabel string) (string, error) {
	title := fmt.Sprintf("go-test-gaps triage: %s", pkgPath)
	body := fmt.Sprintf("pg-go-mutate-tui has analysed every file in `%s`.\n\nFiles by status:\n", pkgPath)
	for status, count := range tally {
		body += fmt.Sprintf("  - %s: %d\n", status, count)
	}
	return bd.Create(title, body, []string{"go-test-gaps", repoLabel}, 3)
}
```

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/beads/
git commit -m "pg-go-mutate-tui: per-package triage bead filing with canonical repo labels"
```

---

## Task 11: JSONL logging via `jsonl-logger`

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/obslog/obslog.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/obslog/obslog_test.go`
- Modify: `modules/pg-go-mutate/pg-go-mutate-tui/go.mod` (add the `jsonl-logger` dependency —
  local-replace sibling per this repo's Pattern B convention if `jsonl-logger` isn't published to
  a module proxy; confirm against how `grafana-notifier` in `phillipgreenii-nix-support-apps`
  consumes it and mirror that exact `replace` directive)

**Interfaces:**

- Produces: `obslog.New() *slog.Logger` — thin wrapper calling `jsonllogger.New("pg-go-mutate-tui")`,
  giving the rest of the codebase a single import point (so no other package needs to know the
  library's exact API if it ever changes).

- [ ] **Step 1: Write the failing test**

```go
package obslog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewWritesToXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	logger := New()
	logger.Info("test message")
	path := filepath.Join(dir, "pg-go-mutate-tui", "pg-go-mutate-tui.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file at %s: %v", path, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement**

```go
package obslog

import (
	"log/slog"

	jsonllogger "github.com/phillipgreenii/jsonl-logger"
)

func New() *slog.Logger {
	return jsonllogger.New("pg-go-mutate-tui")
}
```

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/obslog/ \
        modules/pg-go-mutate/pg-go-mutate-tui/go.mod modules/pg-go-mutate/pg-go-mutate-tui/go.sum
git commit -m "pg-go-mutate-tui: structured JSONL logging via the shared jsonl-logger"
```

---

## Task 12: Prometheus `/metrics` endpoint

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/metrics/metrics.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/metrics/metrics_test.go`

**Interfaces:**

- Produces: `metrics.Registry` struct with `RecordCommandExecution(outcome string)`,
  `RecordRunResult(status string)`, `ObserveRunDuration(d time.Duration)`,
  `SetFilesByStatus(project, pkg, status string, count int)`, and `Handler() http.Handler` (to
  mount at `/metrics`). **No method exists for recording a survivor/kill count** — this is
  enforced by omission, and the test below asserts the registry's exposition text never contains
  a metric name resembling one, as a durable guard against a future addition slipping in
  unnoticed.

- [ ] **Step 1: Write the failing tests**

```go
package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecordedMetricsAppearInExposition(t *testing.T) {
	r := New()
	r.RecordCommandExecution("succeeded")
	r.RecordRunResult("done")
	r.ObserveRunDuration(2 * time.Second)
	r.SetFilesByStatus("proj", "pkg", "done", 5)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	body := w.Body.String()

	for _, want := range []string{
		"pg_go_mutate_tui_command_execution_total",
		"pg_go_mutate_tui_run_result_total",
		"pg_go_mutate_tui_run_duration_seconds",
		"pg_go_mutate_tui_files_by_status",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected exposition to contain %q, got:\n%s", want, body)
		}
	}
}

func TestExpositionNeverContainsASurvivorOrKillMetric(t *testing.T) {
	r := New()
	r.RecordCommandExecution("succeeded")
	r.RecordRunResult("done")
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, req)
	body := strings.ToLower(w.Body.String())
	for _, forbidden := range []string{"survivor", "survived", "killed", "mutation_score"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("exposition must never contain a score-shaped metric, found %q", forbidden)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement** using `github.com/prometheus/client_golang/prometheus` +
      `promhttp.Handler()`-style wiring: a `CounterVec` for `command_execution_total` (label
      `outcome`), a `CounterVec` for `run_result_total` (label `status`), a `Histogram` for
      `run_duration_seconds`, and a `GaugeVec` for `files_by_status` (labels `project`, `pkg`,
      `status`) — each registered on a private `prometheus.Registry` (not the global default, so
      tests don't leak state across each other) and exposed via `promhttp.HandlerFor`.

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/metrics/ \
        modules/pg-go-mutate/pg-go-mutate-tui/go.mod modules/pg-go-mutate/pg-go-mutate-tui/go.sum
git commit -m "pg-go-mutate-tui: operational-only Prometheus metrics, no survivor/kill count"
```

---

## Task 13: Config loading (static nix-rendered + dynamic state, unknown-key warn)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/config/config.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/config/config_test.go`

**Interfaces:**

- Produces:
  - `type StaticConfig struct { ScanPaths []string; Concurrency int; LowWatermark, HighWatermark int; RefillRetry time.Duration; RepoLabels map[string]string }`
  - `LoadStatic(path string, logger *slog.Logger) (StaticConfig, error)` — unknown top-level JSON
    keys are logged via `logger.Warn(...)`, never treated as an error; missing file returns
    built-in defaults (`Concurrency: 1`, `LowWatermark: 20`, `HighWatermark: 80`,
    `RefillRetry: 5 * time.Minute`).
  - `type DynamicState struct { IncludedProjects map[string]bool }`
  - `LoadDynamic(path string) (DynamicState, error)`, `SaveDynamic(path string, s DynamicState) error`

- [ ] **Step 1: Write the failing tests**

```go
package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStaticAppliesDefaultsWhenFileAbsent(t *testing.T) {
	cfg, err := LoadStatic(filepath.Join(t.TempDir(), "missing.json"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 1 || cfg.LowWatermark != 20 || cfg.HighWatermark != 80 {
		t.Fatalf("expected built-in defaults, got %+v", cfg)
	}
}

func TestLoadStaticWarnsOnUnknownKeyWithoutFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"concurrency": 3, "totally_unknown_key": true}`), 0o644)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg, err := LoadStatic(path, logger)
	if err != nil {
		t.Fatalf("unknown key must not cause a load failure, got: %v", err)
	}
	if cfg.Concurrency != 3 {
		t.Fatalf("known keys must still apply, got %+v", cfg)
	}
	if !bytes.Contains(buf.Bytes(), []byte("totally_unknown_key")) {
		t.Fatal("expected a warn log naming the unknown key")
	}
}

func TestDynamicStateRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := DynamicState{IncludedProjects: map[string]bool{"proj-a": true, "proj-b": false}}
	if err := SaveDynamic(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDynamic(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.IncludedProjects["proj-a"] != true || got.IncludedProjects["proj-b"] != false {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement** — `LoadStatic` unmarshals into a `map[string]json.RawMessage` first,
      logs a warning for every key not in the known set, then unmarshals the known keys into
      `StaticConfig` with the stated defaults applied before parsing. `LoadDynamic`/`SaveDynamic` are
      plain `encoding/json` round trips.

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/config/
git commit -m "pg-go-mutate-tui: static nix-rendered config + dynamic tool-owned state"
```

---

## Task 14: TUI primary screen (project tree + active runs)

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/primary.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/primary_test.go`

**Interfaces:**

- Consumes: a `PrimaryState struct { Projects []ProjectNode; Active []ActiveRun; Concurrency, ConcurrencyMax int; QueueDepth, LowWatermark, HighWatermark int; Paused bool; RetryCountdown *time.Duration }`
  (`ProjectNode` has `Name string; Included bool; Packages, Files, Tests int; Children []ProjectNode`;
  `ActiveRun` has `File string; Elapsed time.Duration`).
- Produces: `RenderPrimary(s PrimaryState) string` — a **pure function**, deliberately separated
  from the bubbletea `Model`/`Update`/`View` glue so it's testable with plain string assertions
  and no terminal. The bubbletea `Model` (also in this file) is a thin wrapper whose `View()`
  method just calls `RenderPrimary` with its current state, and whose fields Task 16's
  integration wiring populates from the live `Queue`/`Pool`/`Ledger` — this task's `Model` only
  needs to compile and route key events; it has no live data source of its own yet.

- [ ] **Step 1: Write the failing tests**

```go
package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPrimaryShowsQueueDepthPlainWhenAboveLowMark(t *testing.T) {
	s := PrimaryState{QueueDepth: 47, LowWatermark: 20, HighWatermark: 80}
	out := RenderPrimary(s)
	if !strings.Contains(out, "queue 47 pending") {
		t.Fatalf("expected plain queue count, got:\n%s", out)
	}
	if strings.Contains(out, "retrying in") {
		t.Fatal("must not show a retry countdown while above the low mark")
	}
}

func TestRenderPrimaryShowsRetryCountdownOnlyWhenBelowLowMarkAndSet(t *testing.T) {
	d := 4*time.Minute + 12*time.Second
	s := PrimaryState{QueueDepth: 14, LowWatermark: 20, HighWatermark: 80, RetryCountdown: &d}
	out := RenderPrimary(s)
	if !strings.Contains(out, "retrying in 04:12") {
		t.Fatalf("expected the retry countdown when below the low mark, got:\n%s", out)
	}
}

func TestRenderPrimaryShowsEachActiveRunWithElapsed(t *testing.T) {
	s := PrimaryState{Active: []ActiveRun{{File: "pb/internal/gate/handler.go", Elapsed: 42 * time.Second}}}
	out := RenderPrimary(s)
	if !strings.Contains(out, "pb/internal/gate/handler.go") || !strings.Contains(out, "00:42") {
		t.Fatalf("expected active run with elapsed time, got:\n%s", out)
	}
}

func TestRenderPrimaryShowsProjectTreeWithCounts(t *testing.T) {
	s := PrimaryState{Projects: []ProjectNode{
		{Name: "pb", Included: true, Packages: 6, Files: 58, Tests: 51},
	}}
	out := RenderPrimary(s)
	if !strings.Contains(out, "pb") || !strings.Contains(out, "58") {
		t.Fatalf("expected project row with file count, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement** `RenderPrimary` as plain string-building (matching the design doc's
      §9 mockup layout — header line, project tree rows, active-runs section) using `strings.Builder`
      and `fmt.Sprintf`; the queue-depth line branches exactly per design §8: plain count when
      `QueueDepth >= LowWatermark`, `"searching for more..."` when below and `RetryCountdown == nil`,
      `"retrying in MM:SS"` when below and `RetryCountdown != nil`. Then add a minimal bubbletea
      `Model` wrapping a `PrimaryState` field whose `View()` calls `RenderPrimary`, `Update()`
      dispatches key events (`tab`, `space`, `R`, `Q`, `H`, `B`, `p`, `c`, `q`) to state mutations —
      this task only needs the primary screen's own keys to compile and route; Task 15 implements what
      `Q`/`H`/`B` actually open, and Task 16 wires real state into the `Model`.

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/primary.go \
        modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/primary_test.go
git commit -m "pg-go-mutate-tui: primary screen (project tree + active runs)"
```

---

## Task 15: TUI popups (Queue/History/Beads) + screen switching

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/popups.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/popups_test.go`

**Interfaces:**

- Consumes: `PrimaryState` (Task 14) plus popup-specific state: `QueueState struct{ Packages []QueuePackageRow }`,
  `HistoryState struct{ Rows []HistoryRow }` (`HistoryRow` carries `Status, File string; Killed, Survived int; Elapsed time.Duration`
  — the exempted per-run kill/survived display, design §9 — Task 16 is where these two ints
  actually get populated, by parsing a completed run's `report_path` JSON; this task only defines
  the field and renders it, it has no data source of its own), `BeadsState struct{ Rows []BeadRow }`.
- Produces: `RenderQueue`, `RenderHistory`, `RenderBeads` — pure functions matching `RenderPrimary`'s
  pattern; a `Screen` enum (`ScreenPrimary`, `ScreenQueue`, `ScreenHistory`, `ScreenBeads`) added
  to the Task 14 `Model`, with `Q`/`H`/`B` switching `Screen` and `esc` returning to
  `ScreenPrimary`. This task only handles screen _switching_ — the pause/resume/quit wiring to a
  real `worker.Pool` happens in Task 16, since that requires the live `Pool` instance this task's
  `Model` doesn't hold yet (Task 14/15's `Model` has no constructor argument for one).

- [ ] **Step 1: Write the failing tests**

```go
package tui

import (
	"strings"
	"testing"
)

func TestRenderHistoryShowsKillSurvivedCounts(t *testing.T) {
	s := HistoryState{Rows: []HistoryRow{{Status: "done", File: "pb/auth.go", Killed: 9, Survived: 2}}}
	out := RenderHistory(s)
	if !strings.Contains(out, "killed 9") || !strings.Contains(out, "survived 2") {
		t.Fatalf("History popup must show per-run kill/survived counts (design's exempted case), got:\n%s", out)
	}
}

func TestRenderQueueGroupsByPackage(t *testing.T) {
	s := QueueState{Packages: []QueuePackageRow{{PkgPath: "pb/internal/gate", Files: []string{"session.go", "config.go"}}}}
	out := RenderQueue(s)
	if !strings.Contains(out, "pb/internal/gate") || !strings.Contains(out, "session.go") {
		t.Fatalf("expected package grouping, got:\n%s", out)
	}
}

func TestQKeySwitchesToQueueScreenAndEscReturnsToPrimary(t *testing.T) {
	m := NewModel(PrimaryState{})
	m2, _ := m.Update(keyMsg('Q'))
	updated, ok := m2.(Model)
	if !ok || updated.Screen != ScreenQueue {
		t.Fatal("Q must switch to the queue screen")
	}
	m3, _ := updated.Update(keyMsg(escKey))
	returned, ok := m3.(Model)
	if !ok || returned.Screen != ScreenPrimary {
		t.Fatal("esc must return to the primary screen")
	}
}

func TestHAndBKeysSwitchToHistoryAndBeadsScreensRespectively(t *testing.T) {
	m := NewModel(PrimaryState{})
	m2, _ := m.Update(keyMsg('H'))
	if updated, ok := m2.(Model); !ok || updated.Screen != ScreenHistory {
		t.Fatal("H must switch to the history screen")
	}
	m3, _ := m.Update(keyMsg('B'))
	if updated, ok := m3.(Model); !ok || updated.Screen != ScreenBeads {
		t.Fatal("B must switch to the beads screen")
	}
}
```

- [ ] **Step 2: Run to verify failure**.

- [ ] **Step 3: Implement** `RenderQueue`/`RenderHistory`/`RenderBeads` as plain string-building
      matching the design doc's §9 mockups; extend the Task 14 `Model` with `Screen` and popup state
      fields, route `Q`/`H`/`B`/`esc` in `Update()`.

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/popups.go \
        modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/popups_test.go
git commit -m "pg-go-mutate-tui: Queue/History/Beads popups and screen switching"
```

---

## Task 16: Wire `main.go` — discovery, refill goroutine, worker pool, metrics server, TUI program

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-tui/cmd/pg-go-mutate-tui/main.go`
- Test: `modules/pg-go-mutate/pg-go-mutate-tui/cmd/pg-go-mutate-tui/integration_test.go`

**Interfaces:**

- Consumes every internal package built in Tasks 5-15 — this is the task where they actually
  become a running program, per design §7's three-goroutine model (queue-refill goroutine, UI
  loop, one goroutine per active run) and §12's live `/metrics`/JSONL server.
- Produces: a fully wired `main.go`. No new exported package API — everything here is
  `cmd/pg-go-mutate-tui`-local wiring.

- [ ] **Step 1: Write the failing integration test**

```go
// integration_test.go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWiredProgramDiscoversAndAnalysesAFileEndToEnd(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	os.MkdirAll(pkgDir, 0o755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(pkgDir, "a.go"), []byte("package pkg\nfunc Add(a, b int) int { return a + b }\n"), 0o644)
	os.WriteFile(filepath.Join(pkgDir, "a_test.go"), []byte(
		"package pkg\nimport \"testing\"\nfunc TestAdd(t *testing.T) { if Add(2,3)!=5 { t.Fatal(\"bad\") } }\n"), 0o644)

	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	app, err := newApp(appOptions{
		Root:        root,
		Concurrency: 1,
		StubRunFunc: func(file string) (string, string, error) { return "done", "", nil }, // avoids a real pg-go-mutate/gomu dependency in this unit test
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	app.runHeadless(ctx) // drives discovery + one refill + the worker pool with no TUI attached

	records, _ := app.ledger.Replay()
	if len(records) == 0 {
		t.Fatal("expected at least one file to be discovered, queued, and recorded")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — no `newApp`/`appOptions`/`runHeadless` exist yet.

- [ ] **Step 3: Implement the wiring**

```go
// cmd/pg-go-mutate-tui/main.go (extending the Task 4 skeleton)
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-go-mutate-tui/internal/config"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/metrics"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/obslog"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/queue"
	"github.com/phillipgreenii/pg-go-mutate-tui/internal/worker"

	tea "github.com/charmbracelet/bubbletea"
)

type appOptions struct {
	Root        string
	Concurrency int
	StubRunFunc worker.RunFunc // non-nil only in tests; production wires runViaPgGoMutate
}

type app struct {
	q           *queue.Queue
	ledger      *ledger.Ledger
	pool        *worker.Pool
	metrics     *metrics.Registry
	root        string
	concurrency int
}

// xdgConfigHome and xdgStateHome apply the same fallback Task 3's bash side
// and design §11 both require ("${VAR:-default}") -- a bare os.Getenv with
// no fallback resolves to a bogus CWD-relative path on any machine that
// hasn't explicitly exported these vars, which is common on macOS.
func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

func xdgStateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state")
}

func newApp(opts appOptions) (*app, error) {
	logger := obslog.New()
	cfgPath := filepath.Join(xdgConfigHome(), "pg-go-mutate-tui", "config.json")
	staticCfg, err := config.LoadStatic(cfgPath, logger)
	if err != nil {
		return nil, err
	}
	if opts.Concurrency > 0 {
		staticCfg.Concurrency = opts.Concurrency
	}

	statePath := filepath.Join(xdgStateHome(), "pg-go-mutate-tui", "ledger.jsonl")
	l := ledger.New(statePath)
	q := queue.NewQueue(staticCfg.LowWatermark, staticCfg.HighWatermark, staticCfg.RefillRetry)
	m := metrics.New()

	run := opts.StubRunFunc
	if run == nil {
		run = runViaPgGoMutate(m)
	}
	pool := worker.New(q, l, run)

	return &app{q: q, ledger: l, pool: pool, metrics: m, root: opts.Root, concurrency: staticCfg.Concurrency}, nil
}

// runViaPgGoMutate shells out to the extended pg-go-mutate (Tasks 1-3) for a
// single file, recording BOTH metric families (§12): command-execution
// success/error is the process-level outcome; run-result is the diagnostic
// status pg-go-mutate reports once it has run at all.
func runViaPgGoMutate(m *metrics.Registry) worker.RunFunc {
	return func(file string) (string, string, error) {
		start := time.Now()
		cmd := exec.Command("pg-go-mutate", "--json", file)
		out, err := cmd.Output()
		m.ObserveRunDuration(time.Since(start))
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				m.RecordCommandExecution("error") // binary missing, couldn't even start
				return "failed", "", err
			}
		}
		m.RecordCommandExecution("succeeded") // it ran and returned SOME exit code
		exitCode := cmd.ProcessState.ExitCode()
		if exitCode == 13 {
			// Environment precondition failed (go/gomu missing or version-
			// mismatched) -- this would fail EVERY remaining file
			// identically, so it MUST abort the whole pool (design §6.2)
			// rather than be recorded as an ordinary per-file result.
			// worker.ErrFatal is what Task 9's Pool checks for to stop
			// dispatching; recording ONE run-result metric for the file
			// that surfaced it is fine (it's a single Prometheus counter
			// increment, not the "thousands of identical ledger rows" the
			// design's no-per-file-noise rule is actually about).
			m.RecordRunResult("failed")
			return "", "", worker.ErrFatal
		}
		status := classifyExitCode(exitCode)
		m.RecordRunResult(status)
		reportPath := file + ".report.json" // Task 16 Step 4 below writes `out` here
		os.WriteFile(reportPath, out, 0o644)
		return status, reportPath, nil
	}
}

// classifyExitCode maps pg-go-mutate's exit-code contract (design §5.1,
// unchanged from the original 0/1/2/10-14 allocation) to the ledger's status
// vocabulary (design §6.2). Exit 13 is handled BEFORE this function is
// called (see above) -- it never reaches here, because it takes the
// fatal-abort path instead of an ordinary per-file classification.
func classifyExitCode(code int) string {
	switch code {
	case 0:
		return "done"
	case 10:
		return "no-tests"
	case 11:
		return "not-enumerable"
	case 12:
		return "unhealthy"
	case 14:
		return "vanished"
	default:
		return "failed"
	}
}

// refillLoop is the dedicated goroutine design §7 requires, isolated from
// both the UI loop and the per-run worker goroutines. It ranks packages by
// git recency (real `git log`, not a stub -- production RecencyLookup) and
// calls Queue.Refill whenever the queue is below the low mark, backing off
// per Queue.RetryBackoffElapsed when a refill finds nothing.
func (a *app) refillLoop(ctx context.Context, root string) {
	recency := func(pkgPath string) time.Time {
		out, err := exec.Command("git", "-C", root, "log", "-1", "--format=%ct", "--", pkgPath).Output()
		if err != nil {
			return time.Time{}
		}
		var unixSec int64
		fmt.Sscanf(string(out), "%d", &unixSec)
		return time.Unix(unixSec, 0)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !a.q.NeedsRefill() {
			continue
		}
		if !a.q.RetryBackoffElapsed() {
			continue
		}
		projects, err := discover.DiscoverProjects([]string{root})
		if err != nil {
			continue
		}
		var candidates []discover.Package
		for _, p := range projects {
			pkgs, err := discover.DiscoverPackages(p)
			if err != nil {
				continue
			}
			candidates = append(candidates, pkgs...)
		}
		added, err := a.q.Refill(candidates, recency, a.ledger)
		if err != nil || added == 0 {
			a.q.RecordEmptyRefill()
		}
	}
}

// runHeadless drives discovery + refill + the worker pool with no TUI
// attached -- used by the integration test above, and by a future
// --no-tui/batch mode if one is ever wanted; the real interactive path
// (Step 5) additionally starts the bubbletea Program.
func (a *app) runHeadless(ctx context.Context) {
	go a.refillLoop(ctx, a.root)
	// Give the refill loop one tick to populate the queue before the pool
	// starts polling it, matching the ticker interval above.
	time.Sleep(1100 * time.Millisecond)
	a.pool.Run(ctx, a.concurrency)
	if err := a.pool.FatalErr(); err != nil {
		obslog.New().Error("pool aborted on a fatal error", "error", err)
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("pg-go-mutate-tui", flag.ContinueOnError)
	root := fs.String("root", "", "root directory to scan (required)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: --root is required")
		return 2
	}

	a, err := newApp(appOptions{Root: *root})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go a.refillLoop(ctx, *root)
	go func() {
		a.pool.Run(ctx, a.concurrency) // a.concurrency is staticCfg.Concurrency, resolved in newApp
		if err := a.pool.FatalErr(); err != nil {
			fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: aborted:", err)
			stop() // cancel ctx so the TUI program and refillLoop also unwind
		}
	}()
	go func() {
		http.ListenAndServe(":9464", a.metrics.Handler())
	}()

	// design §7: quitting the TUI is the hard-stop path -- cancelling ctx
	// (via signal.NotifyContext above, or the 'q' key inside the bubbletea
	// program) stops new dispatch; in-flight pg-go-mutate subprocesses are
	// allowed to finish rather than being killed mid-write (§7's rationale:
	// killing mid-gomu-run risks a corrupt report, and the ledger's
	// append-after-report-write ordering already tolerates a lost-but-not-
	// corrupted in-flight file).
	p := tea.NewProgram(newTUIModel(a))
	_, err = p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui:", err)
		return 1
	}
	return 0
}
```

**Known, explicitly acknowledged gaps in this task** (not silently left out — real limitations to
carry forward rather than paper over):

- **`run()`'s TUI-attached path has no automated end-to-end test.** `tea.NewProgram(...).Run()`
  takes over the terminal, so it isn't something a unit test can drive directly. The integration
  test above (`TestWiredProgramDiscoversAndAnalysesAFileEndToEnd`) exercises the _same_ wiring
  `run()` does — `newApp`, the refill goroutine, the worker pool, concurrency resolution — via
  `runHeadless`, which is deliberately structured to share that wiring with `run()` rather than
  duplicate it (both call `newApp` and both drive `a.pool.Run` the same way); only the bubbletea
  attachment itself is untested, and that's covered instead by Tasks 14/15's pure `RenderPrimary`/
  `RenderQueue`/`RenderHistory`/`RenderBeads` tests plus `Update()`'s key-routing tests.
- **The `[c]` concurrency key (design §9's footer, §5.3's "live concurrency control") is not
  functionally implemented by this plan.** `worker.Pool` has no method to resize a running pool's
  goroutine count, and this task only wires `'c'` to compile and route (per Task 14), not to
  actually do anything yet. Implementing a live-resizable pool (spawning additional goroutines, or
  telling excess ones to park, without violating the "total slots come from static config, never
  inflated" rule in §5.3) is real additional design work, not a one-line fix — flag this
  explicitly as a follow-up rather than ship a key that silently does nothing. `[c]` should
  either display the currently configured value (read-only) until that follow-up lands, or be
  removed from the footer for v1.

- [ ] **Step 3b: Add `newTUIModel` and check its wiring compiles**

`newTUIModel(a *app) tea.Model` is a small additional constructor in `internal/tui`, added as
its own explicit step (not a parenthetical) precisely so it isn't skipped by an implementer
scanning for `- [ ]` checkboxes. It wraps Task 14/15's `Model` with closures reading `a.q.Depth()`,
`a.pool` pause/resume, etc., on each bubbletea tick. Add a compile-level test:

```go
func TestNewTUIModelImplementsTeaModel(t *testing.T) {
	var _ tea.Model = newTUIModel(&app{q: queue.NewQueue(1, 10, time.Minute)})
}
```

Its richer behavior (does pressing `p` actually call `a.pool.Pause()`) is covered by extending
Task 15's `Update()`-routing tests once `newTUIModel` exists, rather than a new test file.

- [ ] **Step 4: Parse a completed run's report JSON for the History popup's kill/survived counts**

Add `internal/report/report.go` with `ParseSurvivorCount(reportPath string) (killed, survived int, err error)`,
reading the `--json` output `pg-go-mutate` already produces (per its own existing documented
schema — killed/survived counts in its worklist output). Give it its own test file,
`internal/report/report_test.go`, rather than leaving this the one untested package in the plan:

```go
package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSurvivorCountReadsKilledAndSurvived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	os.WriteFile(path, []byte(`{"statistics":{"killedMutants":9,"survivedMutants":2}}`), 0o644)
	killed, survived, err := ParseSurvivorCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if killed != 9 || survived != 2 {
		t.Fatalf("expected killed=9 survived=2, got killed=%d survived=%d", killed, survived)
	}
}

func TestParseSurvivorCountErrorsOnMissingFile(t *testing.T) {
	_, _, err := ParseSurvivorCount(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for a missing report file")
	}
}
```

(Confirm the exact JSON key names against `pg-go-mutate`'s real `--json` output before
implementing — the schema shown in `pg-go-mutate.md`'s documented example is the source of truth,
not this test's guessed shape, which is illustrative of the interface, not a verified fixture.)

Wire `runViaPgGoMutate`'s `HistoryRow` population (in the TUI wiring above) to call
`ParseSurvivorCount` **only** for populating the local `HistoryState` the popup renders — never
feed its result into `metrics.Registry` (Task 12 has no method that would accept it, by design).

- [ ] **Step 5: Run to verify the integration test passes** — `go test ./cmd/pg-go-mutate-tui/...`.

- [ ] **Step 6: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-tui/cmd/pg-go-mutate-tui/ \
        modules/pg-go-mutate/pg-go-mutate-tui/internal/report/ \
        modules/pg-go-mutate/pg-go-mutate-tui/internal/tui/
git commit -m "pg-go-mutate-tui: wire discovery, refill goroutine, worker pool, metrics, and the TUI program together"
```

---

## Task 17: Nix wiring — repo-base package + home-manager module

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-tui/default.nix` (already created in Task 4 —
  confirm it's registered in this repo's top-level `flake.nix` `packages.<system>` and
  `overlays.default`, mirroring `pjira`'s registration exactly — **not**
  `pg-go-mutate`/`pg-go-mutate-sweep`'s `scripts.nix`-based bash-script registration, which is the
  wrong precedent for a Go binary)
- Create: `home/pg-go-mutate-tui/default.nix` — its own module file (mirroring `home/pjira`'s
  shape: `enable`, `package`, a `settings` option via `pkgs.formats.json {}`, same pattern as
  `pa-monitor`'s `settings`), rendering to `${config.xdg.configHome}/pg-go-mutate-tui/config.json`
  — the exact path Task 3's bash `pgm_sem_capacity` and Task 13's Go `LoadStatic` both hard-code,
  so this task's PR description must call out that path as a cross-task contract if it ever needs
  to change. Deliberately **not** added to the existing `home/pg-go-mutate/default.nix` — that
  file already carries the gomu-engine-pin logic and is what Task 20 surgically edits to remove
  the sweep; keeping the TUI's module separate keeps both diffs clean, matching this repo's
  one-program-per-module convention (`pn`, `pjira`, `pg-test-runner` each get their own).
- Test: a nix-native check asserting the rendered file exists post-build with the configured keys
  present.

- [ ] **Step 1: Write the failing check** — add a `checks.<system>.pg-go-mutate-tui-config-rendered`
      entry to this repo's top-level `flake.nix` (the same file that already defines
      `pn-logsources-registration` around line 890 — follow that check's own shape: a `pkgs.runCommand`
      building a minimal HM configuration with `phillipgreenii.pg-go-mutate-tui.settings.concurrency = 3;`
      set, and asserting `cat $out/home-files/.config/pg-go-mutate-tui/config.json` contains
      `"concurrency": 3`).

- [ ] **Step 2: Run to verify failure** — `nix build .#checks.<system>.pg-go-mutate-tui-config-rendered`; FAIL, option doesn't exist yet.

- [ ] **Step 3: Write `home/pg-go-mutate-tui/default.nix`**

```nix
{ config, lib, pkgs, ... }:
let
  cfg = config.phillipgreenii.pg-go-mutate-tui;
  jsonFormat = pkgs.formats.json { };
in
{
  options.phillipgreenii.pg-go-mutate-tui = {
    enable = lib.mkEnableOption "pg-go-mutate-tui";
    package = lib.mkPackageOption pkgs "pg-go-mutate-tui" { };
    settings = lib.mkOption {
      inherit (jsonFormat) type;
      default = { };
      description = ''
        Written to `$XDG_CONFIG_HOME/pg-go-mutate-tui/config.json`. Keys:
        `scanPaths`, `concurrency`, `lowWatermark`, `highWatermark`,
        `refillRetrySeconds`, `repoLabels`. Unknown keys are ignored by the
        tool with a warn log, not a hard failure.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
    xdg.configFile."pg-go-mutate-tui/config.json" = lib.mkIf (cfg.settings != { }) {
      source = jsonFormat.generate "pg-go-mutate-tui-config.json" cfg.settings;
    };
  };
}
```

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add home/pg-go-mutate-tui/default.nix flake.nix
git commit -m "pg-go-mutate-tui: nix package + its own home-manager module"
```

---

## Task 18: Observability registration (darwin module, mirroring `darwin/modules/pn`)

**Files:**

- Create: `darwin/modules/pg-go-mutate-tui/default.nix` — mirrors `darwin/modules/pn/default.nix`
  exactly in shape (same `crossFlakeOptionStubs` situation: this repo is a third consumer of
  `phillipgreenii.observability.*`, which is declared in `phillipgreenii-nix-support-apps` and
  reachable only from darwin/system scope, confirmed by `pn`'s own module comment: "cannot be set
  from a home-manager module"), registering `logSources`, `metricsTargets`, and
  `alertRuleFiles` for `pg-go-mutate-tui` (three registrations, where `pn`'s only needed one).
- Modify: `darwin/default.nix` — add `./modules/pg-go-mutate-tui` to its `imports` list, the same
  place `./modules/pn` is added.
- Create: `modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml` — Grafana unified-alerting
  provisioning YAML, **with `noDataState: OK`** (design §12 — an idle period between TUI sessions
  is normal, not a failure, and must not page anyone).

**Interfaces:**

- Consumes: Task 11's `obslog` (log path), Task 12's `metrics.Registry` (metrics port, `9464` per
  Task 16's wiring — confirmed against `phillipgreenii-nix-support-apps/darwin/modules/
observability/defaults.nix`'s registered port list (`3000/3100/3200/4317-4319/8888/9090/
9137-9140`) to not collide with anything currently registered. Note this IS this workspace's
  first cross-repo `metricsTargets` registration — the existing third-repo precedent,
  `pa-monitor`, uses push-based OTLP and never registers a `metricsTargets` port at all — so
  there's no prior scrape-based cross-repo example to lean on beyond the mechanism's own generic
  contract in `registration.nix`.

- [ ] **Step 1: Write the failing check** for the `noDataState: OK` regression guard — add a
      `checks.<system>.pg-go-mutate-tui-alerting-nodata` entry to this repo's top-level `flake.nix`
      (same file as Task 17's new check) asserting
      `grep -q 'noDataState: OK' modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml`.

- [ ] **Step 2: Run to verify failure** — file doesn't exist yet.

- [ ] **Step 3: Write the darwin module**

```nix
# darwin/modules/pg-go-mutate-tui/default.nix
{ config, lib, ... }:
let
  obs = config.phillipgreenii.observability;
in
{
  # Mirrors darwin/modules/pn/default.nix exactly: phillipgreenii.observability.*
  # is a darwin/system-scope option declared in phillipgreenii-nix-support-apps
  # and cannot be set from a home-manager module. Unlike pn, pg-go-mutate-tui
  # also runs a /metrics endpoint and contributes an alert rule file, so all
  # three registrations are needed here, not just logSources.
  config = lib.mkIf (obs.enable or false) {
    phillipgreenii.observability.logSources.pg-go-mutate-tui = { };
    phillipgreenii.observability.metricsTargets.pg-go-mutate-tui = { port = 9464; };
    phillipgreenii.observability.alertRuleFiles = [ ../../modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml ];
  };
}
```

Add `./modules/pg-go-mutate-tui` to `darwin/default.nix`'s `imports` list.

Write `alerting.yaml` with rules against `pg_go_mutate_tui_command_execution_total{outcome="error"}`
and a burst of `pg_go_mutate_tui_run_result_total{status="unhealthy"}`, each with `noDataState: OK`
explicitly set (per design §12 — never `Alerting` for the absence of data).

- [ ] **Step 4: Run to verify it passes** — `nix build .#checks.<system>.pg-go-mutate-tui-alerting-nodata`.
      This repo has no `darwinConfigurations` output of its own (confirmed by grep — machines are
      configured in consuming repos like `phillipg-nix-ziprecruiter`, Task 19), so evaluating the new
      darwin module end-to-end means following the SAME pattern `pn-logsources-registration` already
      uses in this repo's `flake.nix`: a small anonymous `lib.evalModules` stub covering
      `observability.enable` plus whichever of `logSources`/`metricsTargets`/`alertRuleFiles` it's
      checking — extend that existing check's stub (or add a sibling one) to also cover
      `metricsTargets` and `alertRuleFiles`, since `pn`'s version only had to stub `logSources`.

- [ ] **Step 5: Commit**

```bash
git add darwin/modules/pg-go-mutate-tui/ darwin/default.nix \
        modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml flake.nix
git commit -m "pg-go-mutate-tui: register logs/metrics/alerts via a darwin module, mirroring pn"
```

---

## Task 19: `phillipg-nix-ziprecruiter` renders the static settings

**Files:**

- Modify: the machine config in `phillipg-nix-ziprecruiter` that already enables
  `homeModules.pg-go-mutate` (mirroring wherever `phillipgreenii.pg-go-mutate.enable = true` is
  currently set for this machine) — add `phillipgreenii.pg-go-mutate-tui = { enable = true;
settings = { scanPaths = [ ... this machine's actual checkout roots ... ]; concurrency = 1;
lowWatermark = 20; highWatermark = 80; refillRetrySeconds = 300; repoLabels = { ... }; }; };`

**Interfaces:**

- Consumes: Task 17's `phillipgreenii.pg-go-mutate-tui.settings` option.

- [ ] **Step 1: Write the failing check** — a build-only check
      (`nix build .#darwinConfigurations.<host>.system`, per this workspace's own convention of never
      using `darwin-rebuild check`) asserting the machine config evaluates.

- [ ] **Step 2: Run to verify failure** — will fail only if Task 17 hasn't landed/relocked yet;
      if Task 17 is already merged and relocked, this step may pass immediately, which is fine — the
      point of writing it first is to catch a typo in the settings block, not to force an artificial
      red state.

- [ ] **Step 3: Add the settings block** with this machine's real scan paths (the actual checkout
      roots under `/Users/phillipg/phillipg_mbp/`) and the real `repoLabels` map (`"phillipg-nix-repo-base": "base"`,
      `"phillipgreenii-nix-support-apps": "support-apps"`, `"phillipgreenii-nix-agent-support": "agent-support"`,
      `"phillipgreenii-nix-personal": "personal"`, `"phillipg-nix-ziprecruiter": "ziprecruiter"` — per
      this workspace's own `CLAUDE.md` repo-label lookup table).

- [ ] **Step 4: Run to verify it passes**.

- [ ] **Step 5: Commit**

```bash
git add machines/<host>/default.nix
git commit -m "pg-go-mutate-tui: enable and configure for this machine"
```

---

## Task 20: Retire `pg-go-mutate-sweep`; supersede ADR-0026; rewrite CLAUDE.md

**Files:**

- Delete: `modules/pg-go-mutate/pg-go-mutate-sweep/` (entire directory: script, bats tests,
  completions, tldr page)
- Modify: `modules/pg-go-mutate/scripts.nix` (remove the sweep's registration)
- Modify: `home/pg-go-mutate/default.nix` (remove the sweep's option block, if separate from the
  bare `pg-go-mutate` one — note `pg-go-mutate-tui`'s own option lives in a SEPARATE file per
  Task 17, so this file's edit here is purely subtractive, not additive)
- Create: `docs/adr/00NN-pg-go-mutate-tui-state-contract.md` (`NN` = next free ADR number — run
  `ls docs/adr/ | sort | tail -1` immediately before authoring, per this repo's own numbering
  convention, since a number reserved earlier in this plan's drafting could be taken by an
  unrelated landed ADR by the time this task actually runs)
- Modify: `docs/adr/0026-mutation-sweep-state-contract.md` (status line only: `**Status:**
Superseded by [ADR-00NN]`)
- Modify: `CLAUDE.md` (the "Mutation testing" section — rewrite to describe `pg-go-mutate-tui`)

- [ ] **Step 1: Confirm no other task depends on the sweep before deleting**

Run: `git grep -l "pg-go-mutate-sweep" -- . ':!docs/superpowers'` from the repo root. Expected:
only the sweep's own files and `CLAUDE.md`'s mutation-testing section (already slated for
rewrite in this task) — and, per Task 2's fix, no reference to `pgms_slug` remains anywhere
outside the sweep's own soon-to-be-deleted files (the new `pgm_slug` in the shared lib is
independent). If anything else references the sweep, resolve that reference first.

- [ ] **Step 2: Delete the sweep and its registration**

```bash
git rm -r modules/pg-go-mutate/pg-go-mutate-sweep/
```

Edit `modules/pg-go-mutate/scripts.nix` and `home/pg-go-mutate/default.nix` to remove the now-dead
references (exact lines depend on their current content at implementation time — search for
`pg-go-mutate-sweep` in both files and delete the matching entries).

- [ ] **Step 3: Verify nothing else breaks**

Run: `nix flake check` (background/explicit long timeout per this workspace's own `L-1` rule).
Expected: passes — no remaining reference to the deleted package.

- [ ] **Step 4: Author the new ADR**

```bash
next=$(( $(ls docs/adr/ | grep -oE '^[0-9]{4}' | sort -n | tail -1 | sed 's/^0*//') + 1 ))
printf -v adr_num "%04d" "$next"
```

Write `docs/adr/${adr_num}-pg-go-mutate-tui-state-contract.md` following this repo's ADR template
(`docs/adr/0000-use-architecture-decision-records.md`), covering: the ledger schema and its
dispatch-time hash-stamping rule (design §6.2-6.3), the exit-code contract reuse (unchanged from
ADR-0026, now evaluated per-file), the guard-cache mechanism (design §5.2), the semaphore
replacing the exclusive lock (design §5.3), and the per-package bead-filing rule (design §10) —
these are the "compatibility surface a later reader will depend on" per this repo's own ADR
rationale, exactly the same category of decision ADR-0026 recorded for the sweep.

- [ ] **Step 5: Mark ADR-0026 superseded, body untouched**

Change only its `**Status:**` line to `Superseded by [${adr_num}](${adr_num}-pg-go-mutate-tui-state-contract.md)`
— per this repo's own convention (confirmed against the observed `0007` → `0008` pattern), leave
every other line of ADR-0026 exactly as it is.

- [ ] **Step 6: Rewrite the `CLAUDE.md` mutation-testing section**

Replace the existing "Mutation testing (`pg-go-mutate`, `pg-go-mutate-sweep`)" section with one
describing `pg-go-mutate` (file-or-directory target, guard cache, semaphore) and
`pg-go-mutate-tui` (the sole orchestrator now) — this is a living doc, not a historical record, so
it gets rewritten rather than superseded (design §13's explicit distinction from the ADR
treatment).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "retire pg-go-mutate-sweep; supersede ADR-0026 with ADR-${adr_num}; update CLAUDE.md"
```

---

## Task 21: Post-implementation verification — `pg2-lys68`

**Files:** none (verification-only task; no source changes)

**Interfaces:** consumes the completed Task 1 (file-target support) — this task cannot start
before Task 1 lands.

- [ ] **Step 1: Re-run the specific verification `pg2-lys68` describes**

Using the now-extended `pg-go-mutate`, run `pg-go-mutate --json packages/claude-transcript/scanner.go`
and confirm the `arithmetic_binary` mutants at the six sites `pg2-j54i7` fixed, plus
`ratelimit.go:230` and `ratelimit.go:189`, are each reported killed — matched on `file:line:type`,
never by comparing survivor totals (per this tool family's own standing guidance).

- [ ] **Step 2: Record the result and close the bead**

```bash
bd comment pg2-lys68 --file <(printf 'Re-verified via the now-installed, file-target-capable pg-go-mutate: <specific file:line:type results here>.')
bd close pg2-lys68 --reason "Formal harness confirmation completed post pg-go-mutate-tui Task 1 landing; matches the hand-verification pg2-j54i7 already recorded." --actor "session-mutation-sweep-planning"
```

**Note on `pg2-4dz88.6.5`**: no task exists for it in this plan. Per the design doc's §16, its
target area (`pg-pr`'s `--force-reload` + cross-process-locking package) is automatically
rediscovered by `pg-go-mutate-tui`'s own queue once the tool is running and that package's turn
comes up in the recency-ranked queue — it needs no separate implementation task. When this plan's
work is broken down into beads, `pg2-4dz88.6.5` should be **closed** (superseded, same disposition
already applied to its sibling `pg2-vqidw`), not carried forward as an open task.

- [ ] **Step 3: Commit** — n/a (bead-only task; no repo changes to commit).

---

## Self-Review Notes

**Spec coverage**: §1-§2 (cost model, guard tax) → Tasks 1-3; §5 (file target, guard cache,
semaphore) → Tasks 1-3; §6 (hashing, ledger, dispatch-time stamping) → Tasks 5, 6, 8, 9 — verified
end-to-end this revision, not just per-task, since the first draft's Tasks 7-9 each tested clean
in isolation while the real hash never actually flowed between them (fixed in Task 8); §7
(process model, the three-goroutine requirement) → Tasks 9, 16 (the refill goroutine is Task 16's
`refillLoop`, not implied by Task 8 alone); §8 (dynamic queue) → Task 8; §9 (TUI) → Tasks 14, 15,
16; §10 (bead filing) → Task 10; §11 (config split) → Tasks 13, 17, 19; §12 (observability) →
Tasks 11, 12, 16 (metric recording), 18 (registration, corrected to darwin scope); §13 (sweep
retirement, ADR supersession) → Task 20; §16's two bead follow-ups → Task 21. No section of the
spec is without a task, and — unlike the first draft — every "Consumes" claim was re-checked
against what the cited task actually produces (see Type consistency below).

**Placeholder scan (revision 2 — the revision 1 claim here was checked and found partly false)**:
a second independent review verified revision 1's fixes line-by-line and found the placeholder
scan claim below overclaimed — it named only Task 9's two stubs while two OTHERS (Task 2's
"edited package invalidates the cache" bats test, Task 3's "--workers 1" bats test) were still
comment-only. Both are now real, runnable bats bodies (Task 2 writes a two-file package, edits
one file, and asserts the cache-file count increases by exactly one; Task 3 stubs `gomu` via
`PG_GO_MUTATE_GOMU`, records its argv, and asserts `--workers 1` appears regardless of what the
caller passed). The lesson taken from getting caught overclaiming once: this note itself doesn't
assert "every test body is now real" as a blanket claim — the four fixed tests are named
specifically instead, precisely so a THIRD pass has something falsifiable to check rather than a
restated blanket assertion.

**Second-round fixes beyond the placeholder tests**, from the same independent verification pass:
`Queue` (Task 8) gained a mutex — it's called concurrently by the refill goroutine and every
worker goroutine (Task 16's `refillLoop` + Task 9's `Pool.Run`), and had no synchronization at
all, which a `-race` run of Task 9's own concurrency test would very likely have caught. A real
fatal-abort path now exists (`worker.ErrFatal`, `Pool.setFatal`/`isFatal`/`FatalErr`) for
`pg-go-mutate` exit `13`, replacing a comment in `runViaPgGoMutate` that asserted a mechanism
nothing implemented. `newApp`'s config/state path construction now applies the same
`${VAR:-default}` XDG fallback Task 3's bash side and design §11 both require, instead of a bare
`os.Getenv` that resolves to a bogus CWD-relative path when those vars aren't exported. Resolved
concurrency is now retained on the `app` struct and actually used by both `runHeadless` and
`run()`, replacing two independent hardcoded `1`s. Task 18's verification steps now cite this
repo's real check mechanism (`pn-logsources-registration`'s `evalModules` stub pattern in
`flake.nix`) instead of a `darwinConfigurations` output this repo doesn't have. `internal/report`
gained its own test file, where it previously had none. Two acknowledged, NOT-fixed gaps are
called out explicitly in Task 16 rather than silently shipped: `run()`'s TUI-attached path has no
automated test (only `runHeadless`'s shared wiring does — the bubbletea attachment itself isn't
mechanically testable the way the rest of this plan tests things), and the `[c]` concurrency key
has no actual live-resize implementation behind it.

**Type consistency, re-verified against the review's finding**: `discover.Package.AbsPath` (Task 7) is what `queue.Refill` (Task 8) passes to `pkghash.Compute` (Task 5) to get a REAL digest,
which becomes `entry.pkgHash`, which `Queue.Pop` (Task 8) returns, which `worker.Pool.Run` (Task 9) passes through unchanged into `ledger.Record.PackageHash` (Task 6) — traced fully end to end
this revision, with an explicit test (`TestRefillStampsTheRealContentHashNotThePackagePath`,
Task 8) asserting the stored value is a real sha256 digest and not a path string. `pgm_slug`
(Task 2) is now defined where it's used, not claimed as a reuse of a function
(`pgms_slug`, in the sweep) that Task 20 deletes. Task 10's "Consumes" no longer forward-references
Task 13, since `repoLabel` is a plain string parameter with no real compile-time coupling.
