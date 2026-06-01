# Design: Topological-graph `Discover()` with multi-remote repo identity

**Status:** Proposed
**Created:** 2026-06-01
**Bead:** tc-perh.5.2

## 0. Summary

Replace `Workspace.Discover()`'s alphabetical flat listing with a graph-aware
discovery that returns each repo enriched with its **flake input name** (as the
terminal flake sees it) and an **IsTerminal** flag, in topological order
(dependencies first, terminal last). This is the foundation for `pn workspace
build`, `apply`, and `flake-check` to behave like their deleted bash
predecessors — building only the terminal flake with correct
`--override-input` flags pinning every dependency to its local clone.

The bash predecessor lived in `pn-discover-workspace.sh`; this design ports
that logic into the Go `workspace` package with three deliberate
improvements:

1. **Multi-remote repos are supported.** A workspace repo may have multiple
   git remotes (e.g. `origin` on Forgejo and `bitbucket` as a mirror). A
   sibling flake input that references _any_ of those remote URLs resolves
   correctly.
2. **Terminal selection is explicit when ambiguous**, not a silent
   alphabetical tiebreak.
3. **Per-repo subprocesses (`nix eval`, `git remote -v`) run concurrently**
   through a shared worker pool, exploiting Go's strength over the
   single-threaded bash version.

## 1. Motivation

`pn workspace build` and friends are unusable today against a real
multi-repo workspace. The Go port runs `nix build .` in every repo
alphabetically, which fails on the first non-terminal flake (no
`packages.<system>.default`). The bash version solved this by picking a
single terminal flake, building only it, and injecting
`--override-input <name> git+file://<path>` for every locally-cloned
dependency. That logic depends on a topological discovery step that the Go
port does not yet have — only an alphabetical `Discover()`.

The bead tc-perh.5.1 surfaced this gap during Phase A verification: build
and flake-check both fail on `nix-agent-support` (the alphabetically-first
non-terminal repo). The work-around must be at the discovery layer because
all three consuming verbs (`build`, `apply`, `flake-check`) need the same
data.

## 2. Goals

- `Discover()` returns repos in topological order with `InputName` and
  `IsTerminal` populated for callers that need them.
- The dep graph correctly handles repos that publish multiple remote URLs
  (e.g. an `origin` on Forgejo and a `bitbucket` mirror, both legitimate
  identities of the same repo).
- Terminal selection has explicit, deterministic, user-controlled
  semantics — no alphabetical tiebreak.
- All subprocess fan-out (`nix eval`, `git remote -v`) runs concurrently
  through a shared, bounded worker pool.
- Existing tests in `discover_test.go` continue to pass; new behaviors are
  covered by new tests.

## 3. Non-goals

- Consuming the new `Discover()` output in `build.go`, `apply.go`, or
  `flake_check.go`. Those are tc-perh.5.2 follow-up tasks; they become
  trivial once this foundation lands.
- Updating `tree.go` to render the full dep-graph. Cosmetic; deferred.
- Auto-detecting the terminal flake from `packages.<system>.default`
  presence. Heuristics are out — explicit terminal selection is the
  contract.
- Caching `nix eval` output across pn invocations.

## 4. TOML schema extensions

Back-compat preserved: existing single-`url` configs work unchanged.

```toml
[workspace]
name = "phillipgreenii"
description = "phillipgreenii's nix workspace (4 nix-* repos)"

# NEW: explicit terminal repo. REQUIRED when the dep graph yields more than
# one repo with zero in-edges. Authoritative even when only one candidate
# exists; in that case it is a sanity check (mismatched value -> error).
terminal = "nix-personal"

# Single-URL form (today's form; equivalent to a single remote named "origin"):
[repos.nix-overlay]
url = "github:phillipgreenii/nix-overlay"
branch = "main"

# Multi-remote form (NEW):
[repos.homelab]
remotes = [
  { name = "origin",    url = "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git" },
  { name = "bitbucket", url = "git@bitbucket.org:phillipgreenii/homelab.git" },
]

# Explicit slug override (NEW, optional). When set, this is the canonical slug
# regardless of URL parsing.
[repos.weirdname]
url = "github:owner/repo-with-different-slug"
slug = "owner/canonical"
```

**Validation rules (parse time):**

