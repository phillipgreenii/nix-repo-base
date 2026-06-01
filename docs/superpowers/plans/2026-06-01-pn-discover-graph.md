# pn Discover() graph + multi-remote implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the alphabetical, flat `Workspace.Discover()` in nix-repo-base's pn Go module with a topology-aware graph-builder that produces `Repo{InputName, IsTerminal}` in dep-first / terminal-last order, with multi-remote repo identity and a shared worker pool for per-repo subprocess fan-out.

**Architecture:** Extend `internal/workspace/config.go` for the new TOML fields, add an isolated `slug.go` with the github-slug regex menu and derivation helpers, build a new `internal/exec/workerpool.go` that wraps the existing `Runner` interface, then assemble the graph in `discover.go` from per-repo `nix eval --json --file flake.nix inputs` + `git remote -v` outputs that the pool runs concurrently. Terminal selection is explicit via `[workspace].terminal`; no alphabetical guess.

**Tech Stack:** Go 1.25, `pelletier/go-toml/v2`, standard-library `sync`/`os/exec`. No new third-party deps.

**Spec:** [`docs/superpowers/specs/2026-06-01-pn-discover-graph-design.md`](../specs/2026-06-01-pn-discover-graph-design.md)

**Bead:** tc-perh.5.2

---

## File map

| Path | Status | Responsibility |
|---|---|---|
| `modules/pn/internal/workspace/config.go` | MODIFY | Add `Remote`, `RepoConfig.Remotes`, `RepoConfig.Slug`, `WorkspaceSection.Terminal`; parse-time validation. |
| `modules/pn/internal/workspace/config_test.go` | MODIFY | Cover new validation rules. |
| `modules/pn/internal/workspace/slug.go` | NEW | `ExtractGithubSlug`, `CanonicalSlug`, `SlugSet`. |
| `modules/pn/internal/workspace/slug_test.go` | NEW | Table-driven slug tests. |
| `modules/pn/internal/exec/workerpool.go` | NEW | Bounded worker pool wrapping a `Runner`. |
| `modules/pn/internal/exec/workerpool_test.go` | NEW | Bounded concurrency, error aggregation. |
| `modules/pn/internal/workspace/discover.go` | REWRITE | Topo-graph implementation; replaces alphabetical impl. |
| `modules/pn/internal/workspace/discover_test.go` | MODIFY | Replace alphabetical-order assertions with graph tests; add multi-remote, terminal-selection, inputName tests. |
| `modules/pn/internal/workspace/workspace.go` | MODIFY | Construct `WorkerPool` in `Open`, expose via `Workspace`. |

No changes to `build.go`, `apply.go`, `flake_check.go`, `nix.go`, `tree.go` in this plan — they are deferred follow-ups per the spec §17.

---

### Task 1: Add `Remote`, `Remotes`, `Slug` to `RepoConfig`; add `Terminal` to `WorkspaceSection`; reject mutual exclusion

**Files:**
- Modify: `modules/pn/internal/workspace/config.go`
- Test: `modules/pn/internal/workspace/config_test.go`

- [ ] **Step 1: Write the failing tests for the new validation rules**

Replace the current `config_test.go` body with the following tests (preserving any existing tests in the file by appending these — read the file first):

```go
func TestParseConfig_RejectsBothUrlAndRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
url = "github:o/foo"
remotes = [{ name = "origin", url = "github:o/foo" }]
`))
	if err == nil {
		t.Fatal("expected error: url + remotes are mutually exclusive")
	}
	if !strings.Contains(err.Error(), "foo") || !strings.Contains(err.Error(), "remotes") {
		t.Errorf("error should name the repo and remotes: %v", err)
	}
}

func TestParseConfig_RejectsEmptyRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = []
`))
	if err == nil {
		t.Fatal("expected error: empty remotes is invalid")
	}
}

func TestParseConfig_RejectsMultipleOriginRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = [
  { name = "origin", url = "github:o/foo" },
  { name = "origin", url = "github:o/bar" },
]
`))
	if err == nil {
		t.Fatal("expected error: at most one remote may be named origin")
	}
}

func TestParseConfig_AcceptsRemotesWithoutOrigin(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
remotes = [
  { name = "bitbucket", url = "git@bitbucket.org:o/foo.git" },
  { name = "gitlab",    url = "git@gitlab.com:o/foo.git" },
]
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Repos["foo"].Remotes) != 2 {
		t.Errorf("expected 2 remotes, got %d", len(cfg.Repos["foo"].Remotes))
	}
}

func TestParseConfig_AcceptsExplicitSlug(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
url = "github:o/foo"
slug = "o/canonical"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Repos["foo"].Slug != "o/canonical" {
		t.Errorf("Slug: got %q", cfg.Repos["foo"].Slug)
	}
}

func TestParseConfig_AcceptsWorkspaceTerminal(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[workspace]
name = "x"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workspace.Terminal != "foo" {
		t.Errorf("Terminal: got %q", cfg.Workspace.Terminal)
	}
}

func TestParseConfig_RejectsTerminalPointingAtUnknownRepo(t *testing.T) {
	_, err := ParseConfig([]byte(`
[workspace]
name = "x"
terminal = "nonexistent"

[repos.foo]
url = "github:o/foo"
`))
	if err == nil {
		t.Fatal("expected error: terminal names a repo not in [repos.*]")
	}
}
```

Add this import if not already present:
```go
import "strings"
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/tcadmin/workspace/nix-repo-base/modules/pn
go test ./internal/workspace/ -run TestParseConfig -v 2>&1 | tail -30
```

Expected: all 7 new tests FAIL with either undefined-symbol errors (no `Terminal` / `Remotes` / `Slug` fields yet) or "expected error: got nil".

- [ ] **Step 3: Extend the struct definitions**

In `modules/pn/internal/workspace/config.go`, change:

```go
type WorkspaceSection struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
}
```

to:

```go
type WorkspaceSection struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Terminal    string `toml:"terminal"`
}
```

And change:

```go
type RepoConfig struct {
	URL    string `toml:"url"`
	Branch string `toml:"branch"`
}
```

to:

```go
// Remote is one named git remote that publishes a workspace repo.
type Remote struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

type RepoConfig struct {
	URL     string   `toml:"url"`
	Remotes []Remote `toml:"remotes"`
	Slug    string   `toml:"slug"`
	Branch  string   `toml:"branch"`
}
```

- [ ] **Step 4: Add validation in `ParseConfig`**

Inside the existing per-repo loop in `ParseConfig`, after the existing `URL == ""` check (which we now relax — it's only an error if BOTH url and remotes are absent), replace the section:

```go
// Apply repo defaults + validate each repo.
for name, r := range cfg.Repos {
	if r.URL == "" {
		return nil, fmt.Errorf("repo %q: url is required", name)
	}
	if r.Branch == "" {
		r.Branch = "main"
	}
	cfg.Repos[name] = r
}
```

with:

```go
// Apply repo defaults + validate each repo.
for name, r := range cfg.Repos {
	if r.URL != "" && len(r.Remotes) > 0 {
		return nil, fmt.Errorf("repo %q: url and remotes are mutually exclusive", name)
	}
	if r.URL == "" && len(r.Remotes) == 0 {
		return nil, fmt.Errorf("repo %q: must specify url or remotes", name)
	}
	if len(r.Remotes) > 0 {
		originCount := 0
		for _, rm := range r.Remotes {
			if rm.Name == "origin" {
				originCount++
			}
			if rm.Name == "" {
				return nil, fmt.Errorf("repo %q: remote entry missing name", name)
			}
			if rm.URL == "" {
				return nil, fmt.Errorf("repo %q: remote %q missing url", name, rm.Name)
			}
		}
		if originCount > 1 {
			return nil, fmt.Errorf("repo %q: at most one remote may be named origin (found %d)", name, originCount)
		}
	}
	if r.Branch == "" {
		r.Branch = "main"
	}
	cfg.Repos[name] = r
}
// Validate workspace.terminal points at a declared repo.
if cfg.Workspace.Terminal != "" {
	if _, ok := cfg.Repos[cfg.Workspace.Terminal]; !ok {
		return nil, fmt.Errorf("workspace.terminal %q is not a declared repo", cfg.Workspace.Terminal)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestParseConfig -v 2>&1 | tail -25
```

Expected: all 7 new tests PASS. Existing `TestOpen_*` and old `ParseConfig` tests must remain green; if any break it's because they relied on the old shape — fix by reading the failure.

- [ ] **Step 6: Run the full workspace package tests**

```bash
go test ./internal/workspace/ 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/tcadmin/workspace/nix-repo-base
git add modules/pn/internal/workspace/config.go modules/pn/internal/workspace/config_test.go
git commit -m "$(cat <<'EOF'
feat(pn): extend RepoConfig with Remotes, Slug; add WorkspaceSection.Terminal

Parse-time validation enforces url and remotes are mutually exclusive,
remotes (if present) is non-empty and has at most one origin entry, and
workspace.terminal (if set) names a declared repo. Backward compatible:
single-url configs parse unchanged.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: GitHub-slug regex menu

**Files:**
- Create: `modules/pn/internal/workspace/slug.go`
- Create: `modules/pn/internal/workspace/slug_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/workspace/slug_test.go`:

```go
package workspace

import "testing"

func TestExtractGithubSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// github: flake refs
		{"github:phillipgreenii/nix-overlay", "phillipgreenii/nix-overlay"},
		{"github:phillipgreenii/nix-overlay/main", "phillipgreenii/nix-overlay"},
		{"github:o/r/sub/dir", "o/r"},
		// https
		{"https://github.com/owner/repo", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo/", "owner/repo"},
		{"https://github.com/owner/repo/tree/main", "owner/repo"},
		// ssh shorthand
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"git@github.com:owner/repo", "owner/repo"},
		// ssh url
		{"ssh://git@github.com/owner/repo", "owner/repo"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		// Non-matches
		{"git@bitbucket.org:phillipgreenii/homelab.git", ""},
		{"ssh://git@synfra.twistcone.us:222/twistcone/homelab.git", ""},
		{"https://gitlab.com/owner/repo", ""},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ExtractGithubSlug(tc.in)
			if got != tc.want {
				t.Errorf("ExtractGithubSlug(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/tcadmin/workspace/nix-repo-base/modules/pn
go test ./internal/workspace/ -run TestExtractGithubSlug 2>&1 | tail -10
```

Expected: build failure (`undefined: ExtractGithubSlug`).

- [ ] **Step 3: Implement `ExtractGithubSlug`**

Create `modules/pn/internal/workspace/slug.go`:

```go
package workspace

import "regexp"

var (
	// github:owner/repo  OR  github:owner/repo/anything
	reGithubFlake = regexp.MustCompile(`^github:([^/]+)/([^/]+?)(?:/.*)?$`)
	// https://github.com/owner/repo  with optional .git and/or trailing /...
	reGithubHTTPS = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/.]+?)(?:\.git)?(?:/.*)?$`)
	// git@github.com:owner/repo  with optional .git
	reGithubSSHShorthand = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/.]+?)(?:\.git)?$`)
	// ssh://git@github.com/owner/repo  with optional .git and trailing path
	reGithubSSHURL = regexp.MustCompile(`^ssh://git@github\.com/([^/]+)/([^/.]+?)(?:\.git)?(?:/.*)?$`)
)

