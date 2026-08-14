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
     say so in the report.
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
           | select(.severity == "error" and (.skipped | not))
           | (.message | capture("\\(→ \"(?<t>[^\"]+)\"\\)") | .t) // "" as $target
           | if $inset and .check == "flake-lock-fresh" and ($L | index($target))
             then "EXEMPT   \(.repo)\t\(.message)"
             else "BLOCKING \(.repo)\t\(.check)\t\(.message)"
             end )'
     ```

     Validate clears the doctor gate when that prints **no `BLOCKING` line**. A
     finding whose message yields no target falls through to `BLOCKING`, so a
     message-format change fails the gate CLOSED rather than exempting silently.

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