- `url` and `remotes` are mutually exclusive per repo — setting both is a
  parse error.
- If `remotes` is set, at most one entry may be named `origin`. Empty
  `remotes` is an error (use `url` instead).
- `slug` is opaque; pn does not validate its format beyond non-empty.
- `[workspace].terminal`, if set, must name a repo declared in `[repos.*]`.
  Cross-check happens after parse but before graph construction.

**Go representation:**

```go
type Remote struct {
    Name string `toml:"name"`
    URL  string `toml:"url"`
}

type RepoConfig struct {
    URL     string   `toml:"url"`     // single-URL form
    Remotes []Remote `toml:"remotes"` // multi-remote form (mutually exclusive with URL)
    Slug    string   `toml:"slug"`    // explicit override; empty = derive
    Branch  string   `toml:"branch"`
}

type WorkspaceSection struct {
    Name        string `toml:"name"`
    Description string `toml:"description"`
    Terminal    string `toml:"terminal"` // explicit terminal repo name
}
```

## 5. Slug derivation

Two distinct quantities. Both are derived per repo.

### 5.1 Canonical slug (per repo)

Used for display / status / identity. One value per repo.

1. If `RepoConfig.Slug` is set in the toml → use it.
2. Else if `Remotes` has an entry named `origin` → derive from that URL.
3. Else if `Remotes` has multiple entries (no `origin`) → derive from the
   first entry in declaration order.
4. Else (single `url` form) → derive from it.
5. Else (derivation fails — non-github URL with no explicit slug) →
   canonical slug is empty; the repo cannot be referenced as a dep target.

### 5.2 Slug set (per repo)

Used for graph edge matching. Multiple values per repo.

The union of:

- The canonical slug (if non-empty).
- Slugs derived from EVERY entry in `Remotes` (or from the single `url`).
- The explicit `Slug` from the toml, if set.

Empty slugs are dropped from the set. The set is what we test against when
asking "does this input URL refer to repo X?"

### 5.3 GitHub-slug regex menu (ported from bash)

`extract_github_slug(url string) string` returns `owner/repo` for any of:

| Input form                        | Example                                   |
| --------------------------------- | ----------------------------------------- |
| `github:owner/repo`               | `github:phillipgreenii/nix-overlay`       |
| `github:owner/repo/subdir`        | `github:phillipgreenii/nix-overlay/main`  |
| `https://github.com/owner/repo`   | with optional `.git` and/or trailing path |
| `git@github.com:owner/repo.git`   | SSH shorthand; `.git` optional            |
| `ssh://git@github.com/owner/repo` | full SSH URL; `.git` optional             |

Anything else returns the empty string. Non-github hosts (Forgejo,
Bitbucket, GitLab) are intentionally not parsed — they cannot participate
in a github-input dep edge unless given an explicit `slug` in the toml that
matches a sibling repo's github input.

## 6. Slug-set sanity check against `git remote -v`

For each repo:

1. Run `git -C <repoDir> remote -v` once per repo (via worker pool).
2. Build a map `remote-name -> URL` from the output. Multiple URLs for the
   same remote name (fetch + push) must agree; first-fetch entry wins.
3. For every named remote in the toml (`url` is the implicit `origin`,
   `remotes` are named explicitly):
   - The remote must exist in git, with the same URL. Mismatch (URL drift)
     → error: `slug-set mismatch on repo X: toml says origin=A, git says
origin=B`.
4. Untracked git remotes (in git but not in toml) → ignored. Users may
   keep personal remotes that pn does not need to know about.

This check runs in the same per-repo concurrent stage as `nix eval`.

## 7. Reading flake inputs

Per repo, for each repo whose directory contains `flake.nix`:

- Run `nix eval --json --file <repoDir>/flake.nix "inputs"`.
- Parse JSON. The result is a map where each input has at least a `url`
  field; some have nested attributes — we recursively walk the structure
  for any string value, then filter to those that match the github-slug
  regex menu (mirrors the bash `jq '.. | strings | select(startswith…)'`).
- On `nix eval` failure: log a warning to stderr and treat the repo as
  contributing no out-edges. Discovery continues for other repos.
- Repos without `flake.nix` are not graph nodes — they are dropped
  silently. (The toml may list them for non-flake reasons; status/discover
  still surface them via separate logic out of scope here.)

