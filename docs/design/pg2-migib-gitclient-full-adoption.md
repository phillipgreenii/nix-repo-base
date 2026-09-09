# Design: should `pn` fully adopt `x/gitclient`?

> Status: **DECIDED (Phillip, 2026-09-03) — see Section 7a.** Sections 1-7 are the original design
> exploration and are kept for their evidence and reasoning; Section 7a is the binding decision and
> supersedes Section 7's recommendation. Written for bead `pg2-migib`, the deferred "full adoption"
> half of `pg2-app6l` (2026-08-29: "adopt read-side now, full design pass filed separately and
> deliberately sequenced after"). Implementation is filed as its own follow-up work, per this
> bead's own scope note ("do not implement without a separate ruling") — the ruling now exists;
> the implementation does not yet.

## 1. Background

`x/gitclient` (design of record: the `DESIGN` field of epic `pg2-svfbb`) ships role-segregated git
interfaces (`Locator`, `RefReader`, `StatusReader`, `HistoryReader`, `Fetcher`, `WorktreeManager`,
`BranchManager`, `Cleaner`) plus one hermetic CLI-backed `Client`. Its whole reason for existing is
a real incident class: git's `GIT_DIR`/`GIT_INDEX_FILE` env vars outrank `-C <dir>` and `cmd.Dir`
inside a linked-worktree hook, and nine leak incidents across three repos in two days showed that
caller discipline alone cannot prevent it. `gitclient`'s answer is **D2**: the target directory and
the child environment are fixed at construction (`Client` is anchored, never per-call), and the
environment is an allowlist, never inherited wholesale.

Nine leaf-app migrations (ccpool, pr-pool x3, pg-pr x3 composite, CETA gh resolver, pa-monitor,
pg-go-mutate-tui) landed clean under epic `pg2-svfbb`, with **zero** new `GIT_DIR`-leak incidents
and no interface churn — even under pg-pr's 6-role composite case. On that evidence, `pg2-app6l`
(closed 2026-08-29, operator decision) split pn's own adoption in two:

- `pg2-oxle0` — migrate pn's **read-only** call sites (`Locator`/`RefReader`/`StatusReader`) now.
  Landed on `phillipg-nix-repo-base` main at `b59a794` (2026-09-02).
- `pg2-migib` (this bead) — a **separate design pass** for pn's mutating verbs, streaming,
  per-invocation config, and new roles, deliberately sequenced after the read-side migration so
  its outcome can inform this design. That is the explicit reason this bead exists rather than
  being folded into `pg2-oxle0`.

pn is explicitly called out in the epic design (section 4.5) as a **validation consumer only**: its
git surface (17 production files, several times the rest of the workspace combined) includes
categories no other consumer needs, and pn is "the tool that builds and applies the machines" —
churning its own proven git layer ahead of battle-testing was deliberately avoided.

## 2. What the read-side migration (`pg2-oxle0`) actually found

`pg2-oxle0`'s close reason reports a clean landing: `CommonDir`, `CurrentBranch`, `HasUpstream`,
`RefExists`, `RemoteURL`, and `Status` were migrated across `doctor_mode.go`, `status.go`,
`push.go`, `rebase.go`, `workforest.go`, and `init.go`, with all gates green (build/vet,
unit+integration+smoke+contract+hostile, prek, `nix build`, repo-wide `nix flake check` run
twice). No comments are recorded on the bead beyond that close reason.

Reading the actual landed code turns up something the close reason doesn't say in so many words:
**the migration was selective, and it left several read-only call sites on the raw runner because
no role method matched them exactly.** This is not a defect in `pg2-oxle0` — the bead's own scope
note only claims sites that "map onto a role method exactly" — but it is real, concrete evidence
about how well `gitclient`'s current interface shapes fit pn's needs, which is exactly what this
design pass is supposed to weigh:

- **`modules/pn/internal/workspace/status.go:283-293` (`deltaArrows`)** still calls raw
  `git rev-list --left-right --count <ref>...<base>` to get ahead **and** behind in one call.
  `gitclient.RefReader.CommitsAhead(ctx, base, tip)` only computes one direction
  (`rev-list --count base..tip`); getting both would cost two subprocess spawns instead of one.
  Left unmigrated.
- **`modules/pn/internal/workspace/status.go:297-309` (`localBranches`)** still calls raw
  `git branch --format=%(refname:short)` to list local branches. There is no `BranchLister` role
  in `gitclient` today — `BranchManager` only has `DeleteBranch`. Left unmigrated (this is the
  concrete production call site behind the bead's "branch-lister" gap).
- **`modules/pn/internal/workspace/doctor_mode.go:52-61` (`gitRevParse`)**, which calls raw
  `git rev-parse --git-dir`, carries an explicit in-code note: _"`--git-dir` has no x/gitclient
  equivalent (bead pg2-oxle0's scope is Locator/RefReader only), so it stays here unmigrated."_
  `Locator` has `CommonDir` (`--git-common-dir`) but not plain `--git-dir`; the two differ for a
  linked worktree, and `workspaceMode`'s worktree-vs-primary detection needs both.
- **`modules/pn/internal/workspace/remotes.go:12-55` (`readGitRemotes`)** still calls raw
  `git remote -v` to enumerate every configured remote. `Locator.RemoteURL(ctx, remote)` takes one
  named remote; there is no "list all remotes" role method.

So the honest read of "friction" is two-layered: **zero** friction in the sense the operator's
evaluation cared about (no leak incidents, no churn in the interfaces that WERE used), but a
real, recurring pattern of **interface-granularity mismatches** on the read side — one-direction
vs. two-direction counts, single-remote vs. all-remotes, and a missing plumbing-level method
(`--git-dir`). Four such mismatches surfaced in a migration scoped to only six read-only role
methods. That ratio matters for sizing the mutating-side work below: the mutating verbs pn needs
(conflict-aware rebase, streaming push/rebase, per-invocation `-c` config, worktree/branch
listing) are, if anything, more bespoke than `CommonDir`/`RemoteURL`, so a similar or higher rate
of "doesn't quite fit, needs its own new shape" should be expected, not treated as a rounding
error.

## 3. What `gitclient` already has, and what it actually lacks

It's worth being precise here, because the bead's framing ("does gitclient grow mutating verbs")
undersells how much mutating surface already exists. Reading
`phillipgreenii-x/gitclient/mutate.go` and `client.go` directly:

**Already implemented (mutating):** `Fetcher.Fetch` (`git fetch`, with a safe-by-default
`--no-prune`), `WorktreeManager.CreateWorktree` (`worktree add -b|-B`), `RemoveWorktree`,
`PruneWorktrees`, `BranchManager.DeleteBranch` (`branch -d|-D`), `Cleaner.ResetHard` (`reset
--hard`), `CleanUntracked` (`clean -fd`). Notably, `CreateWorktree` already models exactly what
`modules/pn/internal/workspace/workforest.go:167-188,283-383,522-544` does today
(`git worktree add [-b|-B] <path> <branch> [<startpoint>]`) — this is a real, already-available
migration target that doesn't require _any_ new gitclient feature, modulo the streaming gap below.

**Genuinely absent (confirmed by reading `gitclient/client.go` and `options.go` directly, not just
the design doc's own gap list):**

- **Streaming.** `Client.run` (`client.go:262-294`) always writes into two `bytes.Buffer`s
  (`cmd.Stdout = &stdout; cmd.Stderr = &stderr`, `client.go:277-278`) and returns `[]byte`. There
  is no live-writer option anywhere in the package. Every mutating call in pn today wires a live
  sink: `rebase.go:68,82,85`, `push.go:330,353`, `clone.go:56-58,69`, `propagate.go:190-195`, and
  `workforest.go:187,383,522,544` all pass `exec.RunOptions{Stdout: out, Stderr: out}` from pn's
  own `internal/exec.Runner` (`modules/pn/internal/exec/exec.go:33-39`) so the operator watching a
  `pn workspace rebase`/`push`/`apply` sees progress live, not after the fact.
- **Per-invocation config.** `options.go` has no `WithConfig(key, value string)` or any per-call
  `-c` mechanism — `config` (the struct `Option`s populate) is built once, at construction, and
  baked into `envParsed`/`envUnparsed` (`client.go:41-49,101-108`). pn needs exactly one
  per-invocation `-c`: `updatecache.go:88` runs
  `git -C <dir> -c core.fsmonitor=false status --porcelain` so the health-check probe never
  queries a wedged `git fsmonitor--daemon`. **This is a narrower, safer kind of "per-call" than
  the one D2 forbids.** D2's constraint is about the _directory_ and the _environment_
  (`cmd.Env`/`cmd.Dir`) — the exact vector that let `GIT_DIR` outrank `-C <dir>` in the original
  incidents. A per-call `-c key=value` is an **argv** concern (`git -c core.fsmonitor=false ...`),
  not an env-var concern, and touches none of the allowlist machinery `gitclient`'s hermeticity
  rests on. Adding it would not reopen the leak class D2/D5 exist to close — but it is still a new
  per-call knob threaded through every mutating method's signature (or a variadic `Option`-like
  parameter on `Run`/each verb method), which is real, non-trivial API surface, not a one-line
  patch.
- **`Syncer` (conflict-aware pull/rebase `--autostash`).** No such role exists. Building one is
  more than "wrap `pull --rebase --autostash`": `gitclient/classify.go`'s error mapping is
  deliberately **exit-code-driven, never stderr-text-driven** (design §4.4: "Error classification
  keys on exit codes, never on localized stderr text" — this is _why_ `LC_ALL=C` is scoped the way
  it is). `git rebase`/`pull --rebase` exit non-zero (typically 1) for a merge conflict AND for
  several other failure modes, so a conflict can't be told apart from a generic failure by exit
  code alone. The `StatusReader.Status` role (already implemented, already parses
  `StatusUnmerged`/`'U'` entries per `types.go`) gives a workable path — probe status after a
  failed rebase and look for unmerged entries — but that is a genuine two-role composition plus a
  new sentinel (`ErrRebaseConflict` or similar) plus a new "probe after failure" call pattern that
  doesn't exist anywhere in `gitclient` today. `modules/pn/internal/workspace/rebase.go:40-86`
  is exactly this shape today (fetch, then pull --rebase --autostash / rebase --autostash, per
  repo, in topological order, with live output).
- **`Committer`, `Pusher`, `Clone`-as-constructor, `BranchLister`.** None exist.
  `modules/pn/internal/workspace/propagate.go:171,178,190-195` (`checkout --`, `add`, `commit -m`),
  `push.go:242-354` (`push` / `push -u <remote> <branch>`, plus a `resolvePushRemote` chain of
  raw `config --get` reads at `push.go:88-163` that a `Locator`-shaped read could partly but not
  fully replace — it reads `branch.<b>.pushRemote`, then local, then global
  `remote.pushDefault`, none of which `Locator.RemoteURL` covers), and `clone.go:29-70` (`clone
--branch ... -- <url> <dir>`, plus `remote add`) all need a role with no `gitclient` counterpart
  today.
- **A richer failure taxonomy** generally (per `pg2-app6l`'s evaluation point 3) — today's
  taxonomy is three named sentinels (`ErrNotARepository`, `ErrDetachedHEAD`, `ErrNoRemote`) plus
  a generic `*GitError{Args, ExitCode, Stderr}`. Conflict detection, "nothing to commit", "already
  up to date", and similar states pn's mutating call sites currently branch on by inspecting
  output text would each need their own sentinel or a documented `*GitError` inspection contract.

**A genuine benefit `gitclient` already has that pn's own runner lacks:** `Client.run`
(`client.go:249-274`) puts the child in its own process group (`Setpgid: true`) and kills the whole
group on context cancellation, so a hook-spawned grandchild can't survive a canceled/deadlined
call. pn's own `internal/exec.realRunner.Run` (`exec.go:70-127`) has no equivalent — it relies on
plain `exec.CommandContext` default cancellation, which only kills the direct child. This is a real
safety property pn would pick up by routing more calls through `gitclient`, independent of how the
mutating-role question is resolved.

## 4. The structural tension: two Runner shapes that don't nest cleanly

pn already has its own general-purpose subprocess abstraction,
`modules/pn/internal/exec.Runner` (`exec.go:20-40`), with `RunOptions{Dir, Env, Stdin, Stdout,
Stderr}` — **per-call** directory, **per-call** env, and live-writer streaming already built in —
plus `WorkerPool` (`workerpool.go:11-49`), which fans a bounded worker count out across many
repos concurrently, forwarding each `Run` to the same underlying runner. This is not a naive
`exec.Command` sprinkled everywhere; it's a considered, already-tested abstraction that happens to
model the exact "per-call dir" shape `gitclient`'s D2 was written to reject.

That means growing `gitclient` to cover pn's mutating verbs doesn't just mean "add methods" — it
means reconciling two different ownership models for the same information (target directory,
child environment, live output sink):

- `gitclient.Client`: directory + environment anchored once at construction; one `Client` per
  target directory; no live-writer parameter on any method.
- pn's `exec.Runner`: directory + environment + live writer supplied fresh on every call; one
  `Runner` (or `WorkerPool`) shared across every repo pn touches.

pn's read-side migration already resolved this tension for reads via an app-local **opener seam**
(`modules/pn/internal/workspace/gitclient.go:28-41`, `gitOpener`) — a `func(ctx, dir)
(gitReader, error)` that constructs a fresh `*gitclient.Client` per target directory, matching
design section 4.6 exactly. That pattern scales fine for reads, which are cheap, synchronous, and
don't need a live sink. It is a materially bigger ask for pn's mutating, streaming,
worker-pool-fanned-out call sites: every `rebase.go`/`push.go`/`workforest.go` call already
carries a live `io.Writer` (`out`) through the exact same call that would need to construct (or
look up a cached) `*gitclient.Client`, and pn currently processes many repos **concurrently**
(`WorkerPool`) while `gitclient.Client` has no notion of a shared pool — each client is a
same-directory singleton.

## 5. The design question

Should `x/gitclient` grow: (a) a live-writer/streaming call shape, (b) a per-invocation `-c`
config mechanism, and (c) new roles (`Syncer` with conflict-aware errors, `Committer`, `Pusher`,
`Clone`-as-constructor, `BranchLister`) — so that pn can eventually migrate its mutating verbs
onto it too? Or should pn's mutating git surface stay on its own `internal/exec.Runner`
permanently, with `gitclient` adoption capped at the read-side roles already migrated (plus,
optionally, the already-available `WorktreeManager.CreateWorktree` for `workforest.go`)?

**Settled, not a live question in this design:** `fsmonitor--daemon` process management
(`modules/pn/internal/workspace/apply.go:142-147,189-208` — `restartFsmonitorDaemon`'s `pkill -f
"git fsmonitor--daemon"` and `stopFsmonitorDaemon`'s `git -C <dir> fsmonitor--daemon stop`) stays
pn-local permanently regardless of which option below is chosen. It's git-subsystem process
lifecycle management, not a repository-state verb `gitclient`'s role model represents, and the
epic design and this bead's own text both already say so.

## 6. Options

### Option A — Full adoption: grow `gitclient` with streaming, per-invocation config, and the new roles

Add a live-writer call shape (either a per-call functional option threaded through `Run` and each
mutating role method, or a parallel "streaming" variant of each), a `WithConfig(key, value
string)`-style per-invocation `-c` mechanism, and the `Syncer`/`Committer`/`Pusher`/`Clone`/
`BranchLister` roles, then migrate pn's mutating call sites onto them.

**Pros:**

- pn picks up the process-group-kill-on-cancel safety property (§3) that its own runner lacks
  today, for every migrated call — a real correctness improvement, not just tidiness.
- One hermetic, tested git-invocation implementation across the whole workspace instead of two
  (pn's `internal/exec` + `gitclient`), which is the whole point of extracting `gitclient` in the
  first place.
- The read-side migration proved the _role-interface_ pattern holds up under real use without
  churn — that evidence transfers to new roles built the same way (small, YAGNI-scoped, one
  method per real call site).

**Cons / real costs, grounded in what was found above:**

- Streaming is not a small add: `gitclient`'s public methods have fixed signatures
  (`Fetch(ctx, opts) error`, `CreateWorktree(ctx, path, branch, opts) error`, ...) with no writer
  parameter anywhere; adding one changes the signature of every mutating role method — which
  breaks every existing app-local fake across all nine already-migrated leaf-app consumers, even
  the ones that never stream, per D4/§4.6 (fakes are hand-written per app). This is a
  workspace-wide compatibility question `pg2-migib`'s scope (pn-only) cannot resolve alone.
- The `Syncer` role needs new machinery `gitclient` has deliberately avoided so far (a
  post-failure `StatusReader` probe plus a new sentinel), not just a new method wrapping one git
  verb — see §3.
- pn's `WorkerPool`-fanned-out, many-repos-concurrently execution model (§4) has no counterpart in
  `gitclient`, which is one `Client` per one anchored directory. Reconciling that is design work
  this bead surfaces but does not resolve.
- The read-side migration's own four left-behind call sites (§2) show `gitclient`'s role-method
  granularity doesn't automatically match pn's actual needs; expect the same "the role exists but
  the shape is wrong" pattern on the mutating side, likely worse (conflict-aware rebase and
  bidirectional ahead/behind are harder to model than "list all remotes").
- Widens the blast radius of any `gitclient` bug: today a defect in `Fetch`/`CreateWorktree`/etc.
  affects nine leaf apps; growing it to cover pn's build/apply path means the SAME code path now
  also runs underneath the tool that builds and applies the machines — precisely the risk the
  epic design called out in excluding pn from Phase 2 in the first place, and precisely why
  `pg2-app6l` treated pn's full adoption as needing its own risk-managed design rather than a
  mechanical migration.

### Option B — pn's mutating git surface stays local permanently; cap adoption at the read-side

Keep `pg2-oxle0`'s landed read-side migration as the ceiling. All mutating verbs (`rebase.go`,
`push.go`, `clone.go`, `propagate.go`, `workforest.go`'s worktree mutations, `updatecache.go`'s
`-c fsmonitor` probe) stay on pn's own `internal/exec.Runner`/`WorkerPool` indefinitely. No further
`gitclient` role growth is driven by pn's needs.

**Pros:**

- Zero new risk to the tool that builds and applies the machines. No new shared-package surface
  area, no signature-breaking changes rippling into nine already-landed leaf-app consumers.
- pn's `internal/exec.Runner` already does everything pn's mutating call sites need today
  (per-call dir/env, live streaming, worker-pool fan-out) — it is not a gap being papered over,
  it's a considered abstraction that predates `gitclient` and already fits pn's actual shape.
- Keeps the "battle-test in leaf apps first, pn adopts only what's proven" posture the epic design
  explicitly chose, indefinitely rather than just until this bead.

**Cons:**

- pn never picks up the process-group-kill safety property (§3) for its mutating calls — the one
  concrete correctness gap in pn's own runner that adopting `gitclient` would close.
- Leaves pn permanently split across two git-invocation code paths (raw runner for mutations, thin
  `gitReader` for reads) rather than converging on one, so a future git-env-safety fix (an "epic
  10") would need to be applied to pn's runner separately from `gitclient`.
- `gitclient`'s already-implemented `WorktreeManager.CreateWorktree` — which already matches
  `workforest.go`'s `worktree add -b|-B` call shape exactly (§3) — goes unused for no reason other
  than "no further adoption," even though nothing about it requires new `gitclient` work.

### Option C — Middle ground: adopt what already fits; defer (don't design now) the rest

Migrate pn's mutating call sites that `gitclient` can _already_ satisfy without any new feature —
concretely, `workforest.go`'s `worktree add [-b|-B]` calls onto `WorktreeManager.CreateWorktree`,
and `rebase.go`'s plain `git fetch` onto `Fetcher.Fetch` — accepting the loss of live streaming on
just those calls (or keeping them on the raw runner solely for the streaming pass-through, calling
`gitclient` for the rest of the logic). Leave everything that needs a genuinely new `gitclient`
feature (streaming, per-invocation config, `Syncer`, `Committer`, `Pusher`, `Clone`, `BranchLister`)
on pn's own runner, undesigned, until a concrete second consumer (not just pn) demonstrates the
need — consistent with the epic's own YAGNI stance ("no method exists without a current
consumer").

**Pros:**

- Captures the free win (`CreateWorktree` needs no new `gitclient` code) without taking on any of
  Option A's shared-signature risk.
- Doesn't force a decision on the hard cases (`Syncer`, streaming) before a second real consumer
  makes the right shape clearer — avoiding pn's build/apply path being the FIRST and ONLY driver
  of a brand-new role's design, which is exactly the situation the epic design tried to avoid by
  deferring pn in the first place.

**Cons:**

- Splits pn's `WorktreeManager`/`Fetcher` calls onto `gitclient` while everything else stays on the
  raw runner — a third code shape (read-only via `gitReader`, some mutations via `gitclient`, most
  mutations via raw `Runner`) that is arguably harder to reason about than either Option A or B's
  cleaner split, for a small, one-time payoff (two call sites).
- If streaming genuinely can't be dropped for `CreateWorktree`/`Fetch` in pn's UX (the operator
  watching `pn workspace workforest add`/`rebase` progress), this option collapses back into
  "nothing changes" for those calls, and the "already fits" win shrinks to zero.

## 7. Recommendation (not a decision) — SUPERSEDED by Section 7a's operator decision

**Recommendation: Option B, with the Option C worktree-create migration as an optional low-risk
add-on the operator can take or leave — not Option A.**

The read-side migration's own evidence (§2) is the deciding fact: even limited to six read-only
role methods, four production call sites needed a shape `gitclient` didn't have (bidirectional
ahead/behind, all-remotes listing, plain `--git-dir`, branch listing) and were correctly left on
the raw runner rather than force-fit. The mutating verbs pn needs are, if anything, harder to model
well up front — `Syncer`'s conflict-aware error handling in particular requires new machinery
`gitclient` doesn't have anywhere else in the package (§3), meaning pn's build/apply path would be
the sole, first driver of that role's design — the exact "churning pn's proven git layer before
extract-then-migrate has battle-tested the shape" risk `pg2-svfbb`'s design and `pg2-app6l`'s
decision were both explicit about avoiding. Streaming compounds this: it is a signature change
across every mutating role method, rippling into the fakes of nine already-landed, otherwise-done
leaf-app migrations, for a benefit (pn's UX) none of those nine consumers asked for.

pn's own `internal/exec.Runner`/`WorkerPool` (§4) is not technical debt standing in for a missing
`gitclient` feature — it is a working, tested abstraction that already has the shape (per-call
dir/env, live streaming, concurrent fan-out) pn's mutating call sites actually need, and it
predates `gitclient` by design intent, not by accident. The one real gap it has relative to
`gitclient` — no process-group kill on cancellation (§3) — is worth fixing, but it is a fix to
pn's _own_ runner (port the `Setpgid`/negative-pid-kill pattern from `gitclient/client.go:249-274`
into `internal/exec/exec.go`'s `realRunner.Run`), not a reason to route pn's mutations through
`gitclient`.

If the operator wants a middle path rather than a flat "stays local permanently," the lowest-risk
piece to carve out is `workforest.go`'s `git worktree add [-b|-B]` calls onto the
already-implemented `WorktreeManager.CreateWorktree` (Option C) — it needs zero new `gitclient`
work and only loses live streaming on that one call family (worktree creation is typically fast;
the streaming need is much more acute for `rebase`/`pull`/`push`, which would stay local either
way). That is a small, separately-schedulable follow-up, not a reason to reopen this design.

## 7a. OPERATOR DECISION (Phillip, 2026-09-03) — SUPERSEDES the Recommendation above

**Option A adopted — full mutating-side adoption — but scoped narrowly to exactly what pn's real
call sites need, not the open-ended "grow gitclient with streaming/config/new roles" framing
Section 5 posed.** This section is the binding decision; Section 7's "Recommendation: Option B"
above no longer applies. Reached via extended live design review (this session), which read pn's
actual mutating call sites (`rebase.go`, `push.go`, `propagate.go`, `clone.go`, `workforest.go`,
`apply.go`), the existing `gitclient` implementation (`client.go`, `read.go`, `mutate.go`,
`options.go`, `argv.go`), and iterated the interface shapes against that evidence rather than
against the abstract role names in Section 6.

### Resolved without any new mechanism

- **`core.fsmonitor=false`** (Section 3's per-invocation-config gap): hardcode it into
  `statusArgs()` (`argv.go`) unconditionally — `Args: []string{"-c", "core.fsmonitor=false",
"status", "--porcelain=v1", "-z"}`. Every `StatusReader.Status` call, for every consumer, always
  disables fsmonitor. Safe because it's a pure performance knob with no correctness downside (the
  existing comment already measured fsmonitor-off `status` as "well under a second" on these
  repos), so there's no reason any consumer would want it configurable. No `WithConfig` option, no
  per-call override, no per-instance construction needed — this eliminates that entire discussion.
- **`PREK_ALLOW_NO_CONFIG`**: **NOT built into gitclient in any form** — no env-override
  mechanism, no parameter on `Committer.Commit`, nothing. It is a workaround for prek being
  misconfigured in pn's `.workforests/.pn-update` ephemeral worktree (which lacks
  `.pre-commit-config.yaml` — a gitignored dev-shell symlink present only in canonical clones),
  and the correct fix is on pn's side: symlink the config into that worktree at creation time,
  exactly as `workforest.go`'s `gitWorktreeAddOne` already does for coordinated sets. This is a
  pn-side follow-up, independent of gitclient adoption.

### The `Handle` mechanism — scoped to exactly where pn needs streaming, nothing else

Every mutating verb pn actually calls streams live output to the operator's terminal today
(`rebase.go`/`push.go`/`workforest.go`/`propagate.go`'s commit all wire
`exec.RunOptions{Stdout: out, Stderr: out}`, which resolve to `cmd.OutOrStdout()`/`cmd.ErrOrStderr()`
— real terminal output, watched live by a human, consumed by nothing else in pn's own logic). Losing
that means: a header line prints, then silence for however long the step takes, and — since today's
buffered `Fetch` discards captured stdout even on success — literally zero confirmation anything
happened on success. That live-progress behavior is worth preserving, so streaming needs to be a
real capability, but **only for the verbs pn actually calls with a live sink**, per the package's
own "no method exists without a current consumer" rule (`interfaces.go` header).

Mechanism (new type, `handle.go`):

```go
// Handle represents a running git invocation. The process starts and runs
// to completion regardless of whether anyone ever calls Wait or
// AttachStream -- only OBSERVING the outcome is optional, never running it.
type Handle struct { /* mutex; separate stdout/stderr buffers (stderr is
    needed by classify() regardless); optional attached writer pair; a
    done channel; the final classified error */ }

// AttachStream may be called at any time -- before, during, or after the
// invocation completes. It first flushes everything already buffered into
// stdout/stderr (the replay), then registers them as the live sink for
// everything still to come, under the same lock -- so nothing is missed
// or duplicated regardless of when it's called.
func (h *Handle) AttachStream(stdout, stderr io.Writer)

// Wait blocks until the invocation completes and returns its classified
// error (same classify()/ctx-error logic run() already has). Safe to call
// more than once or from more than one goroutine.
func (h *Handle) Wait() error
```

Implementation approach: `cmd.Stdout`/`cmd.Stderr` are set to a small internal writer that, under
the Handle's mutex, appends to its buffer and also forwards to the attached writer if one is
registered -- `os/exec`'s own internal copying goroutine (already spun up whenever `cmd.Stdout` is
a non-`*os.File` writer, already joined by `cmd.Wait()`) does the concurrent draining; no manual
pipe/goroutine plumbing needed beyond that. `cmd.Start()` (not `Run()`) is called immediately, and
one internal goroutine is spawned right after to call `cmd.Wait()`, classify the result exactly as
today's `run()` tail does, store it, and close `done` -- this runs whether or not the caller ever
asks, so the process is always reaped (no zombies) and there is no "forgot to consume it, so it
never ran" footgun.

**Scoping — which verbs get `(*Handle, error)` and which stay untouched:**

```go
// CHANGED (existing roles, pn needs to stream them; 9 leaf-app consumers keep using them buffered
// via .Wait()):
type Fetcher interface { Fetch(ctx context.Context, opts FetchOptions) (*Handle, error) }
type WorktreeManager interface {
    CreateWorktree(ctx context.Context, path, branch string, opts CreateWorktreeOptions) (*Handle, error)
    RemoveWorktree(ctx context.Context, path string, force bool) error // unchanged -- no streaming need
    PruneWorktrees(ctx context.Context) error                          // unchanged -- no streaming need
}

// NEW roles (pn-only consumer, so no buffered form is ever needed -- born streaming-shaped):
type Syncer interface { Sync(ctx context.Context, opts SyncOptions) (*Handle, error) }
type Committer interface {
    RestorePath(ctx context.Context, path string) error // checkout -- <path>; no streaming need
    Add(ctx context.Context, paths ...string) error       // add <paths...>; no streaming need
    Commit(ctx context.Context, message string) (*Handle, error) // commit -m <message> -- streams (hook output)
}
type Pusher interface { Push(ctx context.Context, opts PushOptions) (*Handle, error) }
func Clone(ctx context.Context, url, dir string, opts CloneOptions, copts ...Option) (*Client, *Handle, error)

// NEW, read-side, no streaming concept applies:
type BranchLister interface { ListBranches(ctx context.Context) ([]string, error) } // branch --format=%(refname:short)
```

`BranchManager.DeleteBranch`, `Cleaner.ResetHard`, `Cleaner.CleanUntracked` are **untouched** —
nothing needs them streamed today; convert them later only if a real consumer needs it.

### Explicit acceptance bar for the `Handle` implementation (operator requirement, 2026-09-03)

**`Handle` MUST ship with full test coverage, MUST have zero goroutine/process leaks, and MUST be
race-free (`go test -race`).** Concretely, whoever implements this must cover at minimum: the
buffer-then-replay-then-live-sync correctness of `AttachStream` called before, during, and after
completion (no bytes missed, none duplicated); `Wait()`'s error classification is unchanged from
today's `run()`; concurrent calls to `Wait()` and/or `AttachStream()` from multiple goroutines; the
"never call Wait or AttachStream at all" case still reaps the process (no zombie, no leaked
goroutine); and context cancellation/timeout while a stream is attached mid-flight. This is a
hard gate on the implementation, not a nice-to-have.

### Known follow-on costs (not resolved here, but must be scoped by the implementation work)

- **All 9 existing leaf-app consumers' call sites for `Fetch`/`CreateWorktree` change** — from
  `err := x.Fetch(ctx, opts)` to `h, err := x.Fetch(ctx, opts); ...; err = h.Wait()`. Real,
  mechanical edits across ccpool, pr-pool (×3), pg-pr (×3 composite), CETA's gh resolver,
  pa-monitor, pg-go-mutate-tui — not confined to this repo.
- **A new testing problem**: a fake `Fetcher`/`WorktreeManager` can no longer return a plain
  `error` — it needs to return something `*Handle`-shaped. This needs either an exported
  `HandleLike` interface (`Wait`/`AttachStream`) that `Fetch`/`CreateWorktree` return instead of
  the concrete `*Handle`, or a small test-helper package with a fake-Handle constructor. This is
  new API surface that has to be designed as part of implementation, not assumed away.
- **`Clone`'s constructor mechanics**: `setupAnchoredClient`'s existing `prepare` hook (already
  used by `Init` for `os.MkdirAll`) is the right seam — `Clone`'s `prepare` runs the actual `git
clone` invocation itself (using `buildClientConfig`'s already-resolved env/gitPath, before the
  anchor exists), then anchors afterward exactly like `Init` does. Needs `run`'s core spawn logic
  extracted into a helper parameterized by directory rather than reading `c.dir`, so `Clone`'s
  `prepare` can reuse it. Also needs a companion `RemoteManager.AddRemote(ctx, name, url string)
error` (`remote add`) for `clone.go`'s multi-remote configs, and must preserve `clone.go`'s
  existing `--` guard against a leading-dash URL (bead pg2-3j8b2).
- **`Pusher`'s `resolvePushRemote`**: needs either the full 7-step fallback chain folded into
  gitclient as one method, or new primitives (`Locator.Remotes`, a scoped `ConfigGet`) with the
  resolution logic staying in pn. Undecided; implementation should pick based on what's cleanest
  once building it.
- **pn-side, independent of gitclient**: symlink `.pre-commit-config.yaml` into `.pn-update`'s
  worktree at creation (the `PREK_ALLOW_NO_CONFIG` root-cause fix above).

## 8. If the operator rules otherwise

Should the operator instead choose Option A (full adoption), this design surfaces the concrete
next steps it would need, each as its own scoped design/implementation bead rather than one big
migration: (1) a signature-compatible streaming mechanism for `gitclient`'s mutating methods,
coordinated with the nine existing leaf-app consumers' fakes; (2) a per-invocation `-c` config
option (`options.go`), which — per §3 — is architecturally safe under D2/D5 since it's an argv,
not env/dir, concern; (3) a `Syncer` role built on a post-failure `StatusReader` probe with a new
`ErrRebaseConflict`-style sentinel; (4) `Committer`/`Pusher`/`Clone`-as-constructor/`BranchLister`
roles sized the same YAGNI way the existing roles were (one method per real pn call site); (5) a
reconciliation of `gitclient.Client`'s one-directory-per-instance model with pn's
`WorkerPool`-fanned-out concurrency (§4). None of that is attempted here — this bead is the design
pass the operator asked for, not the implementation.
