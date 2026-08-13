# REMOVE_CORP_REFERENCE.md

## What this file is

This repository contains references to a current/former employer (ZipRecruiter, abbreviated
`ZR`) — the corporate email address, the Atlassian tenant, a macOS keychain item name, a live
SSH clone URL for a private work repository, and a `zr` module namespace. This file tracks
every such reference so they can be removed as a unit.

**This file is itself temporary.** It is committed so the work is visible and resumable across
sessions, and the LAST item on the checklist below is to delete it. It MUST NOT outlive the
cleanup.

**Scope:** this repository only. Sibling trackers exist in `nix-agent-support`, `nix-personal`,
`nix-overlay`, and `bb`. `homelab` and `ha-addon-esphome-mcp` are clean and have no tracker.

**Severity:** third by volume, but this repo holds the most CREDENTIAL-SHAPED material of the
five — a complete corporate-credential retrieval recipe and a real private clone URL, both in
tracked Go test files rather than only in docs.

## Provenance

Findings assembled 2026-08-12/13 from three independent read-only sweeps: current content at
every ref tip (incl. untracked and gitignored files), full reachable commit history
(`git log --all` pickaxe + diff-regex), and every object in the store regardless of
reachability (`git cat-file --batch-all-objects`), plus reflogs, notes, and `rr-cache`.

Counts are occurrences in the working tree, verified 2026-08-13, and are re-derivable. Re-run
the commands before claiming an item done; do not trust the recorded number.

```bash
rg -i -o 'ziprecruiter' . | wc -l     # expect 183 -> must reach 0
rg -i -l 'ziprecruiter' . | wc -l     # expect 37 files -> must reach 0
rg -i -o 'starterview'  . | wc -l     # expect 0 (clean)
```

---

## Findings

### A. Corporate credential retrieval recipe — HIGHEST SEVERITY HERE

`docs/superpowers/plans/2026-06-26-generic-jira-access-tool-sp2-zr-edge.md` — **41
occurrences**, the densest single file in the repo. Verbatim at `:262`:

```
command = ["security", "find-generic-password", "-s", "zr-jira", "-a", "phillipg@ziprecruiter.com", "-w"]
```

repeated at `:240, :249, :256, :281, :545, :559, :567, :578, :607, :608, :753`. Also contains
`https://ziprecruiter.atlassian.net` (×3) and the corporate GitHub account path
`phillipgziprecruiter/phillipg_mbp.git` (×4).

No secret VALUE is committed. The corporate email, the exact keychain service name (`zr-jira`),
and the complete retrieval command are.

- [ ] **A1** — Rewrite this document with placeholders, or delete it if superseded.
- [ ] **A2** — Purge it from history (§F).

### B. Corporate email and Atlassian tenant elsewhere

```bash
rg -i -n -e 'phillipg@ziprecruiter\.com' -e 'ziprecruiter\.atlassian\.net' .   # 12 + 3
```

- `docs/superpowers/specs/2026-06-26-generic-jira-access-tool-design.md` (14 `ziprecruiter`)
- `docs/superpowers/plans/2026-06-26-generic-jira-access-tool-sp{1,3,4,5,6}*.md`
- `docs/superpowers/plans/2026-06-29-generic-jira-search-pagination.md`

- [ ] **B1** — Replace the corporate email and tenant URL with placeholders throughout
      `docs/superpowers/`.

### C. Live private clone URL in tracked Go tests

```bash
rg -n 'phillipgziprecruiter' .   # 4 occurrences
```

Verbatim `url = 'git@github.com:phillipgziprecruiter/phillipg_mbp.git'` in:

- `modules/pn/cmd/pn-workspace-toml-enforce/main_test.go:20`
- `modules/pn/internal/workspace/enforce_keys_test.go:40, :313, :373`

