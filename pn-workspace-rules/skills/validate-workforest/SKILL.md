---
name: validate-workforest
description: >-
  Use to VALIDATE a coordinated workforest set before landing it — confirm the
  isolated cross-repo workspace still builds/checks cleanly after the WORK, so
  landing is safe. Fires on: "validate the workforest", "is this set good to
  land?", the validate step of `/pn-workspace-sync` or `/pn-workspace-update`, and the check-before-land
  point of the ad-hoc bead work-cycle. This is the VALIDATE stage of the
  workforest work-cycle (fork → WORK+validate → land → cleanup). It checks the
  WORKSPACE (does the assembled system still build), NOT whether the WORK
  achieved its goal. Do NOT use for a single repo's own tests, to land the set
  (`land-workforest`), or as the workspace Completion Gate outside a set (that is
  the pn-workspace-rules Completion Gate directly).
---

# validate-workforest

**RUN FROM: inside the set** (`<workspace_root>/.workforests/<branch>`). This
stage MUST refuse to run when the resolved root is the canonical workspace
rather than a `.workforests/<branch>` set — run `pnwf resolve` and, if
`in_workforest` is false (or `pnwf` exits non-zero), **halt and report**.

**Contract (MUST).** On success, the **workspace is guaranteed valid** — i.e.
the coordinated set still builds/checks cleanly and is safe to land. This stage
validates the _workspace_, not whether the WORK was correct; a green validate
does not mean the change did what it intended.

**Disambiguation.** This validates a whole coordinated SET. It is the in-set
form of the pn-workspace Completion Gate; do not confuse it with a single repo's
unit tests.

## Deterministic facts from `pnwf`; tier judgment stays here

`pnwf` supplies facts to inform the tier: `pnwf repos` (the set's members in
topo order) and, per repo, whether it changed vs its primary (via the guarded
primitives). This skill owns the judgment the script cannot make: **which
Completion-Gate tier** actually guarantees the whole set is valid.

## Steps

1. **Location guard (MUST).** `pnwf resolve`; require `in_workforest = true`.
   Any non-zero `pnwf` exit → halt and report.
