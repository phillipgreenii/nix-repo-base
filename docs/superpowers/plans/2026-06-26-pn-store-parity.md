# pn store Parity Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore full behavioral parity of `pn store audit` and `pn store deepclean` with the original bash (`pn-store-audit.sh` / `pn-store-deepclean.sh`), fix their broken/withheld output, and wire their CLI flags — replacing the current minimal TODO stubs.

> **User-facing docs:** see [`docs/pn-store.md`](../../pn-store.md) for the config schema, retention (UNION-keep) semantics, command sections, and user journeys this port must preserve. Tracking bead: `pg2-k61y`.

**Architecture:** The `store` package gains focused helper files (`config.go`, `size.go`, `discover.go`, `generations.go`) mirroring the ~20 `pn-lib.bash` helpers, plus rewritten `audit.go`/`deepclean.go` orchestrators. Genuinely-native operations (TOML config parsing, filesystem discovery, symlink/mtime reads, deletes, byte→human size formatting, date math) are implemented in pure Go and tested against temp dirs. Operations with no Go equivalent (`nix-env`, `nix path-info`, `nix-store --gc/--print-dead/--print-roots`, `git worktree list`, `diskutil`, `sudo`) go through the existing `exec.Runner` seam and are tested with `FakeRunner`. All formatted output is written directly to an injected `io.Writer` as each section completes (streaming progress), and the long-running GC subprocess is teed live via `RunOptions{Stdout,Stderr}`.

**Tech Stack:** Go 1.25.9; `github.com/spf13/cobra` (CLI); `github.com/pelletier/go-toml/v2` (config); stdlib `os`/`filepath`/`time`/`regexp`. No new module dependencies → no `gomod2nix.toml` regeneration. Built via `mkGoBinary` (`modules/pn/default.nix`, `src = ./.`), so new files under `internal/store/` need no Nix changes.

## Global Constraints

- Module path: `github.com/phillipgreenii/nix-repo-base/modules/pn`. Package under `modules/pn/internal/store/`.
- **No new Go dependencies.** Use only stdlib + the two existing deps (`cobra`, `go-toml/v2`). A new dep forces `go mod tidy && gomod2nix generate` (ADR 0008) — out of scope; avoid it.
- **Prefer native Go over subprocess** wherever a Go equivalent exists (explicit user directive). Subprocess ONLY for: `nix-env`, `nix path-info`, `nix-store`, `git`, `diskutil`, and the `sudo` prefix. The current stub's `du -sh /nix/store` is REMOVED — store size uses the accurate `df`+`diskutil` pair the bash used (`du` measured the wrong thing).
- All commands accept BOTH an `out io.Writer` (from `cmd.OutOrStdout()`) AND an `errOut io.Writer` (from `cmd.ErrOrStderr()`). `Audit`/`DeepClean` signatures carry both; warnings (e.g. missing search dir) go to `errOut`, report content to `out`. NEVER call `s.runner.Run(..., exec.RunOptions{})` and discard the result for output the user must see.
- The long-running GC step (`sudo nix-store --gc`) MUST stream live: `exec.RunOptions{Stdout: out, Stderr: out}`.
- `diskutil` and `sudo` are NOT in `default.nix` `runtimeDeps`; they resolve from the ambient macOS system PATH (`mkGoBinary` uses `--suffix PATH`, so ambient wins). This matches the bash. They are never invoked in tests (FakeRunner) or CI.
- Per-subcommand `-v/--version` from the bash is intentionally DROPPED — cobra owns versioning via `pn --version` (root.go). `--help` is cobra-generated. Document this; do not re-add per-command version strings.
- Subprocess seam is `exec.Runner`; tests use `exec.NewFakeRunner()` + `AddResponse` (FIFO match on name+args). Filesystem-touching code is tested against `t.TempDir()` with `HOME`/`XDG_CONFIG_HOME`/`TMPDIR` overridden via injected env (see Task 0 seam), never the real home.
- Generation-list dates are the `nix-env` date column only (`YYYY-MM-DD`), parsed with layout `2006-01-02`.
- Section headers are exactly `=== <Title> ===` (matches `section_header`). Category order for deepclean summaries: `system home-manager user-profiles devbox-global devbox-util devbox-projects result-symlinks stale-nix-profiles nh-temp-roots`.
- Verification gate: `go test ./...` green AND `nix flake check` + `darwin-rebuild build --flake .` (build-only; NEVER `switch`) green before claiming complete. `sudo` is NEVER run by the agent; `sudo`-prefixed subprocesses are only ever exercised through `FakeRunner`.

---

## File Structure

- Create `internal/store/env.go` — `Env` struct (Home, XDGConfigHome, TMPDIR) + `RealEnv()`; the testability seam for filesystem roots. `Store` holds an `Env`.
- Create `internal/store/config.go` — `Config{SearchDirs []string; KeepDays int; KeepCount int}` + `LoadConfig(env Env) Config` (reads `<xdg>/pn/store.toml` via go-toml; defaults 14/3/[]).
- Create `internal/store/size.go` — `formatSize(bytes int64) string`; `storeSize(ctx, runner) string`; `profileClosureSize(ctx, runner, profile) string`; `deadPathsSize(ctx, runner) string`; `runtimeRootsSummary(ctx, runner) string`.
- Create `internal/store/discover.go` — profile/symlink/temp-root discovery + `formatProfileLabel` + `isOrphanedStandaloneHMProfile`.
- Create `internal/store/generations.go` — `generation` struct; `listGenerations`; `generationsToPrune` (pure); `pruneGenerations`.
- Modify `internal/store/store.go` — add `Env` field; `New(runner)` keeps `RealEnv()`; add `NewWithEnv(runner, env)` for tests.
- Rewrite `internal/store/audit.go` — `Audit(ctx, out, errOut io.Writer, opts AuditOptions)` producing sectioned output.
- Rewrite `internal/store/deepclean.go` — `DeepClean(ctx, out, errOut io.Writer, opts DeepCleanOptions)` with full pruning + summary.
- Modify `internal/cli/store.go` — wire `--full` (audit); `--dry-run`/`--keep-since`/`--keep` (deepclean); pass `cmd.OutOrStdout()`.
- Test files alongside each: `config_test.go`, `size_test.go`, `discover_test.go`, `generations_test.go`, and extend `store_test.go` for audit/deepclean.

---

## Task 0: Env seam + Store wiring

**Files:**

- Create: `internal/store/env.go`
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go` (extend)

**Interfaces:**

- Produces: `type Env struct { Home, XDGConfigHome, TMPDIR string }`; `func RealEnv() Env`; `func (e Env) configHome() string` (returns `XDGConfigHome` or `Home/.config`); `func (s *Store) Env() Env`; `func NewWithEnv(runner exec.Runner, env Env) *Store`.

- [ ] **Step 1: Write failing test** in `store_test.go`:

```go
func TestEnv_ConfigHomeFallsBackToHome(t *testing.T) {
	e := Env{Home: "/h", XDGConfigHome: ""}
	if got := e.configHome(); got != "/h/.config" {
		t.Fatalf("configHome = %q, want /h/.config", got)
	}
	e2 := Env{Home: "/h", XDGConfigHome: "/x"}
	if got := e2.configHome(); got != "/x" {
		t.Fatalf("configHome = %q, want /x", got)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run TestEnv -v` → FAIL (undefined Env).

- [ ] **Step 3: Implement `env.go`:**

```go
package store

import (
	"os"
	"path/filepath"
)

// Env captures the filesystem roots store commands read from. It is the
// testability seam: production uses RealEnv(); tests inject temp dirs.
type Env struct {
	Home          string // $HOME
	XDGConfigHome string // $XDG_CONFIG_HOME (may be empty)
	TMPDIR        string // $TMPDIR (may be empty → /tmp)
}

// RealEnv reads the ambient environment.
func RealEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), TMPDIR: os.Getenv("TMPDIR")}
}

// configHome returns XDG_CONFIG_HOME or $HOME/.config.
func (e Env) configHome() string {
	if e.XDGConfigHome != "" {
		return e.XDGConfigHome
	}
	return filepath.Join(e.Home, ".config")
}

// tmpDir returns TMPDIR or /tmp.
func (e Env) tmpDir() string {
	if e.TMPDIR != "" {
		return e.TMPDIR
	}
	return "/tmp"
}
```

- [ ] **Step 4: Modify `store.go`** to hold the env:

```go
type Store struct {
	runner exec.Runner
	env    Env
}

// New returns a Store using the given Runner and the real environment.
func New(runner exec.Runner) *Store { return &Store{runner: runner, env: RealEnv()} }

// NewWithEnv returns a Store with an explicit Env (tests).
func NewWithEnv(runner exec.Runner, env Env) *Store { return &Store{runner: runner, env: env} }