This is a real SSH clone URL for a private work repository, used as fixture data. The private
repo name also leaks into `modules/pn/internal/workspace/{enforce_keys,dag,lock}_test.go`
(17/5/… occurrences of `phillipg-nix-ziprecruiter`).

- [ ] **C1** — Replace the clone URL and repo names in `modules/pn` test fixtures with
      fictional values.

### D. Private sibling repo name and `zr` namespace

```bash
rg -i -o 'phillipg-nix-ziprecruiter' . | wc -l   # 92
rg -i -o 'zr-jira' . | wc -l                     # 35
```

| Class                       | Count | Notes                                                                                                  |
| --------------------------- | ----: | ------------------------------------------------------------------------------------------------------ |
| `phillipg-nix-ziprecruiter` |    92 | incl. `flake.nix:1`, `modules/pn/enforce-toml.nix`, `modules/pn/cmd/pn-workspace-toml-enforce/main.go` |
| `pg-pr-issues-jira-zr`      |    70 | ZR Jira CLI name                                                                                       |
| `zr-jira`                   |    35 | macOS keychain service name                                                                            |
| `pg-pr-zr`                  |    33 | Nix module for the ZR edge — `modules/pg-pr-zr/`                                                       |
| Prose `ZR`                  |    82 | shorthand for the employer across docs                                                                 |

Densest remaining files: `docs/superpowers/plans/2026-06-29-activation-output-consistency.md`
(14), `docs/superpowers/plans/2026-06-29-pn-applied-gates-phase3-plugin-smoke-wiring.md` (11),
`docs/superpowers/specs/2026-06-29-activation-script-output-consistency-design.md` (10).

- [ ] **D1** — Rename the `modules/pg-pr-zr/` module tree and its binaries
      (`cmd/pg-pr-issues-jira-zr/`) to neutral names, or move them to the private ZR repo.
- [ ] **D2** — Remove `phillipg-nix-ziprecruiter` from `flake.nix` and `modules/pn` source.
- [ ] **D3** — Rename the `zr-jira` keychain service to a generic name.
- [ ] **D4** — Rewrite prose `ZR` references generically.

### E. Filenames that are themselves references

- [ ] **E1** — `docs/superpowers/plans/2026-06-26-generic-jira-access-tool-sp2-zr-edge.md`
- [ ] **E2** — `modules/pg-pr-zr/` (directory) and `modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/`
- [ ] **E3** — `docs/superpowers/plans/2026-06-29-jira-zr*.md` and
      `docs/adr/0040-consume-agent-support-thin-zr*.md`

### F. Findings NOT fixable by editing files

These survive any content edit and MUST be handled explicitly.

| Item                                              | Detail                                                                                                                                                                                                                                                                                                    |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Commit authorship**                             | **482 of 639 commits (75%)** are authored AND committed as `phillipg@ziprecruiter.com`. Baked into commit objects — only a history rewrite changes it.                                                                                                                                                    |
| **Corporate GitHub account in a noreply address** | 1 committer record: `Phillip Green II <100377090+phillipgziprecruiter@users.noreply.github.com>`. **This form contains no `ziprecruiter.com` and no word boundary before the handle — it is invisible to any search for the email domain.** Any rewrite mapping MUST match on the handle, not the domain. |
| **Unreachable objects**                           | 2 blobs carrying `ziprecruiter`: `506e4ad8` and `aa8e561c` (the latter **referenced by no tree at all** — `git add`-ed, never committed), both `flake.nix` with `# ... Consumed by phillipg-nix-ziprecruiter's`. Invisible to `git log --all`.                                                            |
| **Reflog-only objects**                           | `081f1717`, `476f5858`, `e1b5d37f` — all `flake.nix`.                                                                                                                                                                                                                                                     |

- [ ] **F1** — Rewrite history (`git filter-repo`) to purge §A and the content references.
- [ ] **F2** — Rewrite commit authorship to the personal identity in the same pass. The mailmap
      MUST include the `100377090+phillipgziprecruiter@users.noreply.github.com` form.