2. **Choose the tier.** Apply the existing **pn-workspace-rules Completion Gate**
   tiering (do NOT restate the checklist here — follow that skill's tier table).
   Because "does the change touch the assembled system" is often not
   script-decidable, **default to the full `pn workspace build` (Tier 3)**. Go
   lighter only when a lower tier still guarantees the _whole set_ is valid.
   - Note the `--repos` subset case: a subset that excludes the terminal cannot
     `pn workspace build`; validate at the highest tier the subset supports and
     say so in the report. That same excluded terminal also makes `pn workspace
doctor`'s `terminal-resolvable` check unresolvable by construction — step 6
     below is the classifier carve-out this implies.
3. **Run the selected `pn` check verbs** for that tier (e.g. `pn workspace
flake-check`, or `pn workspace build`), then `pn workspace doctor` as the
   final consistency gate.
4. **Dirty tree → WARN, do not fail (MUST).** If the set has uncommitted changes,
   validate MUST NOT fail on that alone — it **warns** (the WORK may be
   mid-flight). Landing has its own no-uncommitted-changes precondition.
5. **In-set `flake-lock-fresh` on a sibling this run will land → WARN, do not fail
   (MUST).** Inside a set, `pn workspace doctor` runs in `worktree` mode, where each
   repo's reference rev is that member's own committed HEAD rather than its remote
   head. So the moment the WORK advances a sibling, every consumer that pins it by
   rev reports a `flake-lock-fresh` ERROR — drift the pipeline itself caused.
   Validate MUST downgrade such a finding to a **warning** when the stale pin's
   TARGET is a member of THIS set with un-landed commits, and MUST leave every other
   `flake-lock-fresh` finding an ERROR. Nothing in-set can clear the exempt case:
   `pn workspace push` skips relocking inside a set, and a `flake.lock` can only pin
   an already-published rev, which the target's in-set HEAD is not — landing, then
   publishing, is what converges it.
   - **Classify; do not assume.** `pn workspace doctor --json` names the CONSUMER in
     `.findings[].repo` and the TARGET inside `.findings[].message`
     (`… input "<alias>" (→ "<target>") pins <a> but "<target>" is at <b>`).
     `pnwf land-plan <branch>` lists exactly the set members with un-landed commits
     ("present worktree, not an ancestor of primary"). Target in that list →
     warning. Target absent → the drift is NOT this run's to clear, so it stays an
     ERROR and validate FAILS.
   - **Doctor's exit status is NOT the verdict here.** Doctor knows nothing of this
     carve-out and still exits `1` on a downgraded-only report, so read the
     findings. Validate MUST NOT pass `--strict` in-set — it re-promotes warnings to
     errors and re-creates the very failure this step removes.
   - The exemption is gated on `.mode == "worktree"`, so it can never reach a
     canonical checkout. This classifier applies it:

     ```bash
     landing=$(pnwf land-plan "$BRANCH")
     pn workspace doctor --json | jq -r --arg landing "$landing" '
       ($landing | split("\n") | map(select(length > 0))) as $L
       | (.mode == "worktree") as $inset
       | "mode=\(.mode)",
         ( .findings[]
           | select((.severity | ascii_downcase) == "error" and (.skipped | not))
           | (.message | capture("\\(→ \"(?<t>[^\"]+)\"\\)") | .t) // "" as $target
           | if $inset and .check == "flake-lock-fresh" and ($L | index($target))
             then "EXEMPT   \(.repo)\t\(.message)"
             else "BLOCKING \(.repo)\t\(.check)\t\(.message)"
             end )'
     ```

     Validate clears the doctor gate when that prints **no `BLOCKING` line**. A
     finding whose message yields no target falls through to `BLOCKING`, so a
     message-format change fails the gate CLOSED rather than exempting silently.

6. **`--repos` subset excludes the terminal → `terminal-resolvable`
   (`missing_terminal`) WARN, do not fail (MUST).** A subset forked via `pn
workspace workforest add <branch> --repos a,b` gets its own filtered
   `pn-workspace.toml` (`filterConfig`, `modules/pn/internal/workspace/workforest_subset.go`),
   which deliberately clears `workspace.terminal` when the configured terminal
   is not one of the subset's members — the subset genuinely doesn't contain
   it. `pn workspace doctor`'s `terminal-resolvable` check then tries
   auto-detection over the subset's own (smaller) dependency graph
   (`autoDetectTerminal`, `modules/pn/internal/workspace/terminal.go`), which
   requires a candidate sink to share a connected component with at least one
   other flake repo in the graph being searched. When the subset's members
   have no dependency edge on EACH OTHER — their shared dependency is
   precisely the excluded terminal, e.g. two otherwise-unrelated repos
   subsetted together because a change touches both — every candidate is
   isolated and gets filtered out, so NO terminal resolves at all. This is a
   **structural** consequence of restricting the graph to a subset that
   excludes its terminal, not a misconfiguration: the identical subset run
   against the FULL (unforked) workspace never hits this, and re-running with
   `workspace.terminal` explicitly set to a SUBSET member would not fix it
   either (the real terminal still isn't a member — setting it to the wrong
   repo just trades this finding for `terminal_not_sink`). Validate MUST
   downgrade `terminal-resolvable`'s `missing_terminal` code to a **warning**
   when the run is confirmed to be a genuine `--repos` subset (below), and
   MUST leave `terminal_not_sink` and `missing_flake_path` (the check's other
   two codes) untouched — those are real problems even inside a subset.
   - **Distinguish a subset from a genuinely misconfigured FULL set.** A full
     (non-`--repos`) workforest set copies `pn-workspace.toml` byte-for-byte
     (`writeSetMembership`'s `isFullSet` branch), so `workspace.terminal`
     empty on a full set means the CANONICAL workspace itself never had a
     terminal configured — a real bug that would show up identically on the
     canonical primary and MUST stay BLOCKING. `mode == "worktree"` plus
     `workspace.terminal == ""` alone cannot tell the two apart; only an
     actual repo-count comparison against the canonical workspace can.
     Compute it once, alongside `land-plan`:

     ```bash
     set_count=$(env -u PN_WORKSPACE_ROOT pn workspace info --json | jq '.repos | length')
     canonical_root=$(pnwf resolve | jq -r '.canonical_root')
     canonical_count=$(PN_WORKSPACE_ROOT="$canonical_root" pn workspace info --json | jq '.repos | length')
     is_subset=$([ "$set_count" -lt "$canonical_count" ] && echo true || echo false)
     ```

   - **Classify in the same pass as step 5.** Extend that step's jq classifier
     with a second exemption branch, gated on `$subset` in addition to
     `$inset`:

     ```bash
     pn workspace doctor --json | jq -r --arg landing "$landing" --argjson subset "$is_subset" '
       ($landing | split("\n") | map(select(length > 0))) as $L
       | (.mode == "worktree") as $inset
       | "mode=\(.mode)",
         ( .findings[]
           | select((.severity | ascii_downcase) == "error" and (.skipped | not))
           | (.message | capture("\\(→ \"(?<t>[^\"]+)\"\\)") | .t) // "" as $target
           | if $inset and .check == "flake-lock-fresh" and ($L | index($target))
             then "EXEMPT   \(.repo)\t\(.message)"
             elif $inset and $subset and .check == "terminal-resolvable"
                  and (.message | startswith("missing_terminal"))
             then "EXEMPT   \(.repo)\t\(.message)"
             else "BLOCKING \(.repo)\t\(.check)\t\(.message)"
             end )'
     ```

   - **`lock-current` needed no carve-out here.** The same `--repos` subset
     shape also made `lock-current` ("edges/order differ from a fresh
     derive") fire falsely whenever the subset's members have zero edges
     between each other: the on-disk lock `pn workspace workforest add`
     writes (`filterLock`) starts from `emptyLock()`'s non-nil
     `Edges: []LockEdge{}`, while `checkLock`'s fresh re-derive
     (`deriveLock`→`buildEdges`) leaves an untouched, never-appended-to `nil`
     slice — and `reflect.DeepEqual(nil, []LockEdge{})` is `false`. Unlike
     `terminal-resolvable`, this was a genuine implementation bug, not a
     structural fact about subset graphs (a fresh derive over the subset
     alone agrees with the filtered lock on every edge that survives
     filtering, byte for byte, once nil and empty compare equal) — so it was
     fixed directly in `checkLock`
     (`modules/pn/internal/workspace/doctor_checks_structural.go`) rather than
     carved out here. No classifier change was needed or made for it.

## Policies

- MUST guarantee validity on success.
- MUST NOT fail solely because the working tree is dirty — warn instead.
- MUST NOT fail solely because an in-set `flake.lock` still pins a sibling this run
  is about to land — warn instead (step 5). This is a NARROWING, not a removal:
  every other `flake-lock-fresh` finding, and every finding of every other check,
  keeps its severity.
- **Step 5 MUST NOT be widened to a target with NO un-landed commits.** The tempting
  case is a consumer pinning a sibling whose primary carries locally landed but
  UNPUBLISHED commits: in `worktree` mode that pin is stale by construction too, yet
  the target is absent from `pnwf land-plan`, so step 5 correctly leaves it an ERROR.
  That drift is a PIPELINE-ORDER problem — only the publish step can converge it —
  and `/pn-workspace-sync` resolves it OUTSIDE this stage: it short-circuits the case
  where nothing was synced, and otherwise takes a bounded publish-then-re-validate
  escape (bd `pg2-6gjcy`). Widening step 5 to cover it would exempt genuinely stale
  pins along with it, which is the "silent hole" this carve-out exists to avoid.
- MUST NOT fail solely because a `--repos` subset's own `terminal-resolvable`
  check reports `missing_terminal` — warn instead (step 6). This is likewise a
  NARROWING: `terminal_not_sink` and `missing_flake_path` keep their severity,
  and so does `terminal-resolvable` on any run that is not a confirmed subset.
- **Step 6 MUST NOT be widened to `mode == "worktree"` alone (dropping the
  subset-count check).** A FULL workforest set copies `pn-workspace.toml`
  verbatim, so `workspace.terminal == ""` there means the CANONICAL workspace
  itself has no terminal configured — a real bug, not a subsetting artifact.
  Exempting on `mode` alone would silently wave that through on every full-set
  validate; the repo-count comparison against canonical is what tells the two
  apart.
- This is the single Facade for validating a set; consumers (the sync command,
  the bead work-cycle) call it rather than re-deriving check commands.

## Relationship to landing

`validate-workforest` is a **pre-rebase snapshot**. `land-workforest` rebases
each repo onto the _current_ primary at land time, so validate SHOULD
immediately precede land. The post-land recheck is a `pn workspace build` on the
canonical primary (the set is dismantled during landing).

**The post-land gate is NOT relaxed (MUST).** Step 5's carve-out is gated on
doctor's `worktree` mode, so it cannot reach a canonical checkout: there doctor
runs in `primary` mode, where `flake-lock-fresh` compares each consumer against the
target's **remote** head. That comparison is what actually protects published lock
freshness, so on the canonical primary `flake-lock-fresh` MUST remain a **hard
error** — satisfied by `pn workspace push`, never waived. Step 5 exempts only drift
against a rev that is not published yet _because this run has not landed it yet_;
once the run lands and publishes, the same drift is back under the hard gate.

Step 6's carve-out is likewise gated on `mode == "worktree"` PLUS the subset-count
check, so it too cannot reach a canonical checkout — a canonical clone is never a
`--repos` subset of itself. Unlike step 5, there is no later convergence step to
reason about: `land-workforest` folds each member back onto its own canonical
primary one repo at a time (the coordinated set, and the notion of "this subset
excludes the terminal," cease to exist once the set is dismantled), and the
canonical workspace's `pn workspace doctor` always sees the FULL graph, where the
real terminal was never excluded. `terminal-resolvable` on the canonical primary
therefore isn't "the same finding, now hard" the way `flake-lock-fresh` is after
landing — it is simply a different, unrelated doctor run over a graph the subset
carve-out never applied to.

## Frontmatter constraint: never set `disable-model-invocation`

This stage MUST NOT carry `disable-model-invocation` in its frontmatter. The flag
is enforced against the `Skill` tool and also drops the entry from the
model-visible skill listing, so setting it makes this stage unreachable by the
two things that actually reach it: the `Fires on:` prose triggers declared in its
own `description` above, and the orchestrators that dispatch it as a pipeline
stage (`/pn-workspace-sync` and `/pn-workspace-update` steps 3-4, and
`/drain-beads` for the fork stage).

It was set here once as an always-on-listing token saving and reverted for
exactly this reason (bd `pg2-dytfv`; the sibling case in `integrate-branch`'s two
landing handlers is bd `pg2-okzl0`). A stage skill earns its listing cost by
being auto-triggerable; if that cost is ever revisited, cut the `description`
down rather than blocking invocation.