func (s *Store) Runner() exec.Runner { return s.runner }
func (s *Store) Env() Env            { return s.env }
```

- [ ] **Step 5: Run** — `go test ./internal/store/ -run TestEnv -v` → PASS. Then `go build ./...` (the stub `Audit`/`DeepClean` bodies are unchanged in this task, so the package still compiles; their SIGNATURES change in Tasks 5/6, at which point the stub-era tests in `store_test.go` are deleted — see B6 note below).

- [ ] **Step 6: Commit** — `git add internal/store/env.go internal/store/store.go internal/store/store_test.go && git commit -m "feat(pn/store): add Env seam for filesystem roots"`

---

## Task 1: Config (store.toml)

**Files:**

- Create: `internal/store/config.go`
- Test: `internal/store/config_test.go`

**Interfaces:**

- Consumes: `Env.configHome()` (Task 0).
- Produces: `type Config struct { SearchDirs []string; KeepDays int; KeepCount int }`; `func LoadConfig(env Env) Config`.

**Behavior (from `pn-lib.bash` + `test-pn-lib.bats`):** path `<configHome>/pn/store.toml`. Missing file → `{nil, 14, 3}`. Missing/`null` key → that field's default (14 / 3). `search_dirs` absent → empty.

- [ ] **Step 1: Write failing tests** in `config_test.go`:

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStoreTOML(t *testing.T, env Env, body string) {
	t.Helper()
	dir := filepath.Join(env.configHome(), "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultsWhenAbsent(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	c := LoadConfig(env)
	if c.KeepDays != 14 || c.KeepCount != 3 || len(c.SearchDirs) != 0 {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestLoadConfig_ReadsValues(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	writeStoreTOML(t, env, "search_dirs = [\"/a\", \"/b\"]\nkeep_days = 7\nkeep_count = 1\n")
	c := LoadConfig(env)
	if c.KeepDays != 7 || c.KeepCount != 1 {
		t.Fatalf("values wrong: %+v", c)
	}
	if len(c.SearchDirs) != 2 || c.SearchDirs[0] != "/a" || c.SearchDirs[1] != "/b" {
		t.Fatalf("search_dirs wrong: %+v", c.SearchDirs)
	}
}

func TestLoadConfig_DefaultsWhenKeyAbsent(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	writeStoreTOML(t, env, "search_dirs = []\n")
	c := LoadConfig(env)
	if c.KeepDays != 14 || c.KeepCount != 3 {
		t.Fatalf("expected key defaults, got %+v", c)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run TestLoadConfig -v` → FAIL (undefined LoadConfig).

- [ ] **Step 3: Implement `config.go`** (pointer fields distinguish "absent" from "zero"):

```go
package store

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config is the parsed ~/.config/pn/store.toml. Defaults: KeepDays 14, KeepCount 3.
type Config struct {
	SearchDirs []string
	KeepDays   int
	KeepCount  int
}

type rawConfig struct {
	SearchDirs []string `toml:"search_dirs"`
	KeepDays   *int     `toml:"keep_days"`
	KeepCount  *int     `toml:"keep_count"`
}

// defaultConfig returns the built-in defaults (14d / 3 / no search dirs).
func defaultConfig() Config { return Config{KeepDays: 14, KeepCount: 3} }

// parseStoreConfig parses store.toml bytes, applying defaults for absent keys.
// Malformed TOML falls back to defaults (best-effort, matching the bash which
// tolerated yq failures). Split from file I/O so it is unit-testable on
// literals — mirrors workspace.ParseConfig([]byte).
func parseStoreConfig(data []byte) Config {
	c := defaultConfig()
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return c
	}
	c.SearchDirs = raw.SearchDirs
	if raw.KeepDays != nil {
		c.KeepDays = *raw.KeepDays
	}
	if raw.KeepCount != nil {
		c.KeepCount = *raw.KeepCount
	}
	return c
}

// LoadConfig reads <configHome>/pn/store.toml and parses it. A missing file
// yields defaults.
func LoadConfig(env Env) Config {
	path := filepath.Join(env.configHome(), "pn", "store.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultConfig()
	}
	return parseStoreConfig(data)
}
```