We use `nix eval` (not `flake.lock`) deliberately. pn-workspace is a
development tool for editing multiple flakes simultaneously; if the user
just added an input to `flake.nix` but has not yet run `nix flake lock`,
the new input must still be visible to the dep graph. The lock would lag.

## 8. Dep graph

Vertices: workspace repos that have a `flake.nix`.
Edges: B → A when B's `flake.nix` has an input URL whose extracted slug is
in A's slug-set (per §5.2). Self-edges (a repo declaring itself as an
input) are ignored.

Multi-remote case ("A has remotes r0+r1, B uses r0, C uses r1"): both r0's
slug and r1's slug are in A's slug-set. Both B→A and C→A edges resolve
correctly. This is the case the design is built to handle.

Ambiguity case (two distinct workspace repos have overlapping slug-sets):
parse error. This should be impossible in practice — two repos with the
same github slug would be the same repo. We surface it as a clear error
rather than silently picking one.

## 9. Terminal selection

1. Compute in-degree per vertex.
2. Candidates = vertices with in-degree 0.
3. If `[workspace].terminal` is set:
   - Must name a vertex (graph node, i.e. has `flake.nix`).
   - Must be in `candidates` — else error: `terminal X is depended on by Y
(in-degree=1); cannot be the terminal`.
   - Use it.
4. Else if exactly one candidate → use it. Optionally warn if
   `[workspace].terminal` is unset and there are siblings to remind the
   user to set it explicitly. (Decision: no warning. Single-candidate is
   unambiguous and the warning would be noise on the common case.)
5. Else (multiple candidates, no `[workspace].terminal`) → error: list
   candidates, instruct user to set `[workspace].terminal`.
6. Else (zero candidates → graph has a cycle) → error: list the cycle.

## 10. inputName resolution

Per non-terminal repo X:

1. Iterate the terminal flake's parsed inputs (from §7).
2. For each input `(name, url)`, extract the slug from url.
3. If slug is in X's slug-set → record `name` as X's `InputName`.
4. If multiple input names match X's slug-set → error: `terminal has
multiple inputs pointing at workspace repo X: ...`. (Should not happen
   in practice; surface as a hard error if it does.)
5. If zero input names match → leave `InputName` empty and continue. This
   is legal (a workspace sibling that the terminal does not consume) and
   the consuming verbs handle empty `InputName` by skipping the override.

## 11. Topological ordering

Kahn's algorithm: emit vertices in increasing topological depth (deps
first), with a stable secondary sort by repo name within each level for
determinism. The terminal is always last (it has the deepest level by
definition since every other vertex eventually feeds into it).

The Discover() return slice's last element is the terminal when the graph
is non-empty.

## 12. Concurrency

A single shared worker pool — `internal/exec.NewWorkerPool(n int)` —
serves every per-repo subprocess (currently `nix eval --json --file
…/flake.nix inputs` and `git remote -v`). Default `n = runtime.NumCPU()`.
Each repo's set of subprocesses runs sequentially within the repo (so
status output is coherent per repo) and the per-repo batches run in
parallel up to the pool's worker count. The pool is constructed once in
`Workspace.Open` (so its lifetime matches the Workspace it belongs to)
and exposed as `ws.pool`. New subprocess fan-out call sites obtain it
via the Workspace; the existing `Runner` interface is unchanged. The
pool consumes a `Runner` internally, so test fixtures swap a fake
`Runner` as before and the pool transparently dispatches.

Errors from any subprocess propagate up but do not cancel sibling repos —
discovery is best-effort across repos, and per-repo failures become per-
repo warnings/errors aggregated into the final return.

## 13. Output: revised `Repo` struct

```go
type Repo struct {
    Name       string
    URL        string  // canonical URL (= origin's URL when remotes form; URL field when single-url form)
    Path       string  // <workspace_root>/<name>
    InputName  string  // empty for the terminal repo and for siblings not consumed by the terminal
    IsTerminal bool    // exactly one repo in a non-empty graph
}
```

`Discover()` returns `[]Repo` in topological order; the terminal is the
last element when the graph is non-empty. Callers can find the terminal
either by `repo.IsTerminal` or by indexing the last element.

## 14. Errors callers must handle

Each surfaces with a clear, actionable message:

1. **TOML schema violation**: both `url` and `remotes` set on a repo; or
   `remotes` empty; or `[workspace].terminal` names a non-existent repo.
2. **Slug-set mismatch**: toml `[repos.X].remotes` declares
   `origin=git+ssh://...A.git` but `git -C X remote -v` reports
   `origin=git+ssh://...B.git`.
