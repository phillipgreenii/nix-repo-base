# Triad Follow-Ups — Next Session Prompt

**Date:** 2026-06-22 (handoff from the consumer-migration triad)
**Prior handoff:** `2026-06-19-consumer-migrations-prompt.md` (now consumed; triad complete)

## What just finished

The three-chunk consumer-migration triad onto `phillipgreenii-nix-base` flake-parts modules is done:

| Chunk    | Repo           | Status                                 | Lock delta               |
| -------- | -------------- | -------------------------------------- | ------------------------ |
| tc-0nze2 | `homelab`      | ✓ merged 2026-06-20 @ 18547951b        | 99 → 85 nodes            |
| tc-jdt36 | `nix-overlay`  | ✓ merged 2026-06-21 (+ tc-xbxex)       | 26 → 13 nodes            |
| tc-r8brx | `nix-personal` | ✓ merged 2026-06-21 @ 3f79ec4 (pushed) | **71 → 36 nodes (-49%)** |

Mid-triad cleanup landed on 2026-06-22: `.perles/` is now `.gitignore`d in homelab (PR #165, awaiting merge), nix-agent-support, nix-repo-base, nix-personal, ha-addon-esphome-mcp; the triad spec/plan docs are committed to `nix-personal/docs/superpowers/`; the stale status-bar handoff was removed from nix-personal.

Lessons recorded as bd remember entries:

- `heavy-input-sub-follows-needed-for-alignment`
- `cross-flake-consumer-input-follows-table-pattern`
- `lock-alignment-converges-over-multiple-follows-passes`
- `mid-gate-fix-stack-three-ceiling-confirmed`
- `cx-scripts-not-top-level-package-in-nix-personal`
- `reformat-scatter-depends-on-local-treefmt-nix-presence`
- `lock-node-reduction-magnitude-pattern-triad`
- `gate-bead-verifier-label-propagated-tc-r8brx`
- `multi-site-overlay-injection-requires-explicit-enumeration`
- `producer-generated-file-excludes-default-noop-consumers`

Query with `bd memories <keyword>`.

## What's left (recommended order)

### 1. tc-7j2u6 — homelab pin-bump phillipgreenii-nix-personal (P4) — RECOMMENDED FIRST

Direct natural successor to the triad: homelab still pins the pre-migration nix-personal rev. Bump the pin so homelab consumes the post-migration version, and drop any now-dead follows entries on `phillipgreenii-nix-personal.inputs.*` in `homelab/nix/flake.nix`.

**Scope:** single small commit — `nix flake lock --update-input phillipgreenii-nix-personal` (or full `nix flake update` if other inputs also benefit), audit follows, verify `cd nix && ./check.sh` + `nix eval .#nixosConfigurations.monorepod.config.system.build.toplevel.drvPath` (use the same pattern that surfaced the tc-0nze2 incident — don't trust `nix flake check` alone; per-machine eval too).

**Workflow:** homelab is no-direct-push-to-main; use the standard `chore/<topic>` feature branch + `bin/forgejo-api create-pr` flow.

**Pre-flight gotchas (from the triad lessons):**

- Run the same heavy-input-follows alignment check (`jq -r '.nodes | keys[] | select(test("^(phillipgreenii-nix-base|flake-parts|nixpkgs|nixpkgs-unstable|llm-agents|nix-vscode-extensions)(_[0-9]+)$"))' nix/flake.lock` should be empty). If `_2`/`_3` duplicates appear, the same fix pattern as `tc-phouv` applies — add the missing follows.
- The remote nix builder may be out of disk again — see `bd memories nix-remote-builder-disk-full-workaround-builders-empty` if `nix flake check` fails with ENOSPC.

### 2. tc-fe3v1 — Layer B: close-time evidence gate for `kind:verify` beads (P1 BUG)

The institutional lesson from the tc-0nze2 incident (gate beads mislabeled `agent:implementer` → false-green → broken code one step from main) was applied at the **decomposer** level (tc-oqojv, closed: kind:verify beads now stamp `agent:code-verifier`). Layer B is the **close-time** gate: epic-runner / auto-orchestrator should refuse to close a `kind:verify` bead whose close reason doesn't cite real evidence (specific command output, derivation paths, log excerpts).

**Scope:** `development/agent-support/sp-bd-bridge/agents/epic-runner.md` + `auto-orchestrator.md` — add a pre-close check that for any `kind:verify` bead, the `--reason` must contain at least one of: a `/nix/store/` derivation path, an explicit `PASS`/`FAIL`/`green` line per Done Criterion in the bead description, or operator-supplied evidence. Fail-open is unacceptable: refuse the close and surface the bead back to the operator if evidence is missing.

**Why this matters:** memory `epic-runner-sp-bd-bridge-green-complete-claims` documents tc-jdt36's epic-runner reporting "all gates passed" while 3 real failures lurked (tc-xbxex). This Layer B gate is the structural fix.

**Workflow:** homelab feature branch + PR. Touches sp-bd-bridge agent definitions only.

### 3. tc-iv7vz — cmux APFS DMG unpacking fails (P1 BUG)

cmux ships APFS DMGs; nix-overlay's package uses `pkgs.undmg` which only handles HFS. Fix is mechanical per memory `cmux-ships-apfs-dmgs-pkgs-undmg-only-handles`: swap `undmg` for `pkgs._7zz` in the unpackPhase of nix-overlay's `packages/cmux/default.nix`. Surfaced 2026-06-17 when chunk 3 first put cmux into CI.

**Scope:** single-file change in nix-overlay. Verify locally with `nix build --no-link .#packages.x86_64-darwin.cmux` (requires aarch64-darwin/x86_64-darwin support; if not available locally, defer the build verification to CI).

**Workflow:** nix-overlay allows direct ff-merge to main (per triad pattern). No PR needed.

### 4. tc-olcz3 — ensure consistent repo config for all nix-\* repos (P2)

Bigger sweep — audit all 5 nix-\* repos (nix-repo-base, nix-overlay, nix-personal, nix-agent-support, plus homelab as consumer) for config drift: `.gitignore` patterns, `.beads/config.yaml` shape, pre-commit hook lists, treefmt config. The `.perles/` gitignore cleanup just done is a small slice of this. Decompose into a spec/plan first via the normal `/tc-plan` flow.

**Workflow:** this is multi-repo; same triad-style plan-then-decompose-then-execute pattern.

---

## Suggested entry

```text
read docs/superpowers/handoff/2026-06-22-triad-followups-prompt.md and continue
```

Or pick a specific bead directly: `bd show tc-7j2u6` then proceed.

## Operational reminders

- All repos now have `.perles/` gitignored. Don't re-add it.
- Triad spec/plan docs are at `nix-personal/docs/superpowers/{specs,plans}/2026-06-19-flake-parts-consumer-migration*.md` for reference.
- Lock-delta artifact for nix-personal is at `/tmp/nix-personal-impl-lock-delta.md` (may be gone after a reboot — re-derive from git if needed).
- The tc-r8brx impl worktree (`/home/tcadmin/workspace/nix-personal-impl`) was removed; feat branch deleted; work landed on nix-personal/main and pushed.
- Homelab PR #165 (`chore: gitignore .perles/`) is the only outstanding non-merged change from this session — it's awaiting CI + merge.

## Anti-recommendations

- Don't re-open or re-do any of the closed triad beads (tc-0nze2, tc-jdt36, tc-r8brx, tc-phouv, tc-xbxex, tc-neh26, tc-mhhb9, tc-oqojv, tc-uergy). Audit trail in their close reasons is sufficient.
- Don't dispatch `epic-runner` / `auto-orchestrator` for verification-heavy work until tc-fe3v1 lands — they still false-green. Run gates inline or via `homelab-agents:code-verifier` with operator re-verification.
- Don't add `_module.args.checksHelpers` follows or anything similar to the spec table without first running a `nix flake update` + alignment check on a sample consumer — the triad's lesson is that **lock-alignment is a probe**, not something planning can predict perfectly.