- [ ] **F3** — After rewrite, run `git reflog expire --expire=now --all` followed by
      `git gc --prune=now --aggressive`, then re-run the §Provenance sweeps to confirm 0
      across ALL objects.
- [ ] **F4** — Force-push, and confirm the remote no longer serves the old objects.

### G. The existing guardrail is real but too narrow

`modules/jira/pkg/pjira/guardrails_test.go:13` already enforces a denylist:

```go
forbidden := []string{"ziprecruiter", "zr-jira", "security find-generic-password", "secret-tool", "/pg-pr/", "provider/issues"}
```

with `:10` — `// TestNoForbiddenStrings asserts the generic core stays generic: no ZR strings,`.

This is the right mechanism and the best precedent in the workspace. But it is scoped to
`modules/jira`'s generic core only — it does not cover `docs/`, `modules/pn`, or
`modules/pg-pr-zr`, which is why 183 occurrences coexist with a passing test. The denylist
also spells the strings out, so the guard file is itself a reference.

- [ ] **G1** — Widen this guard to cover the whole repository.
- [ ] **G2** — Express the denylist so the guard file does not itself contain the literals
      (e.g. assembled from parts, or read from an untracked/private fixture).

---

## Final item

- [ ] **Z1** — **DELETE THIS FILE.** It names the employer, the corporate email, the keychain
      item, and the private clone URL — it is itself a corporate reference and MUST NOT survive
      the cleanup. Removing it is the last step, and it MUST also be purged from history in the
      §F rewrite (or added after the rewrite and removed in a final ordinary commit).

## ⚠️ THIS REPOSITORY IS ALREADY PUBLIC

Verified 2026-08-13 against the GitHub API: `github.com/phillipgreenii/nix-repo-base` has
`visibility: public`, last pushed 2026-08-12. **Everything in §A–§G is world-readable right
now** — including the corporate email, the Atlassian tenant, the `zr-jira` keychain service
name, the complete `security find-generic-password` retrieval command, and the real private
clone URL `git@github.com:phillipgziprecruiter/phillipg_mbp.git`.

This is NOT a "clean it up before going public" task. It is remediation of a live exposure and
SHOULD be treated as time-sensitive.

`homelab/nix/flake.nix:116` already consumes this repo as `github:phillipgreenii/nix-repo-base`
— i.e. over the public fetcher — which independently confirms the public status.

### A history rewrite does NOT undo publication

Force-pushing a rewritten history removes the objects from `main`, but MUST NOT be treated as
un-publishing. All of the following can retain the old content:

- GitHub continues to serve unreachable objects by SHA on the original repository until
  GitHub Support is asked to run garbage collection.
- Any fork, clone, or mirror made while the content was public.
- Search-engine, CDN, and code-search caches; archival services.
- Anything that scraped the repo for training or indexing.

- [ ] **P1** — Treat the credential recipe in §A as exposed. The keychain SERVICE name and the
      account are public; rotating the underlying Jira API token SHOULD be considered even
      though no token value was ever committed.
- [ ] **P2** — Consider making the repository private IMMEDIATELY as a containment step,
      before the cleanup rather than after. This is reversible; the exposure is not.
- [ ] **P3** — After the §F rewrite and force-push, ask GitHub Support to garbage-collect
      unreachable objects, and check for forks.

## Once this list is complete

Once every item above is done and the verification sweeps return zero across all objects — not
just `main` — **this repository is safe to be public**, which it already is.

If **P2** is taken and the repo is made private as a containment step, note that
`homelab/nix/flake.nix` consumes it via the `github:` fetcher, which hits the unauthenticated
GitHub API and will 404 on a private repo. Making this repo private therefore BREAKS the
homelab flake until that input is switched to `git+ssh` — the mirror image of the problem in
bead `tc-1q5w`. Sequence the containment step with that change, or it will take the fleet down
further.