// ExtractGithubSlug returns the "owner/repo" form for any github URL form
// recognized by this package. Returns the empty string for any non-github
// input (Forgejo, Bitbucket, GitLab, malformed, etc).
//
// Forms accepted:
//   - github:owner/repo  (and github:owner/repo/path)
//   - https://github.com/owner/repo  (with optional .git, with optional /tree/...)
//   - git@github.com:owner/repo[.git]
//   - ssh://git@github.com/owner/repo[.git]
func ExtractGithubSlug(url string) string {
	for _, re := range []*regexp.Regexp{
		reGithubFlake, reGithubHTTPS, reGithubSSHShorthand, reGithubSSHURL,
	} {
		if m := re.FindStringSubmatch(url); m != nil {
			return m[1] + "/" + m[2]
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestExtractGithubSlug -v 2>&1 | tail -25
```

Expected: 18 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/slug.go modules/pn/internal/workspace/slug_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add ExtractGithubSlug regex menu

Ports extract_github_slug from the deleted pn-discover-workspace.sh.
Recognizes github: flake refs, https://, git@github.com:, and
ssh://git@github.com/ forms. Non-github URLs (Forgejo, Bitbucket,
GitLab) return the empty string.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Canonical slug + slug set per repo

**Files:**
- Modify: `modules/pn/internal/workspace/slug.go`
- Modify: `modules/pn/internal/workspace/slug_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `modules/pn/internal/workspace/slug_test.go`:

```go
func TestCanonicalSlug_ExplicitOverride(t *testing.T) {
	r := RepoConfig{
		URL:  "github:o/foo",
		Slug: "o/canonical",
	}
	if got := CanonicalSlug(r); got != "o/canonical" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/canonical")
	}
}

func TestCanonicalSlug_SingleURL(t *testing.T) {
	r := RepoConfig{URL: "github:o/foo"}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_RemotesWithOrigin(t *testing.T) {
	r := RepoConfig{
		Remotes: []Remote{
			{Name: "bitbucket", URL: "git@bitbucket.org:o/foo.git"},
			{Name: "origin", URL: "github:o/foo"},
		},
	}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_RemotesWithoutOrigin_FirstWins(t *testing.T) {
	r := RepoConfig{
		Remotes: []Remote{
			{Name: "github", URL: "github:o/foo"},
			{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
		},
	}
	if got := CanonicalSlug(r); got != "o/foo" {
		t.Errorf("CanonicalSlug: got %q, want %q", got, "o/foo")
	}
}

func TestCanonicalSlug_NonGithubURL_Empty(t *testing.T) {
	r := RepoConfig{URL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"}
	if got := CanonicalSlug(r); got != "" {
		t.Errorf("CanonicalSlug: got %q, want empty", got)
	}
}

func TestSlugSet_UnionOfAllRemotesPlusExplicit(t *testing.T) {
	r := RepoConfig{
		Slug: "explicit/slug",
		Remotes: []Remote{
			{Name: "origin", URL: "github:o/foo"},
			{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
			{Name: "forgejo", URL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"}, // no slug
		},
	}
	got := SlugSet(r)
	want := map[string]bool{
		"explicit/slug":  true,
		"o/foo":          true,
		"o/foo-mirror":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("SlugSet size: got %d, want %d (got: %v)", len(got), len(want), got)
	}
	for s := range want {
		if !got[s] {
			t.Errorf("SlugSet missing %q (got: %v)", s, got)
		}
	}
}

func TestSlugSet_SingleURL(t *testing.T) {
	r := RepoConfig{URL: "github:o/foo"}
	got := SlugSet(r)
	if !got["o/foo"] || len(got) != 1 {
		t.Errorf("SlugSet: got %v, want {o/foo}", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run "TestCanonicalSlug|TestSlugSet" 2>&1 | tail -10
```

Expected: build failure (`undefined: CanonicalSlug`, `undefined: SlugSet`).

- [ ] **Step 3: Implement `CanonicalSlug` and `SlugSet`**

Append to `modules/pn/internal/workspace/slug.go`:

```go
// CanonicalSlug returns the single canonical slug for a repo, per the rules
// in the design doc §5.1:
//
//  1. RepoConfig.Slug wins if set.
//  2. Else if Remotes has an entry named "origin", derive from its URL.
//  3. Else if Remotes is non-empty, derive from the first entry.
//  4. Else (URL set) derive from URL.
//  5. Else (derivation fails) return empty string.
func CanonicalSlug(r RepoConfig) string {
	if r.Slug != "" {
		return r.Slug
	}
	if len(r.Remotes) > 0 {
		for _, rm := range r.Remotes {
			if rm.Name == "origin" {
				return ExtractGithubSlug(rm.URL)
			}
		}
		return ExtractGithubSlug(r.Remotes[0].URL)
	}
	return ExtractGithubSlug(r.URL)
}

// SlugSet returns the set of all slugs that identify a repo. Used for graph
// edge matching: any input URL whose slug is in some repo's SlugSet refers
// to that repo. Per design §5.2 the set is the union of slugs from every
// remote (or the single URL) plus the explicit Slug override if set.
func SlugSet(r RepoConfig) map[string]bool {
	out := make(map[string]bool, 4)
	if r.Slug != "" {
		out[r.Slug] = true
	}
	if len(r.Remotes) > 0 {
		for _, rm := range r.Remotes {
			if s := ExtractGithubSlug(rm.URL); s != "" {
				out[s] = true
			}
		}
	} else if s := ExtractGithubSlug(r.URL); s != "" {
		out[s] = true
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run "TestCanonicalSlug|TestSlugSet" -v 2>&1 | tail -30
```

Expected: all 7 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/slug.go modules/pn/internal/workspace/slug_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add CanonicalSlug and SlugSet derivation

CanonicalSlug returns one slug per repo for display/identity (toml Slug
override > origin remote > first remote > single url). SlugSet returns
the union for graph-edge matching, so a sibling input URL pointing at
any remote of the same repo resolves correctly.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Worker pool

**Files:**
- Create: `modules/pn/internal/exec/workerpool.go`
- Create: `modules/pn/internal/exec/workerpool_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/exec/workerpool_test.go`:

```go
package exec

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_RunsJobsInParallelUpToLimit(t *testing.T) {
	const workers = 3
	const jobs = 9
	pool := NewWorkerPool(NewFakeRunner(), workers)

	var inFlight, peak int64
	var mu sync.Mutex

	track := func() {
		mu.Lock()
		defer mu.Unlock()
		cur := atomic.AddInt64(&inFlight, 1)
		if cur > peak {
			peak = cur
		}
	}
	release := func() { atomic.AddInt64(&inFlight, -1) }

	results := make([]error, jobs)
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		i := i
		wg.Add(1)
		pool.Submit(func() {
			defer wg.Done()
			track()
			time.Sleep(50 * time.Millisecond)
			release()
			results[i] = nil
		})
	}
	wg.Wait()
	pool.Close()

	if peak > int64(workers) {
		t.Errorf("peak in-flight = %d; pool size %d; should never exceed", peak, workers)
	}
	if peak < int64(workers) {
		t.Logf("peak in-flight = %d (workers = %d); expected = workers when jobs ≫ workers", peak, workers)
	}
}

func TestWorkerPool_CollectsErrorsViaCallback(t *testing.T) {
	pool := NewWorkerPool(NewFakeRunner(), 2)
	var collected []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 3; i++ {
		i := i
		pool.Submit(func() {
			defer wg.Done()
			if i == 1 {
				mu.Lock()
				collected = append(collected, errors.New("job 1 failed"))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	pool.Close()
	if len(collected) != 1 || collected[0].Error() != "job 1 failed" {
		t.Errorf("collected errors: %v", collected)
	}
}

func TestWorkerPool_RunMethodDispatchesToInnerRunner(t *testing.T) {
	inner := NewFakeRunner()
	inner.AddResponse("echo", []string{"hi"}, Result{Stdout: []byte("hi\n")}, nil)
	pool := NewWorkerPool(inner, 2)
	res, err := pool.Run(context.Background(), "echo", []string{"hi"}, RunOptions{})
	pool.Close()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hi\n" {
		t.Errorf("Run stdout: %q", res.Stdout)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/tcadmin/workspace/nix-repo-base/modules/pn
go test ./internal/exec/ -run TestWorkerPool 2>&1 | tail -10
```

Expected: build failure (`undefined: NewWorkerPool`).

- [ ] **Step 3: Implement `WorkerPool`**

Create `modules/pn/internal/exec/workerpool.go`:

```go
package exec

import (
	"context"
	"sync"
)

// WorkerPool dispatches Submit jobs across a bounded number of worker
// goroutines. It also implements Runner by forwarding Run calls to its inner
// Runner — this lets pool consumers pass a single value that doubles as both
// the subprocess executor and the concurrency orchestrator.
//
// Submit jobs run concurrently up to the configured worker count. Run calls
// bypass the worker pool (they invoke the inner runner directly), since the
// caller may want a synchronous, prompt subprocess result. Submit is for
// fan-out; Run is for the single-call path.
type WorkerPool struct {
	inner   Runner
	jobs    chan func()
	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
}

// NewWorkerPool returns a WorkerPool with `workers` background goroutines.
// `workers` must be >= 1; values less than 1 are clamped to 1.
func NewWorkerPool(inner Runner, workers int) *WorkerPool {
	if workers < 1 {
		workers = 1
	}
	p := &WorkerPool{
		inner: inner,
		jobs:  make(chan func()),
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *WorkerPool) worker() {
	defer p.wg.Done()
	for job := range p.jobs {
		job()
	}
}

// Submit schedules `fn` for execution on a worker goroutine. Submit blocks
// when all workers are busy; this gives natural backpressure.
func (p *WorkerPool) Submit(fn func()) {
	p.jobs <- fn
}

// Close stops accepting new jobs and waits for the in-flight ones to finish.
// Safe to call multiple times.
func (p *WorkerPool) Close() {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	close(p.jobs)
	p.closeMu.Unlock()
	p.wg.Wait()
}

// Run forwards to the inner Runner. Implements Runner.
func (p *WorkerPool) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	return p.inner.Run(ctx, name, args, opts)
}

// ensure WorkerPool satisfies Runner at compile time
var _ Runner = (*WorkerPool)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/exec/ -run TestWorkerPool -v 2>&1 | tail -20
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Run the full exec package tests to ensure no regressions**

```bash
go test ./internal/exec/ 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/pn/internal/exec/workerpool.go modules/pn/internal/exec/workerpool_test.go
git commit -m "$(cat <<'EOF'
feat(pn-exec): add WorkerPool for bounded per-repo subprocess fan-out

WorkerPool wraps a Runner with a fixed number of worker goroutines.
Submit() schedules per-repo jobs (nix eval, git remote -v) that run
concurrently up to the worker cap. Run() forwards to the inner Runner
synchronously, so the pool doubles as a drop-in Runner.

Foundation for tc-perh.5.2 topo-graph Discover concurrency model.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Per-repo `nix eval --json --file flake.nix inputs` helper

**Files:**
- Modify: `modules/pn/internal/workspace/discover.go` (private helper only — public Discover() not touched yet)
- Create: `modules/pn/internal/workspace/inputs_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/workspace/inputs_test.go`:

```go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestReadFlakeInputs_NoFlakeFile_ReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := readFlakeInputs(context.Background(), exec.NewFakeRunner(), root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice when flake.nix missing; got %v", got)
	}
}

func TestReadFlakeInputs_ExtractsAllGithubURLs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	runner.AddResponse("nix",
		[]string{"eval", "--json", "--file", filepath.Join(root, "flake.nix"), "inputs"},
		exec.Result{Stdout: []byte(`{
		  "nixpkgs":    { "url": "github:NixOS/nixpkgs/master" },
		  "overlay":    { "url": "github:phillipgreenii/nix-overlay" },
		  "homelab":    { "url": "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git" },
		  "nested":     { "inputs": { "thing": { "url": "github:foo/bar" } } }
		}`)},
		nil)

	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs: %v", err)
	}
	// Result is a map[inputName]url (only top-level inputs; nested url values
	// are returned under their nested key but we keep simple semantics —
	// match the bash by capturing only top-level input names).
	want := map[string]string{
		"nixpkgs": "github:NixOS/nixpkgs/master",
		"overlay": "github:phillipgreenii/nix-overlay",
		"homelab": "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git",
	}
	if len(got) != len(want) {
		t.Fatalf("input count: got %d (%v) want %d (%v)", len(got), got, len(want), want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("inputs[%q] = %q; want %q", k, got[k], v)
		}
	}
}

func TestReadFlakeInputs_NixEvalFailure_ReturnsEmptyWithNoError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	runner := exec.NewFakeRunner()
	// No scripted response -> FakeRunner.Run returns error.
	got, err := readFlakeInputs(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("readFlakeInputs should swallow nix eval failure and continue; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty inputs map on nix eval failure; got %v", got)
	}
}
```

The test reuses the existing `writeFile(t, path, content string)` helper from `workspace_test.go`. No additional imports needed beyond those in the test file template above.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestReadFlakeInputs 2>&1 | tail -10
```

Expected: build failure (`undefined: readFlakeInputs`).

- [ ] **Step 3: Implement `readFlakeInputs`**

Create `modules/pn/internal/workspace/inputs.go`:

```go
package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// readFlakeInputs runs `nix eval --json --file <repoDir>/flake.nix inputs` and
// returns a map of top-level input name -> input URL (the url field on each
// input). Returns an empty map (and no error) when:
//   - flake.nix is missing — the repo isn't a flake host (e.g. a vendored
//     non-flake repo in the workspace).
//   - nix eval fails for any reason — we log nothing (caller may surface a
//     warning); the repo simply contributes no out-edges to the dep graph.
//
// Higher layers turn the input URLs into github slugs via ExtractGithubSlug
// and match them against workspace repos' SlugSets.
func readFlakeInputs(ctx context.Context, runner exec.Runner, repoDir string) (map[string]string, error) {
	flakePath := filepath.Join(repoDir, "flake.nix")
	if _, err := os.Stat(flakePath); err != nil {
		return map[string]string{}, nil
	}
	res, err := runner.Run(ctx, "nix",
		[]string{"eval", "--json", "--file", flakePath, "inputs"},
		exec.RunOptions{})
	if err != nil {
		return map[string]string{}, nil
	}
	// Unmarshal as a top-level object whose values may have a "url" field.
	// We tolerate values that are themselves objects (some inputs are
	// followers-only and may have no url field).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(raw))
	for k, rv := range raw {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rv, &obj); err != nil {
			continue
		}
		var url string
		if rawURL, ok := obj["url"]; ok {
			_ = json.Unmarshal(rawURL, &url)
		}
		if url != "" {
			out[k] = url
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestReadFlakeInputs -v 2>&1 | tail -15
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/inputs.go modules/pn/internal/workspace/inputs_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add readFlakeInputs helper

Runs `nix eval --json --file <repo>/flake.nix inputs` and returns the
top-level input-name -> URL map. Skips repos without flake.nix; swallows
nix eval failures so one broken flake does not abort workspace discovery.

Uses nix eval (not flake.lock) by design: pn is a development tool, and
unrendered input changes must be visible before `nix flake lock` runs.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Parse `git remote -v` output

**Files:**
- Create: `modules/pn/internal/workspace/remotes.go`
- Create: `modules/pn/internal/workspace/remotes_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/workspace/remotes_test.go`:

```go
package workspace

import (
	"context"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestReadGitRemotes_Simple(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"origin\tssh://git@github.com/owner/repo.git (fetch)\n" +
				"origin\tssh://git@github.com/owner/repo.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	want := map[string]string{"origin": "ssh://git@github.com/owner/repo.git"}
	if len(got) != len(want) || got["origin"] != want["origin"] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadGitRemotes_MultipleRemotes(t *testing.T) {
	runner := exec.NewFakeRunner()
	runner.AddResponse("git", []string{"-C", "/repo", "remote", "-v"},
		exec.Result{Stdout: []byte(
			"bitbucket\tgit@bitbucket.org:phillipgreenii/homelab.git (fetch)\n" +
				"bitbucket\tgit@bitbucket.org:phillipgreenii/homelab.git (push)\n" +
				"origin\tssh://git@synfra.twistcone.us:222/twistcone/homelab.git (fetch)\n" +
				"origin\tssh://git@synfra.twistcone.us:222/twistcone/homelab.git (push)\n",
		)},
		nil)
	got, err := readGitRemotes(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatalf("readGitRemotes: %v", err)
	}
	if got["origin"] != "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git" {
		t.Errorf("origin: got %q", got["origin"])
	}
	if got["bitbucket"] != "git@bitbucket.org:phillipgreenii/homelab.git" {
		t.Errorf("bitbucket: got %q", got["bitbucket"])
	}
}

func TestReadGitRemotes_NotARepo_ReturnsEmpty(t *testing.T) {
	runner := exec.NewFakeRunner()
	// No scripted response → FakeRunner returns error.
	got, err := readGitRemotes(context.Background(), runner, "/notarepo")
	if err != nil {
		t.Fatalf("readGitRemotes should swallow git failure; got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map; got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestReadGitRemotes 2>&1 | tail -10
```

Expected: build failure (`undefined: readGitRemotes`).

- [ ] **Step 3: Implement `readGitRemotes`**

Create `modules/pn/internal/workspace/remotes.go`:

```go
package workspace

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// readGitRemotes runs `git -C <repoDir> remote -v` and returns a map
// remote-name -> URL (first fetch entry per name wins; fetch and push URLs
// are assumed to agree, matching git's normal configuration). Returns an
// empty map (no error) when git fails — caller may surface a warning.
func readGitRemotes(ctx context.Context, runner exec.Runner, repoDir string) (map[string]string, error) {
	res, err := runner.Run(ctx, "git",
		[]string{"-C", repoDir, "remote", "-v"},
		exec.RunOptions{})
	if err != nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Format: "<name>\t<url> (fetch|push)"
		nameTab := strings.IndexByte(line, '\t')
		if nameTab < 0 {
			continue
		}
		name := line[:nameTab]
		rest := line[nameTab+1:]
		// Strip the trailing " (fetch)" or " (push)".
		sp := strings.LastIndexByte(rest, ' ')
		if sp < 0 {
			continue
		}
		url := rest[:sp]
		// First fetch entry per name wins.
		if _, exists := out[name]; !exists {
			out[name] = url
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestReadGitRemotes -v 2>&1 | tail -15
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/remotes.go modules/pn/internal/workspace/remotes_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add readGitRemotes parser

Runs `git -C <repo> remote -v` and returns a map remote-name -> URL
(first fetch entry per name wins). Failures (not a repo, git missing)
return an empty map so workspace discovery can continue.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Slug-set sanity check (toml vs git)

**Files:**
- Create: `modules/pn/internal/workspace/sanity.go`
- Create: `modules/pn/internal/workspace/sanity_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/workspace/sanity_test.go`:

```go
package workspace

import "testing"

func TestCheckRemoteAgreement_SingleURL_Matches(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{"origin": "github:o/foo"}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckRemoteAgreement_SingleURL_Disagrees(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{"origin": "github:o/bar"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected disagreement error")
	}
}

func TestCheckRemoteAgreement_Remotes_AllMatch(t *testing.T) {
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
		{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
	}}
	gitRemotes := map[string]string{
		"origin": "github:o/foo",
		"mirror": "https://github.com/o/foo-mirror",
	}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckRemoteAgreement_Remotes_MissingFromGit(t *testing.T) {
	cfg := RepoConfig{Remotes: []Remote{
		{Name: "origin", URL: "github:o/foo"},
		{Name: "mirror", URL: "https://github.com/o/foo-mirror"},
	}}
	gitRemotes := map[string]string{"origin": "github:o/foo"}
	err := checkRemoteAgreement("foo", cfg, gitRemotes)
	if err == nil {
		t.Fatal("expected missing-remote error")
	}
}

func TestCheckRemoteAgreement_ExtraGitRemotes_AreIgnored(t *testing.T) {
	cfg := RepoConfig{URL: "github:o/foo"}
	gitRemotes := map[string]string{
		"origin":      "github:o/foo",
		"personal":    "git@github.com:me/foo.git",
	}
	if err := checkRemoteAgreement("foo", cfg, gitRemotes); err != nil {
		t.Errorf("unexpected error: extra git remotes should be ignored: %v", err)
	}
}

func TestCheckRemoteAgreement_NoGitRemotes_IsTolerated(t *testing.T) {
	// e.g. fresh clone, no remotes yet; readGitRemotes returned empty
	// after a swallowed error. Don't fail discovery on this.
	cfg := RepoConfig{URL: "github:o/foo"}
	if err := checkRemoteAgreement("foo", cfg, map[string]string{}); err != nil {
		t.Errorf("empty git remotes should be tolerated: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestCheckRemoteAgreement 2>&1 | tail -10
```

Expected: build failure (`undefined: checkRemoteAgreement`).

- [ ] **Step 3: Implement `checkRemoteAgreement`**

Create `modules/pn/internal/workspace/sanity.go`:

```go
package workspace

import "fmt"

// checkRemoteAgreement returns nil when every remote declared in the toml
// also exists in git with the same URL. Untracked git remotes (in git but
// not in toml) are tolerated — users may keep personal remotes. An empty
// gitRemotes map is tolerated too (e.g., a fresh clone before remotes are
// set up, or readGitRemotes swallowed a git failure).
func checkRemoteAgreement(repoName string, cfg RepoConfig, gitRemotes map[string]string) error {
	if len(gitRemotes) == 0 {
		return nil
	}
	if len(cfg.Remotes) > 0 {
		for _, rm := range cfg.Remotes {
			got, ok := gitRemotes[rm.Name]
			if !ok {
				return fmt.Errorf("repo %q: toml declares remote %q but git has none with that name", repoName, rm.Name)
			}
			if got != rm.URL {
				return fmt.Errorf("repo %q: remote %q url mismatch — toml=%q git=%q", repoName, rm.Name, rm.URL, got)
			}
		}
		return nil
	}
	// Single-URL form -> implicit origin
	if got, ok := gitRemotes["origin"]; ok {
		if got != cfg.URL {
			return fmt.Errorf("repo %q: origin url mismatch — toml=%q git=%q", repoName, cfg.URL, got)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestCheckRemoteAgreement -v 2>&1 | tail -15
```

Expected: 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/sanity.go modules/pn/internal/workspace/sanity_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add checkRemoteAgreement sanity check

Verifies every remote declared in pn-workspace.toml also exists in the
repo's git config with the same URL. Untracked git remotes (in git but
not in toml) are ignored — users may keep personal remotes. Empty
gitRemotes (fresh clone / swallowed git failure) is tolerated.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Build dep graph (nodes + edges + in-degree)

**Files:**
- Create: `modules/pn/internal/workspace/graph.go`
- Create: `modules/pn/internal/workspace/graph_test.go`

- [ ] **Step 1: Write the failing tests**

Create `modules/pn/internal/workspace/graph_test.go`:

```go
package workspace

import "testing"

func TestBuildGraph_SimpleSingleEdge(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"base":    {URL: "github:o/base"},
		"overlay": {URL: "github:o/overlay"},
	}}
	repoInputs := map[string]map[string]string{
		"overlay": {"phillipgreenii-nix-base": "github:o/base"},
		"base":    {}, // no out-edges
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["base"] != 1 {
		t.Errorf("base in-degree = %d, want 1", g.inDegree["base"])
	}
	if g.inDegree["overlay"] != 0 {
		t.Errorf("overlay in-degree = %d, want 0", g.inDegree["overlay"])
	}
	if !g.edges["overlay"]["base"] {
		t.Error("expected edge overlay -> base")
	}
}

func TestBuildGraph_MultiRemoteIdentity(t *testing.T) {
	// Repo "lib" has two remotes; "consumer-a" uses one, "consumer-b" uses
	// the other. Both should resolve to "lib".
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"lib": {Remotes: []Remote{
			{Name: "origin", URL: "github:o/lib"},
			{Name: "mirror", URL: "https://github.com/o/lib-mirror"},
		}},
		"consumer-a": {URL: "github:o/consumer-a"},
		"consumer-b": {URL: "github:o/consumer-b"},
	}}
	repoInputs := map[string]map[string]string{
		"consumer-a": {"lib": "github:o/lib"},
		"consumer-b": {"lib": "https://github.com/o/lib-mirror"},
		"lib":        {},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["lib"] != 2 {
		t.Errorf("lib in-degree = %d, want 2", g.inDegree["lib"])
	}
	if !g.edges["consumer-a"]["lib"] || !g.edges["consumer-b"]["lib"] {
		t.Error("expected both consumers to point at lib")
	}
}

func TestBuildGraph_SelfEdgeDropped(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"foo": {URL: "github:o/foo"},
	}}
	repoInputs := map[string]map[string]string{
		"foo": {"self": "github:o/foo"},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["foo"] != 0 {
		t.Errorf("self-edge should be dropped; in-degree = %d", g.inDegree["foo"])
	}
}

func TestBuildGraph_AmbiguousSlugSets_Error(t *testing.T) {
	// Two distinct repos with overlapping slug sets — should never happen
	// in practice but surfaces as an error rather than silent pick.
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"a": {URL: "github:o/dup"},
		"b": {Slug: "o/dup"},
	}}
	_, err := buildGraph(cfg, map[string]map[string]string{})
	if err == nil {
		t.Fatal("expected error: two repos share a slug")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestBuildGraph 2>&1 | tail -10
```

Expected: build failure (`undefined: buildGraph` / `undefined: graph`).

- [ ] **Step 3: Implement `buildGraph`**

Create `modules/pn/internal/workspace/graph.go`:

```go
package workspace

import "fmt"

// graph holds the workspace dependency graph.
//
//   edges[from][to] = true  means repo `from` has a flake input pointing at
//                           repo `to` (i.e. `from` depends on `to`).
//   inDegree[name]         = how many other workspace repos depend on this repo.
type graph struct {
	edges    map[string]map[string]bool
	inDegree map[string]int
	// slugOwner maps each slug in any repo's SlugSet to that repo's name.
	// Populated during construction and used for edge matching.
	slugOwner map[string]string
}

// buildGraph constructs the dep graph from the parsed config and a per-repo
// map of inputName -> URL (typically produced by readFlakeInputs across all
// repos). Returns an error when two distinct repos have overlapping slug
// sets (which would be a misconfiguration — two repos cannot share a github
// identity).
func buildGraph(cfg *WorkspaceConfig, repoInputs map[string]map[string]string) (*graph, error) {
	g := &graph{
		edges:     make(map[string]map[string]bool),
		inDegree:  make(map[string]int),
		slugOwner: make(map[string]string),
	}
	// Initialize one entry per repo so even repos with no edges show up.
	for name := range cfg.Repos {
		g.edges[name] = make(map[string]bool)
		g.inDegree[name] = 0
	}
	// Populate slugOwner. Reject overlaps.
	for name, rc := range cfg.Repos {
		for slug := range SlugSet(rc) {
			if owner, exists := g.slugOwner[slug]; exists && owner != name {
				return nil, fmt.Errorf("slug %q claimed by both %q and %q", slug, owner, name)
			}
			g.slugOwner[slug] = name
		}
	}
	// Add edges.
	for from, inputs := range repoInputs {
		for _, url := range inputs {
			slug := ExtractGithubSlug(url)
			if slug == "" {
				continue
			}
			to, ok := g.slugOwner[slug]
			if !ok || to == from {
				continue
			}
			if !g.edges[from][to] {
				g.edges[from][to] = true
				g.inDegree[to]++
			}
		}
	}
	return g, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestBuildGraph -v 2>&1 | tail -15
```

Expected: 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/graph.go modules/pn/internal/workspace/graph_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add dep-graph builder

buildGraph turns per-repo flake-input maps + the parsed config into a
directed graph with in-degrees. Multi-remote repos resolve correctly:
any input URL whose slug is in some repo's SlugSet matches that repo.
Self-edges are dropped silently; overlapping slug sets across distinct
repos surface as a hard error.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Terminal selection

**Files:**
- Modify: `modules/pn/internal/workspace/graph.go` (add `selectTerminal`)
- Modify: `modules/pn/internal/workspace/graph_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `modules/pn/internal/workspace/graph_test.go`:

```go
func TestSelectTerminal_SingleCandidate(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"base":     1,
		"overlay":  0,
	}}
	cfg := &WorkspaceConfig{}
	got, err := selectTerminal(cfg, g)
	if err != nil {
		t.Fatalf("selectTerminal: %v", err)
	}
	if got != "overlay" {
		t.Errorf("got %q, want overlay", got)
	}
}

func TestSelectTerminal_ExplicitInToml(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"base":     1,
		"overlay":  0,
		"personal": 0,
	}}
	cfg := &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "personal"}}
	got, err := selectTerminal(cfg, g)
	if err != nil {
		t.Fatalf("selectTerminal: %v", err)
	}
	if got != "personal" {
		t.Errorf("got %q, want personal", got)
	}
}

func TestSelectTerminal_AmbiguousNoToml_Error(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"a": 0,
		"b": 0,
	}}
	cfg := &WorkspaceConfig{}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: multiple terminal candidates without explicit terminal")
	}
}

func TestSelectTerminal_ExplicitTerminalIsDependedOn_Error(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"a": 1, // depended on
		"b": 0,
	}}
	cfg := &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "a"}}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: explicit terminal has in-degree > 0")
	}
}

func TestSelectTerminal_Cycle_Error(t *testing.T) {
	// All repos have in-degree > 0 -> cycle.
	g := &graph{inDegree: map[string]int{
		"a": 1,
		"b": 1,
	}}
	cfg := &WorkspaceConfig{}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: dependency cycle")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestSelectTerminal 2>&1 | tail -10
```

Expected: build failure (`undefined: selectTerminal`).

- [ ] **Step 3: Implement `selectTerminal`**

Append to `modules/pn/internal/workspace/graph.go`:

```go
import "sort"

// selectTerminal picks the terminal repo per design §9. Inputs:
//   - cfg.Workspace.Terminal (optional explicit pick)
//   - g.inDegree           (computed by buildGraph)
//
// Behavior:
//   1. Compute the set of candidates (in-degree == 0).
//   2. If cfg.Workspace.Terminal is set:
//      - it must be in candidates (in-degree 0); else error.
//      - it must exist in inDegree (graph node); else error.
//      Return it.
//   3. If exactly one candidate, return it.
//   4. If multiple candidates and no explicit terminal, return error with
//      candidate list — user must set [workspace].terminal.
//   5. If zero candidates, the graph has a cycle — return error.
func selectTerminal(cfg *WorkspaceConfig, g *graph) (string, error) {
	candidates := make([]string, 0, len(g.inDegree))
	for name, d := range g.inDegree {
		if d == 0 {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)

	if explicit := cfg.Workspace.Terminal; explicit != "" {
		if _, exists := g.inDegree[explicit]; !exists {
			return "", fmt.Errorf("workspace.terminal %q is not a graph node (no flake.nix?)", explicit)
		}
		if g.inDegree[explicit] > 0 {
			return "", fmt.Errorf("workspace.terminal %q has in-degree %d; cannot be a terminal", explicit, g.inDegree[explicit])
		}
		return explicit, nil
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("dependency cycle: no repo has in-degree 0")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple terminal candidates (%v); set [workspace].terminal in pn-workspace.toml", candidates)
	}
}
```

(Note: `sort` import goes at the top of the file with the existing imports. The current file has only `fmt`; add `sort`.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestSelectTerminal -v 2>&1 | tail -15
```

Expected: 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/graph.go modules/pn/internal/workspace/graph_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add terminal selection

Per design §9: prefer explicit [workspace].terminal; fall back to the
single in-degree-0 candidate; error when ambiguous (multiple candidates
without explicit pick), invalid (explicit terminal is depended on), or
cyclic (no zero-in-degree vertex).

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: inputName resolution

**Files:**
- Modify: `modules/pn/internal/workspace/graph.go` (add `resolveInputNames`)
- Modify: `modules/pn/internal/workspace/graph_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `modules/pn/internal/workspace/graph_test.go`:

```go
func TestResolveInputNames_Simple(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"base":     {URL: "github:o/base"},
		"overlay":  {URL: "github:o/overlay"},
		"personal": {URL: "github:o/personal"},
	}}
	repoInputs := map[string]map[string]string{
		"personal": {
			"phillipgreenii-nix-base":    "github:o/base",
			"phillipgreenii-nix-overlay": "github:o/overlay",
		},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatal(err)
	}
	names, err := resolveInputNames(cfg, g, repoInputs, "personal")
	if err != nil {
		t.Fatalf("resolveInputNames: %v", err)
	}
	if names["base"] != "phillipgreenii-nix-base" {
		t.Errorf("base inputName = %q", names["base"])
	}
	if names["overlay"] != "phillipgreenii-nix-overlay" {
		t.Errorf("overlay inputName = %q", names["overlay"])
	}
}

func TestResolveInputNames_SiblingNotConsumed_Empty(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"base":     {URL: "github:o/base"},
		"personal": {URL: "github:o/personal"},
		"sibling":  {URL: "github:o/sibling"}, // not depended on by personal
	}}
	repoInputs := map[string]map[string]string{
		"personal": {"phillipgreenii-nix-base": "github:o/base"},
		"base":     {},
		"sibling":  {},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatal(err)
	}
	names, err := resolveInputNames(cfg, g, repoInputs, "personal")
	if err != nil {
		t.Fatalf("resolveInputNames: %v", err)
	}
	if got, ok := names["sibling"]; ok && got != "" {
		t.Errorf("sibling should be empty/missing; got %q", got)
	}
}

func TestResolveInputNames_MultipleMatches_Error(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"base":     {URL: "github:o/base"},
		"personal": {URL: "github:o/personal"},
	}}
	repoInputs := map[string]map[string]string{
		"personal": {
			"name-one": "github:o/base",
			"name-two": "github:o/base", // same target twice — illegal
		},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveInputNames(cfg, g, repoInputs, "personal")
	if err == nil {
		t.Fatal("expected error: terminal has two inputs pointing at the same repo")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestResolveInputNames 2>&1 | tail -10
```

Expected: build failure (`undefined: resolveInputNames`).

- [ ] **Step 3: Implement `resolveInputNames`**

Append to `modules/pn/internal/workspace/graph.go`:

```go
// resolveInputNames returns a map repoName -> inputName, where the input
// name is what the terminal flake calls each non-terminal workspace repo
// among its inputs. Repos not consumed by the terminal are absent from the
// returned map (callers should treat absent == empty inputName).
//
// Errors when the terminal has more than one input pointing at the same
// workspace repo — this would mean the user has two distinct inputs that
// both resolve to the same on-disk clone, which is a configuration mistake
// pn cannot silently disambiguate.
func resolveInputNames(cfg *WorkspaceConfig, g *graph, repoInputs map[string]map[string]string, terminal string) (map[string]string, error) {
	termInputs := repoInputs[terminal]
	out := make(map[string]string)
	for inputName, url := range termInputs {
		slug := ExtractGithubSlug(url)
		if slug == "" {
			continue
		}
		repo, ok := g.slugOwner[slug]
		if !ok || repo == terminal {
			continue
		}
		if existing, dup := out[repo]; dup {
			return nil, fmt.Errorf("terminal %q has multiple inputs pointing at repo %q: %q and %q",
				terminal, repo, existing, inputName)
		}
		out[repo] = inputName
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestResolveInputNames -v 2>&1 | tail -15
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/graph.go modules/pn/internal/workspace/graph_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add inputName resolution against the terminal flake

resolveInputNames maps each non-terminal workspace repo to the input
name the terminal flake uses for it. Repos not consumed by the terminal
are absent from the result; duplicate inputs pointing at the same repo
surface as a configuration error.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Topological sort (Kahn's algorithm)

**Files:**
- Modify: `modules/pn/internal/workspace/graph.go` (add `topoSort`)
- Modify: `modules/pn/internal/workspace/graph_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `modules/pn/internal/workspace/graph_test.go`:

```go
func TestTopoSort_DepsFirstTerminalLast(t *testing.T) {
	// overlay -> base ; personal -> overlay, personal -> base
	g := &graph{
		edges: map[string]map[string]bool{
			"overlay":  {"base": true},
			"personal": {"base": true, "overlay": true},
			"base":     {},
		},
		inDegree: map[string]int{
			"base":     2,
			"overlay":  1,
			"personal": 0,
		},
	}
	order, err := topoSort(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 {
		t.Fatalf("len(order)=%d, want 3", len(order))
	}
	// "base" must come before "overlay"; both before "personal".
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos["base"] >= pos["overlay"] {
		t.Errorf("base must come before overlay; order=%v", order)
	}
	if pos["overlay"] >= pos["personal"] {
		t.Errorf("overlay must come before personal; order=%v", order)
	}
	if pos["base"] >= pos["personal"] {
		t.Errorf("base must come before personal; order=%v", order)
	}
}

func TestTopoSort_StableByNameWithinLevel(t *testing.T) {
	// Three repos with no edges between them — all at level 0. Should
	// emerge sorted alphabetically.
	g := &graph{
		edges:    map[string]map[string]bool{"a": {}, "b": {}, "c": {}},
		inDegree: map[string]int{"a": 0, "b": 0, "c": 0},
	}
	order, err := topoSort(g)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	for i, n := range order {
		if n != want[i] {
			t.Errorf("order[%d]=%q want %q (full: %v)", i, n, want[i], order)
		}
	}
}

func TestTopoSort_Cycle_Error(t *testing.T) {
	// a -> b -> a
	g := &graph{
		edges: map[string]map[string]bool{
			"a": {"b": true},
			"b": {"a": true},
		},
		inDegree: map[string]int{"a": 1, "b": 1},
	}
	_, err := topoSort(g)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/workspace/ -run TestTopoSort 2>&1 | tail -10
```

Expected: build failure (`undefined: topoSort`).

- [ ] **Step 3: Implement `topoSort`**

Append to `modules/pn/internal/workspace/graph.go`:

```go
// topoSort returns repos in Kahn-topological order — dependencies first,
// terminal last. Within each "level" (set of nodes whose remaining
// in-degree dropped to 0 in the same iteration), the order is stable
// alphabetical for determinism.
//
// Returns an error when the graph has a cycle (some node never reaches
// in-degree 0).
func topoSort(g *graph) ([]string, error) {
	// Local in-degree copy so we can mutate.
	deg := make(map[string]int, len(g.inDegree))
	for n, d := range g.inDegree {
		deg[n] = d
	}
	out := make([]string, 0, len(deg))
	for len(out) < len(g.inDegree) {
		// Collect all zero-in-degree nodes for this level; sort
		// alphabetically; emit them in that order.
		level := make([]string, 0)
		for n, d := range deg {
			if d == 0 {
				level = append(level, n)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("dependency cycle: cannot topologically sort remaining repos: %v", remaining(deg))
		}
		sort.Strings(level)
		for _, n := range level {
			out = append(out, n)
			delete(deg, n)
			// Decrement in-degree of every node n points at.
			for to := range g.edges[n] {
				if _, present := deg[to]; present {
					deg[to]--
				}
			}
		}
	}
	return out, nil
}

func remaining(deg map[string]int) []string {
	r := make([]string, 0, len(deg))
	for n := range deg {
		r = append(r, n)
	}
	sort.Strings(r)
	return r
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/workspace/ -run TestTopoSort -v 2>&1 | tail -15
```

Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/graph.go modules/pn/internal/workspace/graph_test.go
git commit -m "$(cat <<'EOF'
feat(pn): add topological sort (Kahn's algorithm)

topoSort returns repos in deps-first, terminal-last order. Stable by
repo name within each level so test fixtures and pn output are
deterministic. Cycles surface as a clear error naming the unresolved
repos.

Foundation for tc-perh.5.2 topo-graph Discover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Wire `Discover()` to call all of the above

**Files:**
- Modify: `modules/pn/internal/workspace/workspace.go` (add pool, plumb through Open)
- Modify: `modules/pn/internal/workspace/discover.go` (rewrite `Discover`)
- Modify: `modules/pn/internal/workspace/discover_test.go` (replace old tests + add new)

This is the largest task. Keep the steps small.

- [ ] **Step 1: Update `Repo` struct and add a worker pool to `Workspace`**

In `modules/pn/internal/workspace/workspace.go`, change:

```go
type Workspace struct {
	root   string
	config *WorkspaceConfig
	lock   *Lock
	runner exec.Runner
}
```

to:

```go
type Workspace struct {
	root   string
	config *WorkspaceConfig
	lock   *Lock
	runner exec.Runner
	pool   *exec.WorkerPool
}
```

And update `Open` to construct the pool (just append two lines at the end before the return):

```go
import "runtime"

func Open(dir string, runner exec.Runner) (*Workspace, error) {
	cfgPath := filepath.Join(dir, ConfigFileName)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, err
	}
	lock, err := ReadLock(filepath.Join(dir, LockFileName))
	if err != nil {
		return nil, err
	}
	pool := exec.NewWorkerPool(runner, runtime.NumCPU())
	return &Workspace{
		root:   dir,
		config: cfg,
		lock:   lock,
		runner: runner,
		pool:   pool,
	}, nil
}
```

Also add a `Close` method:

```go
// Close releases the workspace's resources (worker pool, etc.).
// Safe to call multiple times.
func (w *Workspace) Close() {
	if w.pool != nil {
		w.pool.Close()
	}
}
```

- [ ] **Step 2: Run the existing workspace tests to confirm no regressions**

```bash
cd /home/tcadmin/workspace/nix-repo-base/modules/pn
go test ./internal/workspace/ 2>&1 | tail -5
```

Expected: PASS. (Existing tests should still pass — the new pool field doesn't change observable behavior yet.)

- [ ] **Step 3: Rewrite `Repo` struct and `Discover`**

Replace the entire contents of `modules/pn/internal/workspace/discover.go` with:

```go
package workspace

import (
	"context"
	"path/filepath"
	"sync"
)

// Repo is one workspace repo entry as surfaced by Discover.
type Repo struct {
	Name       string
	URL        string // canonical URL (origin in the multi-remote form)
	Path       string
	InputName  string // empty for the terminal repo and for siblings not consumed by the terminal
	IsTerminal bool
}

// Discover returns the workspace's repos in topological order (dependencies
// first, terminal last). Each repo is enriched with InputName (the name the
// terminal flake uses for that input) and IsTerminal.
//
// Discover performs per-repo subprocess fan-out (nix eval + git remote -v)
// in parallel via the workspace's worker pool. Per-repo failures are tolerated
// (the repo simply contributes no out-edges); errors that prevent graph
// construction (slug conflicts, terminal ambiguity, cycles) are returned.
func (ws *Workspace) Discover() ([]Repo, error) {
	ctx := context.Background()
	names := orderedRepoNames(ws.config.Repos)
	repoInputs := make(map[string]map[string]string, len(names))
	gitRemotesByRepo := make(map[string]map[string]string, len(names))
	errsByRepo := make(map[string]error, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range names {
		n := n
		repoDir := filepath.Join(ws.root, n)
		wg.Add(1)
		ws.pool.Submit(func() {
			defer wg.Done()
			inputs, _ := readFlakeInputs(ctx, ws.runner, repoDir)
			remotes, _ := readGitRemotes(ctx, ws.runner, repoDir)
			mu.Lock()
			repoInputs[n] = inputs
			gitRemotesByRepo[n] = remotes
			mu.Unlock()
		})
	}
	wg.Wait()

	// Slug-set agreement check (per repo, sequential — fast in-memory)
	for _, n := range names {
		if err := checkRemoteAgreement(n, ws.config.Repos[n], gitRemotesByRepo[n]); err != nil {
			errsByRepo[n] = err
		}
	}
	for _, n := range names {
		if e := errsByRepo[n]; e != nil {
			return nil, e
		}
	}

	g, err := buildGraph(ws.config, repoInputs)
	if err != nil {
		return nil, err
	}
	terminal, err := selectTerminal(ws.config, g)
	if err != nil {
		return nil, err
	}
	inputNames, err := resolveInputNames(ws.config, g, repoInputs, terminal)
	if err != nil {
		return nil, err
	}
	order, err := topoSort(g)
	if err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(order))
	for _, name := range order {
		out = append(out, Repo{
			Name:       name,
			URL:        canonicalURL(ws.config.Repos[name]),
			Path:       filepath.Join(ws.root, name),
			InputName:  inputNames[name],
			IsTerminal: name == terminal,
		})
	}
	return out, nil
}

// canonicalURL returns one URL string for display purposes:
//   - If the toml uses the single-url form, return that URL.
//   - Else (multi-remote form), return the origin remote's URL when one
//     exists, otherwise the first remote's URL.
func canonicalURL(r RepoConfig) string {
	if r.URL != "" {
		return r.URL
	}
	for _, rm := range r.Remotes {
		if rm.Name == "origin" {
			return rm.URL
		}
	}
	if len(r.Remotes) > 0 {
		return r.Remotes[0].URL
	}
	return ""
}
```

This signature change — `Discover() []Repo` → `Discover() ([]Repo, error)` — propagates. The CLI caller in `internal/cli/workspace.go` must be updated too.

- [ ] **Step 4: Update the CLI caller**

In `modules/pn/internal/cli/workspace.go`, find:

```go
func workspaceDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			repos := w.Discover()
			out := cmd.OutOrStdout()
			for _, r := range repos {
				fmt.Fprintf(out, "%s\t%s\t%s\n", r.Name, r.URL, r.Path)
			}
			return nil
		},
	}
}
```

and change it to:

```go
func workspaceDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List workspace repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := openWorkspace()
			if err != nil {
				return err
			}
			defer w.Close()
			repos, err := w.Discover()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, r := range repos {
				fmt.Fprintf(out, "%s\t%s\t%s\n", r.Name, r.URL, r.Path)
			}
			return nil
		},
	}
}
```

- [ ] **Step 5: Update existing `Discover` callers in the workspace package**

In `modules/pn/internal/workspace/status.go`, `tree.go`, `build.go`, `apply.go`, `flake_check.go`, the existing code uses `orderedRepoNames(ws.config.Repos)` directly — they iterate the config, not the new `Discover()`. They still work. Confirm:

```bash
grep -n "ws\.Discover\(\)" modules/pn/internal/workspace/*.go
```

Expected: only `discover.go` itself defines `Discover`. No other callers in the package. So step 5 is a no-op — proceed.

- [ ] **Step 6: Replace the old `discover_test.go`**

Replace `modules/pn/internal/workspace/discover_test.go` with:

```go
package workspace

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// helper: build a workspace whose runner is pre-scripted with the per-repo
// nix eval + git remote responses.
func newTestWorkspace(t *testing.T, configToml string, perRepo map[string]struct {
	flakeInputs string // raw JSON; empty -> nix eval not scripted (FakeRunner returns err)
	gitRemotes  string // raw `git remote -v` output; empty -> no remotes
	createFlake bool   // whether to create flake.nix on disk (gates the inputs lookup)
}) *Workspace {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), configToml)
	runner := exec.NewFakeRunner()
	for repoName, fixture := range perRepo {
		repoDir := filepath.Join(root, repoName)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if fixture.createFlake {
			writeFile(t, filepath.Join(repoDir, "flake.nix"), "{}")
			runner.AddResponse("nix",
				[]string{"eval", "--json", "--file", filepath.Join(repoDir, "flake.nix"), "inputs"},
				exec.Result{Stdout: []byte(fixture.flakeInputs)},
				nil)
		}
		runner.AddResponse("git",
			[]string{"-C", repoDir, "remote", "-v"},
			exec.Result{Stdout: []byte(fixture.gitRemotes)},
			nil)
	}
	w, err := Open(root, runner)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestDiscover_SimpleDep_OrderAndInputName(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "personal"

[repos.base]
url = "github:o/base"

[repos.personal]
url = "github:o/personal"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"base": {
			flakeInputs: `{}`,
			gitRemotes:  "origin\tgithub:o/base (fetch)\norigin\tgithub:o/base (push)\n",
			createFlake: true,
		},
		"personal": {
			flakeInputs: `{"upstream-base": {"url": "github:o/base"}}`,
			gitRemotes:  "origin\tgithub:o/personal (fetch)\norigin\tgithub:o/personal (push)\n",
			createFlake: true,
		},
	})
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
	}
	if repos[0].Name != "base" {
		t.Errorf("first repo = %q, want base", repos[0].Name)
	}
	if !repos[1].IsTerminal {
		t.Errorf("last repo (personal) should be terminal")
	}
	if repos[0].InputName != "upstream-base" {
		t.Errorf("base inputName = %q, want upstream-base", repos[0].InputName)
	}
	if repos[1].InputName != "" {
		t.Errorf("personal (terminal) InputName should be empty; got %q", repos[1].InputName)
	}
}

func TestDiscover_MultiRemoteIdentity(t *testing.T) {
	cfg := `
[workspace]
name = "test"
terminal = "personal"

[repos.lib]
remotes = [
  { name = "origin", url = "github:o/lib" },
  { name = "mirror", url = "https://github.com/o/lib-mirror" },
]

[repos.personal]
url = "github:o/personal"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"lib": {
			flakeInputs: `{}`,
			gitRemotes:  "mirror\thttps://github.com/o/lib-mirror (fetch)\nmirror\thttps://github.com/o/lib-mirror (push)\norigin\tgithub:o/lib (fetch)\norigin\tgithub:o/lib (push)\n",
			createFlake: true,
		},
		"personal": {
			// personal uses the MIRROR url, not origin
			flakeInputs: `{"my-lib": {"url": "https://github.com/o/lib-mirror"}}`,
			gitRemotes:  "origin\tgithub:o/personal (fetch)\norigin\tgithub:o/personal (push)\n",
			createFlake: true,
		},
	})
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// "personal" should be terminal; "lib" should be first with inputName "my-lib".
	if repos[0].Name != "lib" || repos[0].InputName != "my-lib" {
		t.Errorf("lib: %+v", repos[0])
	}
	if !repos[1].IsTerminal {
		t.Errorf("personal should be terminal: %+v", repos[1])
	}
}

func TestDiscover_RemoteAgreementFailure(t *testing.T) {
	cfg := `
[workspace]
name = "test"

[repos.foo]
url = "github:o/foo"
`
	w := newTestWorkspace(t, cfg, map[string]struct {
		flakeInputs string
		gitRemotes  string
		createFlake bool
	}{
		"foo": {
			gitRemotes:  "origin\tgithub:o/SOMETHING-ELSE (fetch)\norigin\tgithub:o/SOMETHING-ELSE (push)\n",
			createFlake: false,
		},
	})
	_, err := w.Discover()
	if err == nil {
		t.Fatal("expected remote-agreement error")
	}
}

func TestDiscover_EmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "empty"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	repos, err := w.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected empty repo list; got %v", repos)
	}
}
```

Add the missing imports at the top of the file:

```go
import "os"
```

- [ ] **Step 7: Run all discover tests**

```bash
go test ./internal/workspace/ -run TestDiscover -v 2>&1 | tail -25
```

Expected: 4 tests PASS.

- [ ] **Step 8: Run the full workspace package tests**

```bash
go test ./internal/workspace/ 2>&1 | tail -5
```

Expected: PASS. All earlier tasks' tests still green.

- [ ] **Step 9: Build pn end-to-end via nix and smoke-test against the real workspace**

Stage the changes so the flake-local source sees them, then build:

```bash
cd /home/tcadmin/workspace/nix-repo-base
git add modules/pn/internal/exec/workerpool.go modules/pn/internal/exec/workerpool_test.go \
        modules/pn/internal/workspace/slug.go modules/pn/internal/workspace/slug_test.go \
        modules/pn/internal/workspace/inputs.go modules/pn/internal/workspace/inputs_test.go \
        modules/pn/internal/workspace/remotes.go modules/pn/internal/workspace/remotes_test.go \
        modules/pn/internal/workspace/sanity.go modules/pn/internal/workspace/sanity_test.go \
        modules/pn/internal/workspace/graph.go modules/pn/internal/workspace/graph_test.go \
        modules/pn/internal/workspace/discover.go modules/pn/internal/workspace/discover_test.go \
        modules/pn/internal/workspace/workspace.go \
        modules/pn/internal/cli/workspace.go
nix build .#pn 2>&1 | tail -3
./result/bin/pn --version
```

Update `/home/tcadmin/workspace/pn-workspace.toml` to add `terminal = "nix-personal"` under `[workspace]` (otherwise discovery will error on multiple terminal candidates now that we no longer alphabetical-pick):

Read the file first, then edit. Expected new contents (preserving the existing header comment):

```toml
# pn-workspace.toml — workspace root, machine-local
[workspace]
name = "phillipgreenii"
description = "phillipgreenii's nix workspace (4 nix-* repos)"
terminal = "nix-personal"

[repos.nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"

[repos.nix-overlay]
url = "github:phillipgreenii/nix-overlay"

[repos.nix-personal]
url = "github:phillipgreenii/nix-personal"

[repos.nix-agent-support]
url = "github:phillipgreenii/nix-agent-support"
```

Then smoke-test:

```bash
cd /home/tcadmin/workspace
./nix-repo-base/result/bin/pn workspace discover 2>&1
```

Expected: 4 repos printed in topological order. `nix-repo-base` first (it has no workspace inputs of its own), then `nix-overlay` (depends on nix-repo-base), then `nix-personal` last (the terminal). `nix-agent-support` will appear before or after `nix-overlay` depending on whether its flake uses `nix-overlay` as an input.

If `nix eval` is too slow, the test should still succeed (we tolerate eval failures by treating the repo as having no out-edges).

- [ ] **Step 10: Commit (this is the biggest commit of the plan; everything previously committed plus this glue)**

```bash
git commit -m "$(cat <<'EOF'
feat(pn): topology-aware Discover with multi-remote + worker pool

Replaces alphabetical flat Discover() with a graph-aware implementation
that returns Repo{InputName, IsTerminal} in dep-first / terminal-last
order. Per-repo nix eval and git remote -v run concurrently through a
shared worker pool (sized to runtime.NumCPU()).

Multi-remote repos are first-class: a sibling input URL matching any of
a repo's declared remotes resolves to that repo. Terminal selection is
explicit via [workspace].terminal — no alphabetical guesswork.

Closes the discover.go:17 TODO from tc-perh.5. Foundation for the
build.go / apply.go / flake_check.go follow-ups in tc-perh.5.2.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Update /home/tcadmin/workspace/pn-workspace.toml + re-run Phase A discover smoke test from a subdir

**Files:**
- Modify: `/home/tcadmin/workspace/pn-workspace.toml` (workspace root, NOT nix-repo-base)

This is the integration check — confirming the new `Discover` works against the actual nix-* workspace. The toml needs the new `terminal` field.

- [ ] **Step 1: Confirm the toml has `terminal = "nix-personal"`**

This was done in Task 12 step 9. Verify:

```bash
grep -n "^terminal" /home/tcadmin/workspace/pn-workspace.toml
```

Expected: one line `terminal = "nix-personal"` under `[workspace]`.

- [ ] **Step 2: Run `pn workspace discover` from the workspace root**

```bash
cd /home/tcadmin/workspace
/home/tcadmin/workspace/nix-repo-base/result/bin/pn workspace discover
```

Expected: 4 lines, topologically ordered. `nix-personal` is last.

- [ ] **Step 3: Run `pn workspace discover` from a subdir to confirm walk-up still works**

```bash
cd /home/tcadmin/workspace/nix-personal/home
/home/tcadmin/workspace/nix-repo-base/result/bin/pn workspace discover
```

Expected: same 4 lines as step 2.

- [ ] **Step 4: Confirm tree and status still work**

```bash
cd /home/tcadmin/workspace
/home/tcadmin/workspace/nix-repo-base/result/bin/pn workspace tree
/home/tcadmin/workspace/nix-repo-base/result/bin/pn workspace status
```

Expected: tree prints a flat list in topological order (the new ordering propagates because `tree.go` calls `orderedRepoNames`, which is independent of the new Discover — so tree may stay alphabetical; if that bothers anyone, fix in a follow-up). Status runs `git status --short` per repo and prints (clean) or untracked entries.

- [ ] **Step 5: No commit (just integration smoke test).**

---

### Task 14: Post a comment on tc-perh.5.2 with the foundation-landed summary

**Files:** none (beads operation)

- [ ] **Step 1: Post comment**

```bash
bd comments add tc-perh.5.2 -f - <<'EOF'
**Foundation landed.** The discover.go:17 TODO is now closed: Discover() returns repos in topological order with InputName and IsTerminal populated, and the new TOML schema supports multi-remote repos + explicit [workspace].terminal.

Commits (in nix-repo-base, on main):
- task 1: extend RepoConfig with Remotes/Slug; add WorkspaceSection.Terminal
- task 2: add ExtractGithubSlug regex menu
- task 3: add CanonicalSlug and SlugSet derivation
- task 4: add WorkerPool for bounded per-repo subprocess fan-out
- task 5: add readFlakeInputs helper
- task 6: add readGitRemotes parser
- task 7: add checkRemoteAgreement sanity check
- task 8: add dep-graph builder
- task 9: add terminal selection
- task 10: add inputName resolution
- task 11: add topological sort
- task 12: topology-aware Discover with multi-remote + worker pool

Verified against the real /home/tcadmin/workspace/ (4 nix-* repos):
- `pn workspace discover` prints repos in dep-first order with nix-personal last.
- Smoke-tested from a subdir to confirm openWorkspace walk-up still works.
- /home/tcadmin/workspace/pn-workspace.toml now includes terminal = "nix-personal".

**Still open** (the remaining tc-perh.5.2 medium-impact tasks; now trivial after this foundation):
- build.go: pick terminal flake, run only there, emit real --override-input <inputName> path:<dir>
- apply.go: same as build for apply
- flake_check.go: use computeOverrideArgs(ws) extended for the new InputName field
- nix.go: deny-list for --override-input-incompatible subcommands
- update.go: signal handling + lock regeneration
EOF
```

- [ ] **Step 2: Done.** The foundation deliverable for tc-perh.5.2 is complete. Whether to close tc-perh.5.2 now or leave it open for the remaining medium-impact items is a follow-up call.

---

## Out-of-scope for this plan

Per spec §17, these are deferred:

- `internal/workspace/build.go` — consume `Discover()`'s new `InputName`/`IsTerminal` to build only the terminal flake with proper overrides.
- `internal/workspace/apply.go` — same as build.
- `internal/workspace/flake_check.go` — extend `computeOverrideArgs` to use the new `InputName` field instead of repo name, then call it from FlakeCheck.
- `internal/workspace/nix.go` — deny-list for subcommands incompatible with `--override-input`.
- `internal/workspace/update.go` — signal handling, regenerate `pn-workspace.lock` after pulling.
- `internal/workspace/tree.go` — ASCII dep-graph renderer.
- `pn-workspace.lock` schema for the new Repo fields.

Each of these is a small, independent follow-up after this plan lands.