> Implementer: the Task-1 tests may target `parseStoreConfig([]byte)` directly (FS-free, like `config_test.go`'s `ParseConfig` tests) in addition to the `LoadConfig(env)` file-based tests shown above.

- [ ] **Step 4: Run** — `go test ./internal/store/ -run TestLoadConfig -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/store/config.go internal/store/config_test.go && git commit -m "feat(pn/store): load store.toml config with defaults"`

---

## Task 2: Size & formatting helpers

**Files:**

- Create: `internal/store/size.go`
- Test: `internal/store/size_test.go`

**Interfaces:**

- Consumes: `exec.Runner`.
- Produces: `func formatSize(bytes int64) string`; `func storeSize(ctx context.Context, r exec.Runner) string`; `func profileClosureSize(ctx context.Context, r exec.Runner, profile string) string`; `func deadPathsSize(ctx context.Context, r exec.Runner) string`; `func runtimeRootsSummary(ctx context.Context, r exec.Runner) string`.

**Behavior (from `pn-lib.bash`):**

- `formatSize`: ≥1<<30 → `"%.1f GB"`; ≥1<<20 → `"%.1f MB"`; ≥1<<10 → `"%.1f KB"`; else `"%d B"`.
- `storeSize`: `df /nix/store` → device = field 1 of line 2; `diskutil info <device>` → parse first `(<digits> Bytes)` → `formatSize`. No match → `"0 B"`.
- `profileClosureSize`: resolve symlinks (`filepath.EvalSymlinks`, fallback to input); `nix path-info -S <resolved>` → 2nd whitespace field is bytes; empty/`0`/error → `"unknown"`; else `formatSize`.
- `deadPathsSize`: `sudo nix-store --gc --print-dead` → newline paths; empty → `"0 B"`; else `nix path-info -S <paths...>` summed col 2 → `formatSize`.
- `runtimeRootsSummary`: `nix-store --gc --print-roots` → lines `<root> -> <storepath>` (storepath = last whitespace field); `{lsof}` lines are lsof roots, others file roots; `lsof_only = lsof \ file`; empty → `""`; else `"<n> store path(s) held only by running processes (up to <size> reclaimable)\n  Tip: Restarting applications and re-running may free additional space"` (singular `path` when n==1; size from `nix path-info -S` sum).

- [ ] **Step 1: Write failing tests** in `size_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{54440673280, "50.7 GB"},
	}
	for _, c := range cases {
		if got := formatSize(c.in); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStoreSize_ParsesDiskutilBytes(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte(
		"Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/disk3s7 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk3s7"}, exec.Result{Stdout: []byte(
		"   Volume Used Space:         50.7 GB (54440673280 Bytes) (exactly X 512-Byte-Units)\n")}, nil)
	if got := storeSize(context.Background(), f); got != "50.7 GB" {
		t.Fatalf("storeSize = %q, want 50.7 GB", got)
	}
}

func TestStoreSize_NoVolumeUsedSpace(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte("h\n/dev/disk3s7 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk3s7"}, exec.Result{Stdout: []byte("   Volume Name: Nix Store\n")}, nil)
	if got := storeSize(context.Background(), f); got != "0 B" {
		t.Fatalf("storeSize = %q, want 0 B", got)
	}
}

func TestProfileClosureSize_UnknownOnError(t *testing.T) {
	f := exec.NewFakeRunner() // no response scripted → Run errors
	if got := profileClosureSize(context.Background(), f, "/nope"); got != "unknown" {
		t.Fatalf("profileClosureSize = %q, want unknown", got)
	}
}

func TestRuntimeRootsSummary_LsofOnly(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix-store", []string{"--gc", "--print-roots"}, exec.Result{Stdout: []byte(
		"{lsof} -> /nix/store/aaa-pkg\n/some/file -> /nix/store/bbb-pkg\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/store/aaa-pkg"}, exec.Result{Stdout: []byte(
		"/nix/store/aaa-pkg 1048576\n")}, nil)
	got := runtimeRootsSummary(context.Background(), f)
	if !strings.Contains(got, "held only by running processes") {
		t.Fatalf("runtimeRootsSummary = %q", got)
	}
}
```

> Implementer: `size_test.go` imports `"strings"` and uses `strings.Contains` directly (no helper shims). Write it that way from the start so the red phase shows an _undefined-symbol_ failure for the function under test, not a compile error in the test's own helpers.

- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run 'TestFormatSize|TestStoreSize|TestProfileClosure|TestRuntimeRoots' -v` → FAIL.

- [ ] **Step 3: Implement `size.go`:**

```go
package store

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

var diskutilBytesRE = regexp.MustCompile(`\((\d+) Bytes\)`)

// storeSize reports the /nix/store APFS volume's own used bytes. df resolves the
// block device; diskutil reports the per-volume used space (df alone reports the
// shared container, hence diskutil). macOS-only — returns "0 B" off darwin.
func storeSize(ctx context.Context, r exec.Runner) string {
	dfRes, err := r.Run(ctx, "df", []string{"/nix/store"}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	lines := strings.Split(strings.TrimRight(string(dfRes.Stdout), "\n"), "\n")
	if len(lines) < 2 {
		return formatSize(0)
	}
	fields := strings.Fields(lines[1])
	if len(fields) == 0 {
		return formatSize(0)
	}
	device := fields[0]
	duRes, err := r.Run(ctx, "diskutil", []string{"info", device}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	m := diskutilBytesRE.FindSubmatch(duRes.Stdout)
	if m == nil {
		return formatSize(0)
	}
	bytes, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return formatSize(bytes)
}

// profileClosureSize returns the human-readable closure size, or "unknown".
func profileClosureSize(ctx context.Context, r exec.Runner, profile string) string {
	resolved := profile
	if rp, err := filepath.EvalSymlinks(profile); err == nil {
		resolved = rp
	}
	res, err := r.Run(ctx, "nix", []string{"path-info", "-S", resolved}, exec.RunOptions{})
	if err != nil {
		return "unknown"
	}
	bytes := secondField(res.Stdout)
	if bytes <= 0 {
		return "unknown"
	}
	return formatSize(bytes)
}

func deadPathsSize(ctx context.Context, r exec.Runner) string {
	res, err := r.Run(ctx, "sudo", []string{"nix-store", "--gc", "--print-dead"}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	paths := nonEmptyLines(res.Stdout)
	if len(paths) == 0 {
		return formatSize(0)
	}
	return formatSize(sumPathInfoSizes(ctx, r, paths))
}

func runtimeRootsSummary(ctx context.Context, r exec.Runner) string {
	res, err := r.Run(ctx, "nix-store", []string{"--gc", "--print-roots"}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	var lsof, files []string
	for _, line := range nonEmptyLines(res.Stdout) {
		fields := strings.Fields(line)
		// Store path is awk field $3 of "<root> -> <storepath>".
		// ACCEPTED DEVIATION (NB1): bash `awk '{print $3}'` would emit ""
		// for a <3-field line and still classify it; we skip such lines.
		// Real `nix-store --gc --print-roots` output is always 3 fields, so
		// this never differs in practice; documented to avoid a silent gap.
		if len(fields) < 3 {
			continue
		}
		storePath := fields[2]
		if strings.Contains(line, "{lsof}") {
			lsof = append(lsof, storePath)
		} else {
			files = append(files, storePath)
		}
	}
	lsofOnly := subtractSorted(uniqueSorted(lsof), uniqueSorted(files))
	if len(lsofOnly) == 0 {
		return ""
	}
	size := formatSize(sumPathInfoSizes(ctx, r, lsofOnly))
	word := "paths"
	if len(lsofOnly) == 1 {
		word = "path"
	}
	return fmt.Sprintf("%d store %s held only by running processes (up to %s reclaimable)\n"+
		"  Tip: Restarting applications and re-running may free additional space", len(lsofOnly), word, size)
}

// --- small helpers ---

// secondField parses the 2nd whitespace field of the first line as an int64.
func secondField(b []byte) int64 { return secondFieldFromLine(string(b)) }

func nonEmptyLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// sumPathInfoSizes runs `nix path-info -S <paths...>` once and sums column 2.
func sumPathInfoSizes(ctx context.Context, r exec.Runner, paths []string) int64 {
	res, err := r.Run(ctx, "nix", append([]string{"path-info", "-S"}, paths...), exec.RunOptions{})
	if err != nil {
		return 0
	}
	var sum int64
	for _, line := range nonEmptyLines(res.Stdout) {
		sum += secondFieldFromLine(line)
	}
	return sum
}

func secondFieldFromLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[1], 10, 64)
	return n
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// subtractSorted returns elements of a not present in b.
func subtractSorted(a, b []string) []string {
	bset := map[string]struct{}{}
	for _, s := range b {
		bset[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := bset[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
```

> Implementer: `secondField([]byte)` delegates to `secondFieldFromLine(string)` (one helper, no duplication). `"sort"` is imported above; call `sort.Strings` directly. Argument ordering for `nix path-info -S <paths...>` differs by caller and MUST match the bash: `deadPathsSize` passes paths in `nonEmptyLines` order (UNSORTED — bash pipes `--print-dead` straight to `xargs`); `runtimeRootsSummary` passes `uniqueSorted(lsofOnly)` (SORTED — bash uses `sort -u`). Script the FakeRunner args accordingly (sorted for the runtime-roots test, unsorted for dead-paths).

- [ ] **Step 4: Run** — `go test ./internal/store/ -run 'TestFormatSize|TestStoreSize|TestProfileClosure|TestRuntimeRoots' -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/store/size.go internal/store/size_test.go && git commit -m "feat(pn/store): native size formatting + store/closure/dead/runtime size helpers"`

---

## Task 3: Discovery helpers

**Files:**

- Create: `internal/store/discover.go`
- Test: `internal/store/discover_test.go`

**Interfaces:**

- Consumes: `Env`, `exec.Runner` (only `discoverDevboxProjects` uses the runner, for `git worktree list`).
- Produces:
  - `func (s *Store) systemProfile() string` → `/nix/var/nix/profiles/system`
  - `func (s *Store) homeManagerProfile() string` → `<home>/.local/state/nix/profiles/home-manager`
  - `func (s *Store) userProfiles() []string`
  - `func (s *Store) devboxGlobalProfile() string` (`""` if absent)
  - `func (s *Store) devboxUtilProfile() string` (`""` if absent)
  - `func (s *Store) devboxProjects(ctx context.Context, errOut io.Writer, searchDirs []string) []string` (emits missing-dir warnings to `errOut` — see implementer note in Step 3)
  - `func discoverResultSymlinks(searchDirs []string) []string`
  - `func (s *Store) staleNixProfiles(keepDays int, now time.Time) []string`
  - `func (s *Store) nhTempRoots() []string`
  - `func (s *Store) formatProfileLabel(profile, category string) string`
  - `func (s *Store) isOrphanedStandaloneHMProfile(hmProfile string) bool`
  - `func (s *Store) homeManagerGenLinks() []string` — the orphan-HM branch's generation-link enumerator (NB3). Returns symlinks `<home>/.local/state/nix/profiles/home-manager-*-link` at depth 1 (bash: `find "$hm_dir" -maxdepth 1 -name "${hm_name}-*-link" -type l`). Task 6 uses it for BOTH the gen-link count in the orphan block AND the live-removal loop; without it, Task 6 / `TestDeepClean_RemovesOrphanedStandaloneHM` reference a capability no task provides. Add a unit test: create `home-manager`, `home-manager-195-link`, `home-manager-7-link`, and a non-matching `channels-1-link` → returns exactly the two `home-manager-*-link` symlinks.

**Behavior (from `pn-lib.bash`):**

- `userProfiles`: entries directly under `<home>/.local/state/nix/profiles` (depth 1), excluding name `home-manager` and names matching `-[0-9]+-link$`. Absent dir → empty.
- `devboxGlobal/Util`: fixed paths under `<home>/.local/share/devbox/...`; return path if it exists (`os.Lstat`), else `""`.
- `devboxProjects`: for each search dir (skip non-existent, warn to stderr), walk depth≤4 for `.git` dirs; `git -C <repo> worktree list --porcelain` → add `worktree <path>` lines (skip repo itself); then walk each dir depth≤5 for `*/.devbox/nix/profile/default`; dedup by path.
- `discoverResultSymlinks`: per search dir, walk depth≤3 for symlinks named `result` or `result-*` whose target starts with `/nix/store/`. **Use `os.Readlink` (single-hop, matches bash `readlink "$entry"`), NOT `filepath.EvalSymlinks`** — the targets are real `/nix/store/...` paths that DO NOT exist in test temp dirs (the bats fixtures symlink to nonexistent `/nix/store/fakehash-*`); `EvalSymlinks` would error and silently drop them, breaking parity and the tests.
- `staleNixProfiles`: symlinks directly under `<home>/.nix-profiles`; `keepDays==0` → all; else those whose symlink mtime (via the `lstatModTime` seam) is older than `now - keepDays*24h`. **Accepted deviation from bash:** bash `find -mtime +N` truncates partial days, so it is effectively stricter by up to 24h (selects age ≥ (N+1)×24h). We use a strict `now - keepDays*24h` cutoff. This ≤24h boundary difference is intentional and documented in a code comment; it does not affect real-world use (links are days/weeks past threshold). Do not silently "fix" it without updating this note.
- `nhTempRoots`: under `tmpDir()` depth≤2, symlinks whose **path** matches the glob `*/nh-darwin*/result` (parent-dir prefix `nh-darwin`, basename `result`; e.g. `<tmp>/nh-darwinABCDEF/result`). This is a PATH match, not a basename match — see the `walkSymlinks` note below.
- `formatProfileLabel`: per category (see Global Constraints + bash); `devbox-projects` climbs 4 parents and applies `~` substitution against `<home>`.
- `isOrphanedStandaloneHMProfile`: `hmProfile` is a symlink AND `<home>/.local/state/home-manager/gcroots/current-home` is a symlink.

> **Depth semantics:** the bash uses `find -maxdepth N`. Implement with `filepath.WalkDir` plus a depth guard relative to the search root (count separators), to avoid scanning the whole tree. Match the bash maxdepth exactly (4 for `.git`, 5 for devbox profile, 3 for result, 2 for nh).

- [ ] **Step 1: Write failing tests** in `discover_test.go` (representative — implementer adds one per helper; assertions below are the spec):

```go
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUserProfiles_ExcludesHMAndGenLinks(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".local/state/nix/profiles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"home-manager", "home-manager-195-link", "channels", "channels-3-link"} {
		if err := os.WriteFile(filepath.Join(pdir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewWithEnv(nil, Env{Home: home})
	got := s.userProfiles()
	if len(got) != 1 || filepath.Base(got[0]) != "channels" {
		t.Fatalf("userProfiles = %v, want [.../channels]", got)
	}
}

func TestFormatProfileLabel_DevboxProjectTilde(t *testing.T) {
	home := "/Users/me"
	s := NewWithEnv(nil, Env{Home: home})
	p := home + "/projects/repo-alpha/.devbox/nix/profile/default"
	if got := s.formatProfileLabel(p, "devbox-projects"); got != "~/projects/repo-alpha" {
		t.Fatalf("label = %q, want ~/projects/repo-alpha", got)
	}
	if got := s.formatProfileLabel("/nix/var/nix/profiles/system", "system"); got != "system" {
		t.Fatalf("system label = %q", got)
	}
}

func TestStaleNixProfiles_MtimeThreshold(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".nix-profiles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pdir, "old-env-1-link")
	// Target need not exist; staleNixProfiles only Lstats the link itself.
	if err := os.Symlink("/nix/store/x", link); err != nil {
		t.Fatal(err)
	}
	// Inject the symlink mtime via the seam — do NOT use os.Chtimes (it follows
	// the symlink and would ENOENT on the missing target), and a freshly-created
	// link's real mtime is ~now (would fail the 14d assertion). The seam is the
	// ONLY mtime source in staleNixProfiles, so this fully controls the test.
	old := time.Now().Add(-30 * 24 * time.Hour)
	orig := lstatModTime
	lstatModTime = func(string) (time.Time, error) { return old, nil }
	defer func() { lstatModTime = orig }()

	s := NewWithEnv(nil, Env{Home: home})
	now := time.Now()
	if got := s.staleNixProfiles(14, now); len(got) != 1 {
		t.Fatalf("expected 1 stale at 14d, got %v", got)
	}
	if got := s.staleNixProfiles(60, now); len(got) != 0 {
		t.Fatalf("expected 0 stale at 60d, got %v", got)
	}
	if got := s.staleNixProfiles(0, now); len(got) != 1 {
		t.Fatalf("keepDays=0 should mark all stale, got %v", got)
	}
}

func TestIsOrphanedStandaloneHM(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".local/state/nix/profiles")
	gcroots := filepath.Join(home, ".local/state/home-manager/gcroots")
	os.MkdirAll(pdir, 0o755)
	os.MkdirAll(gcroots, 0o755)
	hm := filepath.Join(pdir, "home-manager")
	os.Symlink("/nix/store/standalone", hm)
	s := NewWithEnv(nil, Env{Home: home})
	if s.isOrphanedStandaloneHMProfile(hm) {
		t.Fatal("not orphaned without current-home")
	}
	os.Symlink("/nix/store/darwin", filepath.Join(gcroots, "current-home"))
	if !s.isOrphanedStandaloneHMProfile(hm) {
		t.Fatal("orphaned when both exist")
	}
}
```

> **Implementer note (mtime on symlinks):** `os.Chtimes` follows the symlink; to set the _link's_ own mtime (as the bash `touch -h` does, and as `staleNixProfiles` reads via `os.Lstat`), use `golang.org/x/sys/unix.Lutimes` — BUT that is a new dep (forbidden). Instead, in the test create the symlink and then move the clock by reading with `os.Lstat`; if the platform's `os.Lstat().ModTime()` on a freshly-created symlink is "now", make the threshold test tolerant, OR set the link mtime via a tiny `syscall` call (`syscall.Lutimes` is not in stdlib on all platforms). RESOLUTION: implement `linkModTime(path)` using `os.Lstat` and set test fixtures' mtime via the `touch -h` shell out **in the test only** is disallowed (subprocess). Final approach: add a package-level `var lstatModTime = func(path string) (time.Time, error)` seam so the test injects controlled mtimes without touching the FS clock. Document in code.

- [ ] **Step 2: Run, verify fail** → undefined symbols.

- [ ] **Step 3: Implement `discover.go`** per the Behavior spec. Key skeletons:

```go
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func (s *Store) systemProfile() string { return "/nix/var/nix/profiles/system" }

func (s *Store) homeManagerProfile() string {
	return filepath.Join(s.env.Home, ".local/state/nix/profiles/home-manager")
}

var genLinkRE = regexp.MustCompile(`-[0-9]+-link$`)

func (s *Store) userProfiles() []string {
	dir := filepath.Join(s.env.Home, ".local/state/nix/profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if name == "home-manager" || genLinkRE.MatchString(name) {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out
}

// lstatModTime is the seam for symlink mtime (overridden in tests).
var lstatModTime = func(path string) (time.Time, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (s *Store) staleNixProfiles(keepDays int, now time.Time) []string {
	dir := filepath.Join(s.env.Home, ".nix-profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if keepDays == 0 {
			out = append(out, p)
			continue
		}
		mt, err := lstatModTime(p)
		if err != nil {
			continue
		}
		if mt.Before(cutoff) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) formatProfileLabel(profile, category string) string {
	switch category {
	case "system", "home-manager", "devbox-global", "devbox-util":
		return category
	case "user-profiles":
		return filepath.Base(profile)
	case "devbox-projects":
		projDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(profile))))
		if s.env.Home != "" && strings.HasPrefix(projDir, s.env.Home) {
			return "~" + strings.TrimPrefix(projDir, s.env.Home)
		}
		return projDir
	default:
		return profile
	}
}

func (s *Store) isOrphanedStandaloneHMProfile(hmProfile string) bool {
	if fi, err := os.Lstat(hmProfile); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	current := filepath.Join(s.env.Home, ".local/state/home-manager/gcroots/current-home")
	fi, err := os.Lstat(current)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// walkSymlinks returns symlinks under root up to maxDepth whose RELATIVE PATH
// (from root) satisfies pathMatch, and (when targetPrefix != "") whose
// os.Readlink target has that prefix. pathMatch receives the path relative to
// root so callers can express both basename rules (result / result-*) AND
// path-component rules (*/nh-darwin*/result). Targets are read with os.Readlink
// (single-hop), never EvalSymlinks, so dangling /nix/store targets still match.
func walkSymlinks(root string, maxDepth int, pathMatch func(rel string) bool, targetPrefix string) []string { /* depth-guarded WalkDir; os.Lstat for symlink mode; os.Readlink for target */ return nil }
// result-symlink call: pathMatch on basename ∈ {result, result-*}, targetPrefix "/nix/store/".
// nh-temp-root call: pathMatch on rel path glob "*/nh-darwin*/result" (use a small matcher on
// path.Base(dir) hasPrefix "nh-darwin" && base=="result"), no targetPrefix (bash only checks -L).

// devboxProjects, devboxGlobalProfile, devboxUtilProfile, discoverResultSymlinks,
// nhTempRoots, and walkSymlinks implemented per Behavior spec above.
```

> Implementer: `devboxProjects` shells `git` via the runner exactly as `pn-lib.bash` does: `git -C <repo> worktree list --porcelain`, parse lines beginning `worktree `. **Signature:** `func (s *Store) devboxProjects(ctx context.Context, errOut io.Writer, searchDirs []string) []string` — emit `WARNING: search dir does not exist: <dir>` to `errOut` (matching the bash stderr warning), since both `Audit` and `DeepClean` now thread `errOut` (Tasks 5/6). Add a test asserting the warning lands on `errOut`, not `out`.

- [ ] **Step 4: Run** — `go test ./internal/store/ -run 'TestUserProfiles|TestFormatProfileLabel|TestStaleNixProfiles|TestIsOrphaned|TestDevbox|TestResultSymlinks|TestNHTempRoots' -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/store/discover.go internal/store/discover_test.go && git commit -m "feat(pn/store): profile/symlink/temp-root discovery helpers"`

---

## Task 4: Generations

**Files:**

- Create: `internal/store/generations.go`
- Test: `internal/store/generations_test.go`

**Interfaces:**

- Consumes: `exec.Runner`.
- Produces: `type generation struct { Num int; Date string; Current bool }`; `func listGenerations(ctx, r, profile string, sudo bool) ([]generation, error)`; `func generationsToPrune(gens []generation, keepDays, keepCount int, now time.Time) []int`; `func pruneGenerations(ctx, r, profile string, nums []int, sudo bool) error`.

**Behavior (from `pn-lib.bash`):**

- `listGenerations`: `[sudo] nix-env --profile <p> --list-generations`; each line: field 1 = gen number, field 2 = date (`YYYY-MM-DD`), `Current` if any field contains `(current)`.
- `generationsToPrune` (UNION keep; pure, the most test-critical):
  - never prune `Current`.
  - if `keepCount > 0`: protect the last `keepCount` entries (by slice position, matching the bash `start = total - keepCount`).
  - if `keepDays > 0`: protect entries whose date ≥ `now - keepDays*24h` (parse `2006-01-02`; unparseable date → treat as NOT protected by time, matching bash where a failed parse yields ts that won't satisfy `>= cutoff` only if it errors — implementer: on parse error, do not time-protect).
  - emit remaining numbers in input order.
- `pruneGenerations`: if `nums` empty → no-op; else `[sudo] nix-env --profile <p> --delete-generations <nums...>`.

- [ ] **Step 1: Write failing tests** in `generations_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func gens() []generation {
	return []generation{
		{Num: 1, Date: "2024-01-01", Current: false}, // ancient
		{Num: 2, Date: "2099-01-01", Current: true},  // current
	}
}

func TestGenerationsToPrune_CountProtectsAll(t *testing.T) {
	// keepCount=3 over 2 gens → top 3 protects both → nothing pruned
	got := generationsToPrune(gens(), 14, 3, time.Now())
	if len(got) != 0 {
		t.Fatalf("expected none pruned, got %v", got)
	}
}

func TestGenerationsToPrune_AggressivePrunesGen1(t *testing.T) {
	// keepDays=0 (time off), keepCount=1 → only current protected → gen1 pruned
	got := generationsToPrune(gens(), 0, 1, time.Now())
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1], got %v", got)
	}
}

func TestGenerationsToPrune_KeepCountZeroDisablesCount(t *testing.T) {
	got := generationsToPrune(gens(), 0, 0, time.Now())
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected [1], got %v", got)
	}
}

func TestGenerationsToPrune_TimeProtects(t *testing.T) {
	g := []generation{
		{Num: 1, Date: time.Now().Add(-3 * 24 * time.Hour).Format("2006-01-02"), Current: false},
		{Num: 2, Date: time.Now().Format("2006-01-02"), Current: true},
	}
	// keepDays=7 protects gen1 (3d old); keepCount=0 → gen1 kept by time
	if got := generationsToPrune(g, 7, 0, time.Now()); len(got) != 0 {
		t.Fatalf("expected none (time-protected), got %v", got)
	}
}

func TestGenerationsToPrune_TimeBoundaryLocal(t *testing.T) {
	// DETERMINISTIC now at local noon (never use time.Now() here — flaky at
	// midnight). bash's cutoff carries the current time-of-day:
	//   cutoff = now - keepDays*24h = midnight(today-keepDays) + 12h.
	// A gen dated EXACTLY keepDays ago is at LOCAL midnight(today-keepDays),
	// which is < cutoff → PRUNED (matches bash + the impl). A gen one day newer
	// is > cutoff → protected. This guards the ParseInLocation(..., Local) fix:
	// a UTC parse would shift the gen timestamp by the TZ offset and flip the
	// boundary result.
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.Local)
	cur := generation{Num: 2, Date: now.Format("2006-01-02"), Current: true}

	boundary := []generation{{Num: 1, Date: now.AddDate(0, 0, -7).Format("2006-01-02")}, cur}
	if got := generationsToPrune(boundary, 7, 0, now); len(got) != 1 || got[0] != 1 {
		t.Fatalf("gen at exactly keepDays boundary should be pruned (matches bash), got %v", got)
	}
	inside := []generation{{Num: 1, Date: now.AddDate(0, 0, -6).Format("2006-01-02")}, cur}
	if got := generationsToPrune(inside, 7, 0, now); len(got) != 0 {
		t.Fatalf("gen within keepDays window should be protected, got %v", got)
	}
}

func TestListGenerations_ParsesCurrent(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix-env", []string{"--profile", "/p", "--list-generations"}, exec.Result{Stdout: []byte(
		"   1   2024-01-01 12:00:00\n   2   2099-01-01 12:00:00   (current)\n")}, nil)
	g, err := listGenerations(context.Background(), f, "/p", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 2 || g[0].Num != 1 || g[0].Date != "2024-01-01" || !g[1].Current {
		t.Fatalf("parsed wrong: %+v", g)
	}
}

func TestPruneGenerations_Sudo(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sudo", []string{"nix-env", "--profile", "/p", "--delete-generations", "1", "3"}, exec.Result{}, nil)
	if err := pruneGenerations(context.Background(), f, "/p", []int{1, 3}, true); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `generations.go`:**

```go
package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

type generation struct {
	Num     int
	Date    string
	Current bool
}

func sudoArgs(sudo bool, name string, args ...string) (string, []string) {
	if sudo {
		return "sudo", append([]string{name}, args...)
	}
	return name, args
}

func listGenerations(ctx context.Context, r exec.Runner, profile string, sudo bool) ([]generation, error) {
	name, args := sudoArgs(sudo, "nix-env", "--profile", profile, "--list-generations")
	res, err := r.Run(ctx, name, args, exec.RunOptions{})
	if err != nil {
		return nil, err
	}
	var out []generation
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		num, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		current := strings.Contains(line, "(current)")
		out = append(out, generation{Num: num, Date: fields[1], Current: current})
	}
	return out, nil
}

func generationsToPrune(gens []generation, keepDays, keepCount int, now time.Time) []int {
	total := len(gens)
	if total == 0 {
		return nil
	}
	countProtected := map[int]bool{}
	if keepCount > 0 {
		start := total - keepCount
		if start < 0 {
			start = 0
		}
		for i := start; i < total; i++ {
			countProtected[gens[i].Num] = true
		}
	}
	var cutoff time.Time
	if keepDays > 0 {
		cutoff = now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	}
	var del []int
	for _, g := range gens {
		if g.Current || countProtected[g.Num] {
			continue
		}
		if keepDays > 0 {
			// ParseInLocation(..., time.Local) → LOCAL midnight, matching the
			// bash `date -d "$gdate"` (local). Plain time.Parse yields UTC
			// midnight and would skew the boundary by the TZ offset. On parse
			// error, do NOT time-protect (matches bash falling through).
			if t, err := time.ParseInLocation("2006-01-02", g.Date, time.Local); err == nil {
				if !t.Before(cutoff) { // t >= cutoff → time-protected
					continue
				}
			}
		}
		del = append(del, g.Num)
	}
	return del
}

func pruneGenerations(ctx context.Context, r exec.Runner, profile string, nums []int, sudo bool) error {
	if len(nums) == 0 {
		return nil
	}
	strs := make([]string, len(nums))
	for i, n := range nums {
		strs[i] = strconv.Itoa(n)
	}
	name, args := sudoArgs(sudo, "nix-env", "--profile", profile, "--delete-generations")
	args = append(args, strs...)
	_, err := r.Run(ctx, name, args, exec.RunOptions{})
	return err
}
```

- [ ] **Step 4: Run** — `go test ./internal/store/ -run 'TestGenerations|TestListGenerations|TestPruneGenerations' -v` → PASS.

- [ ] **Step 5: Commit** — `git add internal/store/generations.go internal/store/generations_test.go && git commit -m "feat(pn/store): generation listing + union-keep prune logic"`

---

## Task 5: Audit orchestrator

**Files:**

- Modify: `internal/store/audit.go` (full rewrite)
- Modify: `internal/cli/store.go` (audit only — done fully in Task 7; here keep compile via temporary writer)
- Test: extend `internal/store/store_test.go`

**Interfaces:**

- New signature: `func (s *Store) Audit(ctx context.Context, out, errOut io.Writer, opts AuditOptions) error`. `AuditOptions{ Full bool }` (unchanged). `errOut` receives `devboxProjects` missing-search-dir warnings.
- Consumes: Tasks 0–4 helpers.

**B6 — remove stub-era tests first:** Before writing new tests, DELETE the six stub tests in `store_test.go` that assert the removed contracts (`TestAudit_RunsStoreSizeOnly`, `TestAudit_FullAddsDeadPathsEstimate`, `TestAudit_PropagatesStoreSizeError`, `TestDeepClean_DryRunUsesEstimateAndSkipsGC`, `TestDeepClean_RunsSudoGC`, `TestDeepClean_PropagatesGCError`). They reference `du -sh`/`nix store gc --dry-run` and the old single-`out`-less signature; leaving them in breaks compilation at Step 5. Keep `TestNew_ExposesRunner` (still valid) and the Task-0 `TestEnv_*`.

**Behavior (from `pn-store-audit.sh`):** emit, in order, sections `System Profiles`, `Home Manager`, `User Profiles`, `Devbox Global`, `Devbox Projects`, `Nix Store`. **NOTE:** audit has NO `Devbox Util` section — that section exists ONLY in deepclean (Task 6). Do not add it to audit; the bats audit spec confirms its absence. `devboxProjects` missing-dir warnings go to `errOut`. Per-profile block:

```
  <label>:
    Profile: <profile>
    <each list_generations line, indented 4 spaces>
    Closure size: <profileClosureSize>
<blank line>
```

System uses `sudo` for `listGenerations`. Home Manager prints `(not installed)` when the profile path is absent. Devbox Global prints `(not installed)` when absent. Devbox Projects prints `(no search dirs configured)` when none. Nix Store: `Volume used: <storeSize>`; if `Full`: also `Reclaimable (dead paths): <deadPathsSize>`. Write everything to `out` as produced (progress visibility). Errors from individual subprocesses are tolerated (mirror bash: a profile that errors still prints its header; closure size becomes `unknown`), so `Audit` returns `nil` except on a programming error.

- [ ] **Step 1: Write failing test** (section presence + `--full` gating, mirroring the bats):

```go
func TestAudit_EmitsSectionsAndStoreSize(t *testing.T) {
	home := t.TempDir()
	f := exec.NewFakeRunner()
	// store size
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte("h\n/dev/disk1 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk1"}, exec.Result{Stdout: []byte("Volume Used Space: 12.0 GB (12884901888 Bytes)\n")}, nil)
	// system profile generations (sudo) + closure
	f.AddResponse("sudo", []string{"nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"}, exec.Result{Stdout: []byte("1 2024-01-01 12:00:00 (current)\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"}, exec.Result{Stdout: []byte("/nix/var/nix/profiles/system 1048576\n")}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"=== System Profiles ===", "=== Home Manager ===", "=== Nix Store ===", "Volume used: 12.0 GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Reclaimable") {
		t.Error("non-full audit must not show Reclaimable")
	}
}

func TestAudit_FullShowsReclaimable(t *testing.T) {
	// as above + sudo nix-store --gc --print-dead returning a path, + nix path-info sum,
	// assert strings.Contains(out, "Reclaimable (dead paths):")
}
```

> Implementer: `EvalSymlinks` on `/nix/var/nix/profiles/system` will fail in tests (path absent) → falls back to the raw path, so script `nix path-info -S` with the raw profile path as above.

- [ ] **Step 2: Run, verify fail** — signature mismatch / missing assertions.

- [ ] **Step 3: Implement `audit.go`** writing to `out` (use a private `auditProfile(ctx, out, label, profile, sudo)` helper that prints the block). **NB4 — error tolerance contract:** `auditProfile` MUST call `listGenerations` and, on error, emit ZERO generation lines and continue (closure size separately becomes `unknown` on its own error). `Audit` MUST NOT propagate a per-profile/subprocess error — it returns `nil` except on a true programming error. This mirrors the bash, which never aborts the audit when a profile's `nix-env` fails. Keep the package-doc and `AuditOptions` doc comments.

- [ ] **Step 3b: Add the negative test** `TestAudit_ToleratesProfileError`: script the system `sudo nix-env --list-generations` to return an error and `nix path-info -S` to error too; assert `Audit` returns `nil`, output still contains `Profile: /nix/var/nix/profiles/system` and `Closure size: unknown`, and the run continues to `=== Nix Store ===`.

- [ ] **Step 4: Update `cli/store.go`** minimally so the package compiles: `return store.New(exec.NewRealRunner()).Audit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store.AuditOptions{Full: full})` (full flag wired fully in Task 7; add it here).

- [ ] **Step 5: Run** — `go test ./internal/store/ -run TestAudit -v` → PASS; `go build ./...` → ok.

- [ ] **Step 6: Commit** — `git add -A && git commit -m "feat(pn/store): audit emits full sectioned report to writer"`

---

## Task 6: DeepClean orchestrator

**Files:**

- Modify: `internal/store/deepclean.go` (full rewrite)
- Test: extend `internal/store/store_test.go`

**Interfaces:**

- New signature: `func (s *Store) DeepClean(ctx context.Context, out, errOut io.Writer, opts DeepCleanOptions) error`. `DeepCleanOptions{ DryRun bool; KeepSince string; Keep int }` (unchanged; `Keep` < 0 = use config default).
- Consumes: Tasks 0–4 helpers + `LoadConfig`.

**Behavior (from `pn-store-deepclean.sh`):**

1. Resolve `keepDays`: parse `KeepSince` (`<N>d` or `<N>w`→×7; empty → `config.KeepDays`; invalid → error).
2. Resolve `keepCount`: `Keep >= 0` → `Keep`; else `config.KeepCount`.
3. Capture `storeBefore = storeSize()` (live runs only need it for summary).
4. Per-section processing (each prints `=== <Title> ===`):
   - **System Profiles**: `processProfile("system", systemProfile(), "system", sudo=true)`.
   - **Home Manager**: if profile exists AND `isOrphanedStandaloneHMProfile` → print orphan block, count gen links (`<dir>/<name>-*-link` symlinks), and in live mode remove the profile symlink + gen links; else `processProfile(...)`.
   - **User Profiles**: each from `userProfiles()`.
   - **Devbox Global / Util**: `processProfile` or `(not installed)`.
   - **Devbox Projects**: from `devboxProjects(searchDirs)`, or `(no search dirs configured)`.
   - **Result Symlinks**: list + (live) remove; count.
   - **Stale Nix Profiles**: list (basename) + (live) remove; count.
   - **NH Temp Roots**: list + (live) remove; count.
5. **Summary**:
   - dry-run: `DRY RUN — no changes made`, then `Would prune:` with each category count (fixed order), then `Reclaimable estimate (dead paths):` + `deadPathsSize()`.
   - live: `Store before: <before>`, run **`sudo nix-store --gc` streamed live** (`RunOptions{Stdout: out, Stderr: out}`), `Store after: <storeSize()>`, blank, `Pruned generations:` with counts, blank, `=== Runtime Roots ===` + `runtimeRootsSummary()`.

`processProfile(out, label, profile, category, sudo)`: if profile missing → return (count 0). `nums = generationsToPrune(listGenerations(...), keepDays, keepCount, now)`. Add `len(nums)` to `prunedCounts[category]`. If empty → `  <label>: nothing to prune`. Else `  <label>: N generation(s) to prune (n1 n2 ...)` and, when not dry-run, `pruneGenerations(...)`.

- [ ] **Step 1: Write failing tests** (mirror the deepclean bats — behaviors, not exact strings where brittle):

```go
func TestDeepClean_DryRunNoDeletesNoGC(t *testing.T) {
	env, f := deepcleanFixture(t) // helper: home with 2 gens via fake, search_dirs empty
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{DryRun: true, KeepSince: "0d", Keep: 0}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls() {
		if c.Name == "sudo" && len(c.Args) >= 2 && c.Args[0] == "nix-store" && c.Args[1] == "--gc" {
			t.Error("dry-run must not GC")
		}
		for _, a := range c.Args {
			if a == "--delete-generations" {
				t.Error("dry-run must not delete generations")
			}
		}
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Error("missing DRY RUN banner")
	}
}

func TestDeepClean_LiveRunsSudoGC(t *testing.T) {
	// keepSince 0d, keep 1 → gen1 pruned; assert a sudo nix-store --gc call
	// recorded AND that the --gc Call.Opts.Stdout != nil (streamed live).
}

func TestDeepClean_RemovesResultSymlinks(t *testing.T) {
	// create search_dirs with a project containing result/result-1 → /nix/store symlinks;
	// live run removes them; dry-run leaves them
}

func TestDeepClean_RemovesOrphanedStandaloneHM(t *testing.T) {
	// Mirror the bats _setup_orphaned_hm: under HOME create
	//   .local/state/nix/profiles/home-manager → home-manager-195-link → <store>
	//   .local/state/nix/profiles/home-manager-195-link (symlink)
	//   .local/state/home-manager/gcroots/current-home (symlink)
	// Live run (keepSince 0d, keep 1): output contains "orphaned standalone profile";
	// home-manager AND home-manager-195-link symlinks are removed. Dry-run: message
	// printed, both links still present.
}

func TestDeepClean_InvalidKeepSince(t *testing.T) {
	_, f := deepcleanFixture(t)
	s := NewWithEnv(f, Env{Home: t.TempDir()})
	if err := s.DeepClean(context.Background(), io.Discard, io.Discard, DeepCleanOptions{KeepSince: "garbage", Keep: -1}); err == nil {
		t.Fatal("expected error on invalid --keep-since")
	}
}
```

> Implementer: build a `deepcleanFixture` that scripts `nix-env --list-generations` for `system` (sudo) and any user profiles created, plus `nix-store --gc`/`nix path-info` as needed. **The live path calls `storeSize()` TWICE (Store before + Store after), so script the `df`+`diskutil` pair TWICE** — FakeRunner consumes responses FIFO per (name,args), so add two identical `df` and two identical `diskutil` responses (or the run fails with "no scripted response"). Dry-run does not need store-size at all (it prints the dead-paths estimate instead). Assert streaming via `Call.Opts.Stdout != nil` on the `--gc` call. Assert symlink removal by `os.Lstat` after the run.

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `deepclean.go`.** Add a pure `parseKeepSince(string) (days int, ok bool)` helper: `^(\d+)w$`→×7, `^(\d+)d$`→N (so `0d`→`(0,true)`, `0w`→`(0,true)`), anything else→`(0,false)`. **NB6 — resolution ordering MUST be:**

  ```go
  keepDays := cfg.KeepDays
  if opts.KeepSince != "" {            // empty ⇒ keep config default; do NOT call parseKeepSince("")
      d, ok := parseKeepSince(opts.KeepSince)
      if !ok { return fmt.Errorf("--keep-since must be <N>d or <N>w (e.g. 14d, 2w)") }
      keepDays = d                     // 0d ⇒ keepDays=0 ⇒ time protection OFF
  }
  keepCount := cfg.KeepCount
  if opts.Keep >= 0 { keepCount = opts.Keep }   // <0 ⇒ config default; 0 ⇒ count protection OFF
  ```

  Resolve `now := time.Now()`. Thread `out`/`errOut` to every section. Stream the GC call.

- [ ] **Step 3b: Add tests** — `TestParseKeepSince` (table: `7d→7,true`; `2w→14,true`; `0d→0,true`; `0w→0,true`; `garbage→_,false`; `""→_,false`) AND `TestDeepClean_EmptyKeepSinceUsesConfigDefault`: config `keep_days=14`, `opts.KeepSince=""`, a gen dated 10d ago → NOT pruned (config 14d protects it), proving `""` resolves to config 14 and is NOT treated as `0d`.

- [ ] **Step 4: Run** — `go test ./internal/store/ -run TestDeepClean -v` → PASS.

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat(pn/store): deepclean prunes generations/roots with live GC + summary"`

---

## Task 7: CLI flags & writer wiring

**Files:**

- Modify: `internal/cli/store.go`
- Test: `internal/cli/store_test.go` (create; follow `workspace_test.go` cobra patterns)

**Interfaces:**

- Consumes: `store.Audit(ctx, out, errOut, AuditOptions)`, `store.DeepClean(ctx, out, errOut, DeepCleanOptions)`.

**Behavior:** wire flags matching the bash:

- `audit`: `--full` (bool).
- `deepclean`: `--dry-run` (bool); `--keep-since` (string, default ""); `--keep` (int, default -1 so "unset" → config).
- Both pass `cmd.OutOrStdout()` as `out`, `cmd.ErrOrStderr()` as `errOut`, and `cmd.Context()`.
- Add `Long:` help to all three commands (parent `store` + both subcommands) recovering the deleted bash help — see the `Long:` strings below and in `docs/pn-store.md`. The parent `addStoreCmd` gets:

  ```go
  Long: `Audit and reclaim space in the local Nix store.

  Subcommands:
    audit      Read-only report of profile generations, closure sizes, and store usage.
    deepclean  Prune old generations and stale GC roots, then garbage-collect the store.

  Configuration lives in ~/.config/pn/store.toml (search_dirs, keep_days, keep_count).
  See docs/pn-store.md for user journeys and retention semantics.`,
  ```

- [ ] **Step 1: Write failing test** — assert flags exist and parse:

```go
package cli

import (
	"bytes"
	"testing"
)

func TestStoreAudit_HasFullFlag(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "audit", "--help"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("--full")) {
		t.Fatalf("audit --help missing --full:\n%s", buf.String())
	}
}

func TestStoreDeepClean_HasFlags(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "deepclean", "--help"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--dry-run", "--keep-since", "--keep"} {
		if !bytes.Contains(buf.Bytes(), []byte(f)) {
			t.Errorf("deepclean --help missing %s", f)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** `cli/store.go`:

```go
func storeAuditCmd() *cobra.Command {
	var full bool
	c := &cobra.Command{
		Use:   "audit",
		Short: "Audit nix store contents",
		Long: `Audit Nix profile generations and store size.

Reports, in order: System Profiles, Home Manager, User Profiles, Devbox Global,
Devbox Projects, and Nix Store (volume used). Read-only — makes no changes.

With --full, also estimates reclaimable space from dead store paths
(runs 'sudo nix-store --gc --print-dead'; slow).

Examples:
  # Show profile generations and store usage
  pn store audit

  # Include the reclaimable-space estimate
  pn store audit --full`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(exec.NewRealRunner()).Audit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store.AuditOptions{Full: full})
		},
	}
	c.Flags().BoolVar(&full, "full", false, "Include dead paths estimate (slow, requires sudo)")
	return c
}

func storeDeepCleanCmd() *cobra.Command {
	var dryRun bool
	var keepSince string
	var keep int
	c := &cobra.Command{
		Use:   "deepclean",
		Short: "Aggressive nix store cleanup",
		Long: `Clean old Nix profile generations, stale GC roots, and garbage-collect the store.

Cleans:
  - System, home-manager, user, and devbox profile generations
  - Result symlinks (nix build outputs) under configured search_dirs
  - Stale ~/.nix-profiles/ entries (mtime older than --keep-since)
  - NH temp roots in TMPDIR

Retention is a UNION keep: a generation is kept if it is current, OR within the
most recent --keep generations, OR newer than --keep-since. The current
generation is always kept. --keep-since 0d disables time protection; --keep 0
disables count protection. Defaults come from ~/.config/pn/store.toml
(keep_days=14, keep_count=3).

After a live run, prints a runtime-roots summary: store paths held only by
running processes that could be freed by restarting applications.

Examples:
  # Preview without deleting anything
  pn store deepclean --dry-run

  # Aggressive clean keeping only the most recent generation
  pn store deepclean --keep-since 0d --keep 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(exec.NewRealRunner()).DeepClean(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store.DeepCleanOptions{
				DryRun: dryRun, KeepSince: keepSince, Keep: keep,
			})
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be cleaned without deleting")
	c.Flags().StringVar(&keepSince, "keep-since", "", "Keep generations newer than this (<N>d or <N>w; default from config)")
	c.Flags().IntVar(&keep, "keep", -1, "Keep N most recent generations (default from config; 0 disables count protection)")
	return c
}
```

- [ ] **Step 4: Run** — `go test ./internal/cli/ -run TestStore -v` → PASS.

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat(pn/store): wire --full/--dry-run/--keep-since/--keep + writer"`

---

## Task 8: Full verification gate

**Files:** none (verification only).

- [ ] **Step 1:** `cd modules/pn && go test ./...` → all PASS.
- [ ] **Step 2:** `go vet ./...` → clean.
- [ ] **Step 3:** From repo root: `nix flake check` → PASS (builds pn, runs Go tests in the derivation).
- [ ] **Step 4:** Build-only activation check (NEVER switch): `darwin-rebuild build --flake .` from the terminal flake → PASS. (Per global rules; do not run `switch`.)
- [ ] **Step 5: Manual smoke (read-only paths only):**
  - `nix run .#pn -- store audit` → prints sectioned report with live progress; store size shown.
  - `nix run .#pn -- store deepclean --dry-run` → prints per-section "would prune" + DRY RUN summary; **no** sudo/GC/delete invoked. Verify output streams (sections appear as they complete), not withheld.
  - Do NOT run live `deepclean` as part of the gate (it calls `sudo` + deletes). Note in the PR that live deepclean was validated by unit tests + dry-run only.
- [ ] **Step 6: Commit** any test/doc fixups; open PR / finish branch via `superpowers:finishing-a-development-branch`.

---

## Task 9: User-facing docs

**Files:**

- Already created: `docs/pn-store.md` (config schema, UNION-keep retention, audit/deepclean sections + asymmetry, user journeys A–E, two mermaid diagrams, implementation pointer).

- [ ] **Step 1:** Verify `docs/pn-store.md` matches the IMPLEMENTED behavior (section names/order, flag semantics, defaults) — update it if implementation revealed any drift.
- [ ] **Step 2:** Confirm the three cobra `Long:` strings (Task 7) match `docs/pn-store.md`.
- [ ] **Step 3: Commit** — `git add docs/pn-store.md && git commit -m "docs(pn/store): user journeys + config/retention reference"`.

---

## Round-2 Review Addenda (4 independent reviews, 2026-06-26)

Four reviewers (deep-correctness, pn-workspace pattern-conformance, test-sufficiency, user-journeys) reviewed the post-critique plan. Their must-fix items are folded into the tasks above; this section is the authoritative checklist for the remaining cross-cutting requirements. **Apply ALL of these during implementation.**

### A. Output-byte exactness (parity-critical — the bats were grep-only and will NOT catch these)

The user-visible output IS the product. Reproduce these EXACT bytes (verify against the bash with a golden test, not substring checks):

1. **Generation line (audit block):** `list_generations` emits `<gen> <date> <current|>` via `awk 'print gen, date, current'` — for a NON-current gen the third field is empty, yielding a **trailing space**: `"1 2024-01-01 "`. The audit block indents each by 4 spaces. Reconstruct as `fmt.Sprintf("    %d %s %s", g.Num, g.Date, currentWord)` where `currentWord` is `"current"` for current and `""` otherwise (KEEP the trailing space).
2. **`current` token:** bash renders the current marker as the bare word `current`, NOT `(current)`. (It parses `(current)` from `nix-env` but prints `current`.)
3. **Per-profile audit block shape** (exact indentation + trailing blank line):
   ```
     <label>:
       Profile: <profile>
       <gen lines, 4-space indented>
       Closure size: <size>
   <blank line>
   ```
4. **`(not installed)` / `(no search dirs configured)` / `nothing to prune` / `nothing to clean`** — exact strings, 2-space indent (`  (not installed)`).
5. **Deepclean prune line:** `  <label>: N generation(s) to prune (1 2)` — number list space-joined (bash `${to_delete[*]}`), NOT comma-joined.
6. **Summary `generation(s)` literal:** bash prints `  <category>: <n> generation(s)` for ALL nine categories — including `result-symlinks`, `stale-nix-profiles`, `nh-temp-roots` (a bash quirk; reproduce the literal `generation(s)` suffix on every category).
7. **Nine-category fixed order** (dry-run "Would prune" and live "Pruned generations"): `system home-manager user-profiles devbox-global devbox-util devbox-projects result-symlinks stale-nix-profiles nh-temp-roots`.
8. **Em-dash:** `DRY RUN — no changes made` uses U+2014 EM DASH, not a hyphen. (The substring test `Contains(out,"DRY RUN")` won't catch a hyphen mistake — a golden test will.)
9. **Two-space alignment:** live summary uses `Store after:  <size>` (two spaces, to align under `Store before: `).
10. **Runtime-roots:** line 1 `<n> store <path|paths> held only by running processes (up to <size> reclaimable)`; line 2 `  Tip: Restarting applications and re-running may free additional space` (2-space indent, singular `path` at n==1).

**Add golden-output tests** (fully-scripted FakeRunner + temp HOME) for: audit normal, audit `--full`, deepclean dry-run, deepclean live. These lock items 1–10. Plus a dry-run converse assertion: no recorded `Call.Opts.Stdout != nil` (nothing streams when nothing runs).

### B. Required additional tests (from test-sufficiency review — port the bats bar)

Port the currently-missing **deepclean** bats cases (each as a Go test):

- `--keep-since 7d` overrides config (gen 50d old → pruned).
- no-flags config-defaults protect by count (2 gens, keep_count=3 → nothing pruned, output `nothing to prune`).
- reads `keep_days` from config (7/1 → gen1 pruned).
- missing config → defaults (deepclean path).
- removes stale `~/.nix-profiles` + `--dry-run` preserves.
- removes `nh-darwin*/result` temp roots + `--dry-run` preserves.
- **devbox label regression:** output contains `~/projects/repo-alpha:` and does NOT match `^\s+\.devbox:` (anchored negative — this is the specific regression the bash test guarded).
- runtime-roots summary printed AFTER `Store after:` on live runs (`=== Runtime Roots ===`).
- normal-HM processing when NOT orphaned (output lacks `orphaned standalone profile`).

Parity-trap UNIT tests (these helpers have flagged traps and zero scripted coverage):

- `discoverResultSymlinks`: `result`→`/nix/store/x` matches; `result`→`/elsewhere` excluded; `result-1`→`/nix/store/y` matches; uses `os.Readlink` on dangling targets.
- `nhTempRoots`: `<tmp>/nh-darwinX/result` matches; `<tmp>/result` and `<tmp>/foo/result` excluded (path glob, not basename).
- `devboxProjects`: git-worktree expansion (script `git -C <repo> worktree list --porcelain` → `worktree <path>`), dedup, AND a missing-search-dir warning routed to `errOut` (not `out`).

Round out the unit matrix:

- `formatProfileLabel` for ALL 7 categories (add `home-manager`, `devbox-global`, `devbox-util`, `user-profiles` basename, and **devbox-projects OUTSIDE `$HOME` → absolute path**).
- `generationsToPrune` unparseable-date → pruned (not time-protected).
- `runtimeRootsSummary` empty→`""`, singular `path` at n==1, and a path that is BOTH `{lsof}` and a file root → excluded (the `comm -23` set-subtraction).
- `deadPathsSize` empty→`"0 B"` and two-paths→summed (UNSORTED arg order — bash pipes `--print-dead` straight to `xargs`).
- `profileClosureSize` success path and `0`/empty-bytes → `"unknown"`.
- `pruneGenerations` empty-nums → no `Run` call.
- `userProfiles` absent-dir → empty.
- `storeSize` malformed/short `df` output → `"0 B"` (defensive), and the device-passthrough assertion (diskutil receives the df device).

### C. Pattern-conformance (read like the surrounding code)

- **`Env` seam is a deliberate departure** from the established `stateDir()`-free-function + `t.Setenv` idiom (`internal/workspace/updatecache.go`). Rationale to record in `env.go`'s doc comment: an explicit `Env` value object makes the three roots (Home/XDG/TMPDIR) explicit and avoids global env mutation in tests (keeps tests `t.Parallel`-safe). The `lstatModTime` package-level `var` seam DOES have precedent (`var openWorkspace` in `cli/workspace.go`). Keep both; just document the `Env` choice so a reviewer doesn't read it as accidental.
- **`store` is intentionally NOT `//go:build darwin`-tagged** (unlike `internal/osx`): its core (config, generations, discovery, prune) is cross-platform; only `storeSize`'s `diskutil` path is darwin-specific and degrades to `"0 B"` elsewhere. Add a one-line rationale in `store.go` or `size.go`.
- **Drop plan-sample shims:** call `sort.Strings` directly (no `sortStrings`); collapse `secondField([]byte)`/`secondFieldFromLine(string)` into one helper (have the `[]byte` form delegate). Add `"strings"`/`"sort"` to the relevant test/impl imports.
- **Optional:** extract a private `isSymlink(path string) bool` in `discover.go` to collapse the repeated `os.Lstat(...) ; err==nil && fi.Mode()&os.ModeSymlink != 0` (appears in `staleNixProfiles`, `isOrphanedStandaloneHMProfile`, `walkSymlinks`, `homeManagerGenLinks`).
- **`profileClosureSize` reads the real FS** via `filepath.EvalSymlinks` before the subprocess — the one place a test touches outside the injected `Env`. In tests the profile paths don't exist so it falls back to the raw path (fine); add a code comment noting this.

### D. Self-review of incorporated fixes

Reviewer-confirmed CORRECT: B1, B2, B5, B6, N1, N5, N6, N7, N8, N10. Reviewer-found-and-now-fixed: NB1 (runtime-roots guard documented), NB3 (`homeManagerGenLinks` added), NB4 (audit error tolerance + test), NB5 (boundary test assertion corrected — was inverted), NB6 (keep-since resolution ordering + tests). Pattern/doc fixes: config read/parse split, Task 7 stale signature, `Long:` strings, `docs/pn-store.md`, Env/build-tag rationale.

---

## Self-Review (completed by plan author)

1. **Spec coverage:** audit sections ✓ (Task 5), deepclean sections + summary + runtime roots ✓ (Task 6), all `pn-lib` helpers mapped — config (T1), sizes/format (T2), discovery + label + orphan (T3), generations + union-keep (T4). Flags `--full/--dry-run/--keep-since/--keep` ✓ (T7). Output streaming + GC live tee ✓ (Global Constraints, T6). `du` removed in favor of df+diskutil ✓.
2. **Placeholder scan:** `walkSymlinks`/`devboxProjects` bodies are specified by Behavior + bash reference, not full Go — flagged as implementer work with exact maxdepth/match semantics. The size_test.go `contains/indexOf` shims are explicitly marked for collapse to `strings.Contains`. These are the only non-complete code blocks; everything logic-bearing (config, formatSize, generationsToPrune, store_size parse, prune) is complete.
3. **Type consistency:** `Audit(ctx, out, errOut io.Writer, AuditOptions)` and `DeepClean(ctx, out, errOut io.Writer, DeepCleanOptions)` used consistently across T5/T6/T7. `generation`, `Env`, `Config` names stable. `lstatModTime` seam referenced in T3 test and impl.

**Decisions (resolved):** (a) symlink-mtime test seam (`lstatModTime`) vs real `Lutimes` — injectable func, no new dep; tests inject mtimes through it (never `os.Chtimes`, which follows the link). (b) `df` subprocess vs native `statfs` — `df` via Runner for unit-test fidelity (mirrors bash mocks) and to avoid a darwin build-tag split, accepting one extra subprocess. (c) devbox-project warnings → `errOut` (both `Audit`/`DeepClean` now carry `out, errOut`).

**Incorporated independent-critique fixes (2026-06-26):**

- **B1** `TestStaleNixProfiles` rewritten to inject mtime via `lstatModTime` (was unrunnable: `os.Chtimes` follows the symlink to a missing target).
- **B2** Documented accepted ≤24h staleness-cutoff deviation from bash `-mtime +N` (strict `now - keepDays*24h`).
- **B3** `generationsToPrune` parses dates with `time.ParseInLocation(..., time.Local)` (was UTC → boundary TZ skew); added a boundary test.
- **B5** `runtimeRootsSummary` extracts store path via field `[2]` (bash `$3`), not last field.
- **B6** Explicit step to DELETE the six stub-era `store_test.go` tests before Task 5/6; corrected the Task 0 Step 5 "still compiles" wording.
- **N1** Added `errOut io.Writer` to `Audit`/`DeepClean`/CLI; devbox warnings routed there.
- **N5** `walkSymlinks` takes a relative-PATH matcher (not basename-only) so `*/nh-darwin*/result` is expressible.
- **N6** Result-symlink target check uses `os.Readlink` (single-hop), NOT `EvalSymlinks` (dangling `/nix/store` targets in tests).
- **N7** `deepcleanFixture` must script the `df`+`diskutil` pair TWICE for live runs (Store before + after).
- **N8** Added `TestDeepClean_RemovesOrphanedStandaloneHM`.
- **N10** `size_test.go` uses `strings.Contains` directly (no non-compiling shims).
