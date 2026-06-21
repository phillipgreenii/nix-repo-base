# Next-session prompt — nix-\* consumer migrations (post-producer chunk)

The producer-side chunk closed on 2026-06-18. nix-repo-base now ships
flake-parts modules; the lib factories are gone. Three consumer flakes still
use the deleted lib factories AND/OR still pin the old nix-repo-base rev —
they will break the moment they `nix flake update`. This session migrates
them.

---

## Copy this prompt into the new session

```
Brainstorm consumer-side migrations for the three nix-* repos that depend on
nix-repo-base. The producer chunk landed yesterday (2026-06-18) on
nix-repo-base/main — see merged spec + plan below. This brainstorm produces
spec(s) + plan(s) + bead epic(s) via the same superpowers:brainstorming → spec →
spec-reviewer → writing-plans → plan-critic → plan-decomposer → epic-runner
flow we used for the producer chunk.

**Repos involved (HEAD pre-migration):**
- nix-repo-base: /home/tcadmin/workspace/nix-repo-base (main @ ce158d0 — the
  new flake-parts modular producer)
- nix-overlay: /home/tcadmin/workspace/nix-overlay (HEAD e177a27; on
  flake-utils; uses phillipgreenii-nix-base.lib.{mkChecks, mkPreCommitHooks,
  mkDevShell, mkInstallMetadata})
- nix-personal: /home/tcadmin/workspace/nix-personal (HEAD unknown — check;
  on flake-utils; uses lib.{mkChecks, mkTreefmtConfig, mkPreCommitHooks,
  mkBashBuilders, mkDevShell, mkInstallMetadata} AND three overlay factories
  mk{Unstable,LlmAgents,VscodeExtensions}Overlay)
- homelab: /home/tcadmin/workspace/homelab (homelab/nix/flake.nix already on
  flake-parts; uses nixBaseLib.{mkTreefmtConfig, mkPreCommitHooks, mkDevShell})

**Producer-side artifacts to read first (do NOT re-litigate decisions):**
- nix-repo-base/README.md — consumer wiring reference; the source of truth
  for the new API
- nix-repo-base/docs/superpowers/specs/2026-06-18-flake-parts-modular-producer-design.md
  — full producer-side design with all decisions pinned
- nix-repo-base/docs/superpowers/plans/2026-06-18-flake-parts-modular-producer.md
  — the producer-side plan; useful as a model for how consumer plans should be
  shaped
- nix-repo-base/tests/consumer-fixture/flake.nix — minimum-viable consumer
  exercising all 9 flake modules + the HM module; clone-and-adapt is fine

**Beads in scope (run `bd show <id>` for descriptions, all from
/home/tcadmin/workspace/homelab/.beads):**
- tc-rzgzq P3 — nix-overlay: prune nix-repo-base transitive inputs (A4) —
  partially absorbed by the producer chunk (the bloat is now gone); remaining
  consumer-side work is just migrating to flake-parts modules + adding the
  4 heavy inputs the consumer now owns
- tc-zt0hh P3 — nix-overlay: flake-parts migration (M3) — direct sibling of
  tc-rzgzq; the new producer chunk made flake-parts the standard, so this is
  now mandatory not optional
- (No existing beads for nix-personal or homelab migrations — create them
  during decomposition)

**Decisions LOCKED IN by the producer chunk (do NOT relitigate):**
1. flake-parts is the standard; no flake-utils fallback for new consumers.
2. Hard cutover — the lib factories are gone from nix-repo-base/main; consumers
   migrate or stay pinned.
3. Pin IS the version contract — no UL_LIB_VERSION constants or runtime
   version-guards. See memory: feedback-pin-is-the-version.
4. Consumer alignment via `follows`-discipline — see memory:
   feedback-grep-not-canonical-consumers; the alignment check (auto-contributed
   by flakeModules.checks) enforces.
5. `phillipgreenii.src` and `phillipgreenii.pre-commit.src` default to
   inputs.self; only set them for subdirectory scoping.
6. The 9 flake modules + 1 HM module + 10 lib functions are the canonical
   producer surface — see nix-repo-base/README.md for the table.

**Per-consumer scope (what each one actually has to change):**

- **homelab** — smallest lift. Already on flake-parts. Swap
  `nixBaseLib = inputs.phillipgreenii-nix-base.lib;` indirection + per-call
  `nixBaseLib.mkTreefmtConfig {...}` / `nixBaseLib.mkPreCommitHooks {...}` /
  `nixBaseLib.mkDevShell {...}` for `imports = [
  inputs.phillipgreenii-nix-base.flakeModules.{checks,devshell,pre-commit} ]`.
  Drop the explicit treefmt setup (pre-commit pulls it transitively).
  Update homelab/nix/flake.lock to bring in the new producer rev.

- **nix-overlay** — medium lift. flake-utils.lib.eachDefaultSystem →
  flake-parts.lib.mkFlake. Replace mkChecks/mkPreCommitHooks/mkDevShell calls
  with imports. mkInstallMetadata becomes the Shape B HM module wrapper
  (see spec §3.2 + producer README migration table). No heavy overlays in use
  today, so the alignment check stays a no-op. Supersedes tc-zt0hh's M3 work
  and absorbs the remainder of tc-rzgzq's A4 work; close both when this lands.

- **nix-personal** — biggest lift. flake-utils → flake-parts. Replace lib
  calls. Declare three heavy inputs at top-level (nixpkgs-unstable, llm-agents,
  nix-vscode-extensions — NOT flox; nix-personal doesn't use it). Replace the
  three `phillipgreenii-nix-base.lib.mk{Unstable,LlmAgents,VscodeExtensions}Overlay`
  call sites with `imports = [ flakeModules.{unstable,llm-agents,vscode-extensions}-overlay ]`
  + `nixpkgs.overlays = [ self.overlays.{unstable,llm-agents,vscode-extensions} ];`
  at the darwinConfigurations/nixosConfigurations level. mkBashBuilders stays
  as a lib call (it's used inside an overlay context — see the producer spec's
  M2 finding for why). Shape B HM module wrapper for install-metadata.

**Cross-consumer ordering / coordination concerns:**

- nix-personal consumes nix-overlay as an input. If nix-personal pins a newer
  nix-overlay than its `inputs.phillipgreenii-nix-overlay.inputs.phillipgreenii-nix-base.follows`
  resolves to, the lock has nix-repo-base twice. Migrate nix-overlay FIRST, then
  nix-personal can pull the migrated nix-overlay cleanly.
- Recommended order: homelab → nix-overlay → nix-personal. Each individually
  shippable; no required interlock other than nix-overlay-before-nix-personal.
- ALTERNATIVE: bundle all three into one coordinated chunk with three
  PR-group children (homelab, nix-overlay, nix-personal). Choose during
  brainstorm based on appetite for one-big-chunk vs three-sequential-chunks.

**Working pattern (locked in from the producer chunk):**
- spec → spec subagent review → apply fixes → plan → plan-critic review →
  apply fixes → plan-decomposer creates beads → epic-runner dispatches
  implementers in the implementation worktree
- Per repo: ONE worktree for spec/plan on docs/<topic>-spec, ONE worktree
  for implementation on feat/<topic>. After review: rebase + FF-merge both to
  main, remove worktrees, delete branches, close beads.
- No PRs. Local merges after review.
- Each repo has its own main; merges are per-repo.
- The producer chunk used /home/tcadmin/workspace/nix-repo-base-flake-parts
  (spec) and /home/tcadmin/workspace/nix-repo-base-impl (impl) as worktree
  paths; mirror that naming per repo (e.g.,
  /home/tcadmin/workspace/nix-overlay-flake-parts-spec and
  /home/tcadmin/workspace/nix-overlay-impl).

**Recommended brainstorm questions to drive toward:**
1. One coordinated chunk for all three, OR three separate per-consumer chunks?
2. For each consumer, what's the consumer-fixture-equivalent verification
   step? (Producer had `tests/consumer-fixture`; each consumer doesn't ship a
   "consumer of itself" fixture but it should at least update its own
   `nix flake check` to exercise everything.)
3. Should we DELETE the now-orphan `phillipgreenii-nix-base.flakeModules.flox-overlay`
   from the producer? It has no in-workspace consumer (only flagged
   "keep for symmetry" — but if no consumer is added in the foreseeable
   migration, it's dead code).
4. For nix-personal's overlay sites, do we use `self.overlays.X` (preferred,
   uses the producer's module-contributed overlay) OR direct
   `inputs.X.overlays.default`? (Both work; module-contributed is more
   consistent.)
5. Should homelab also adopt the Shape B `homeModules.install-metadata`
   pattern, or does homelab not declare install-metadata? (Check
   homelab/nix/flake.nix to confirm — it may not even need this.)
6. Cross-consumer follows-alignment: when nix-personal consumes nix-overlay,
   what `inputs.phillipgreenii-nix-overlay.inputs.X.follows = "X"` entries
   are needed for the heavy inputs? Producer README documents the pattern; the
   alignment check fires on any miss.

**Out of scope (do NOT include):**
- Any new producer-side work (the producer chunk is closed; if a NEW
  producer gap surfaces, file a separate bead — don't fold into consumer
  migration).
- Tag/release strategy for nix-* repos — explicitly rejected per memory
  feedback-pin-is-the-version.
- Any UL_LIB_VERSION-style runtime version-guards — same memory.
- Renaming `phillipgreenii-nix-base` input — decided against during producer
  brainstorm.
- The orphan flox-overlay module's deletion is a Q above, NOT a foregone
  conclusion.

**Output expected:**
- One or three spec(s) under each consumer repo's
  `docs/superpowers/specs/2026-06-19-<topic>-design.md` (path may differ per
  repo's existing convention — check ls of their docs/superpowers/ first)
- One or three corresponding plan(s)
- Subagent reviews applied
- Beads created via plan-decomposer (epic + per-consumer task tree)
- Implementer subagents dispatched via epic-runner; merge gates left for
  human review
- Each consumer: rebase + FF-merge to its own main when ready, then update
  any DOWNSTREAM repo's pin in lock-step (e.g., when nix-personal lands,
  homelab's `flake.lock` reference to nix-personal needs `nix flake update`)

**Auto-memory entries that apply (read before starting):**
- feedback-use-worktrees — stash/push from dirty main forbidden; worktree
  per chunk
- feedback-useglobalpkgs-true — HM modules must never set
  nixpkgs.config/overlays; consuming flake owns at system level (especially
  relevant for nix-personal's darwinConfigurations/nixosConfigurations)
- feedback-grep-not-canonical-consumers — workspace grep is a snapshot;
  out-of-workspace consumers may exist; don't vendor-and-delete on grep alone
- feedback-pin-is-the-version — for HEAD-consumed internal APIs, no
  LIB_VERSION constants
- feedback-grep-nested-and-dotted — when auditing Nix option consumers, check
  both `foo.bar = true` AND `foo = { bar = true; };` forms

**First-touch checklist for the brainstorming agent:**
- Read producer README + spec + plan (paths above)
- Read each consumer's current flake.nix to confirm exact lib call sites
- Run `bd show tc-zt0hh` and `bd show tc-rzgzq` to understand nix-overlay's
  in-flight context
- Then start the brainstorm flow per superpowers:brainstorming
```

---

## Notes for whoever runs the next session

- The producer chunk is fully merged + closed; no follow-up work on
  nix-repo-base itself is in scope unless this brainstorm surfaces a gap.
- The recommended `homelab → nix-overlay → nix-personal` order means
  homelab is the lowest-risk first migration to validate the producer's
  consumer story end-to-end. If anything is off in the producer's API,
  homelab will surface it cheaply.
- The producer-side `consumer-input-alignment` check is your safety net —
  it fires the moment a consumer imports an overlay module without
  declaring the matching input, or has follows-misalignment with a
  downstream flake.