3. **Ambiguous slug-set across repos**: two workspace repos publish
   overlapping slugs (canonical or via remote list).
4. **Multiple terminal candidates without explicit selection**: error with
   the candidate list and instructions to set `[workspace].terminal`.
5. **Explicit terminal is depended on**: `[workspace].terminal` names a
   repo with in-degree > 0.
6. **Dep cycle**: no zero-in-degree vertex; list the cycle.
7. **Multiple inputName matches**: terminal has more than one input
   pointing at the same workspace repo.

Non-errors (warnings or silent):

- `nix eval` failure on one repo → that repo contributes no out-edges
  (warned to stderr, discovery continues).
- Repo with `flake.nix` but no slug → it cannot be a dep target, but may
  still be a graph vertex with outgoing edges.

## 15. Testing strategy

- **Slug regex menu (`slug_test.go`)**: table-driven over the bash
  `extract_github_slug` cases, including non-matches.
- **TOML parsing (`config_test.go` extensions)**: `url`+`remotes`
  mutual-exclusion, empty `remotes` rejection, `[workspace].terminal`
  pointing at a missing repo.
- **Slug-set computation (`discover_test.go`)**: canonical vs set,
  precedence ordering.
- **Graph construction**: vertex enumeration (flake.nix presence),
  single-remote dep, multi-remote dep, self-edge dropped, two-repo cycle
  detection, multi-remote A with B and C consumers.
- **Terminal selection**: single candidate, multiple candidates +
  explicit terminal in toml, multiple candidates + no terminal in toml
  (error), explicit terminal that is depended on (error).
- **inputName resolution**: simple case, sibling-not-consumed case
  (empty InputName), multiple matches (error).
- **Topological order**: deterministic by repo name within level;
  terminal last; bench (informational only) verifying parallel `nix eval`
  is meaningfully faster than serial on a 4-repo workspace.

All graph tests use a fake `Runner` returning canned `nix eval` JSON and
`git remote -v` output — no real subprocess in unit tests.

## 16. Files touched

| File                                     | Change                                                                                                                                          |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/workspace/config.go`           | Add `Remotes`, `Slug` to `RepoConfig`; add `Terminal` to `WorkspaceSection`; parse-time validation rules (§4).                                  |
| `internal/workspace/config_test.go`      | New parse tests covering the validations.                                                                                                       |
| `internal/workspace/slug.go` (new)       | `ExtractGithubSlug` regex menu (§5.3) + `CanonicalSlug` + `SlugSet`.                                                                            |
| `internal/workspace/slug_test.go` (new)  | Table-driven slug tests.                                                                                                                        |
| `internal/workspace/discover.go`         | Replace flat alphabetical with graph-aware discovery (§7-§13).                                                                                  |
| `internal/workspace/discover_test.go`    | Major extension; cover graph, terminal selection, inputName resolution.                                                                         |
| `internal/exec/workerpool.go` (new)      | Bounded concurrent runner pool (§12). Existing `Runner` interface unchanged; `WorkerPool` is a higher-level orchestrator that calls a `Runner`. |
| `internal/exec/workerpool_test.go` (new) | Verifies bounded concurrency, error aggregation, ordering preservation per-key.                                                                 |

## 17. Out-of-scope follow-ups (tc-perh.5.2 remainder)

- `build.go`: pick terminal flake from `Discover()`, run only there, emit
  real `--override-input <name> git+file://<path>` per non-terminal repo
  (and `git+file://` vs `path:` choice — defer to a small follow-up
  decision when consuming).
- `apply.go`: same as build but for the apply command.
- `flake_check.go`: call `computeOverrideArgs(ws)` (extended to use the
  new `InputName` field) per-repo.
- `nix.go`: deny-list for subcommands incompatible with
  `--override-input`.
- `update.go`: signal handling + lock regeneration.
- `tree.go`: ASCII dep-graph renderer using the new topological data.
