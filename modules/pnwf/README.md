# pnwf — workforest work-cycle helper

Delivery pattern and non-obvious gotchas (from `pg2-xs5cj`; extracted 2026-08-17 from a bd
memory). The PACKAGE builds here (`modules/pnwf`: a `mkBashLibrary` `pnwf-lib` + a `mkBashScript`
`pnwf`); the HOME MODULE lives in `phillipgreenii-nix-agent-support` (`home/programs/pnwf`).

## Cross-repo delivery (do not "simplify" this)

1. The home module MUST live in agent-support because its enable default
   (`claude-code.enable` AND `pluginEnabled "pn-workspace-rules"`) reads options defined ONLY
   there; this repo sits BELOW agent-support in the dep graph and cannot see them. Mirror:
   `home/programs/integrate-branch-support`.
2. agent-support threads this repo's package via a GUARDED overlay attr:
   `basePkgs = phillipgreenii-nix-base.packages.<system> or {}` then
   `// prev.lib.optionalAttrs (basePkgs ? pnwf) { inherit (basePkgs) pnwf; }`. It MUST use `prev`
   (the input pkgs), NOT `final`, for both the system lookup AND the `optionalAttrs` — using
   `final` to determine the overlay OUTPUT SHAPE is an infinite-recursion fixpoint cycle. The
   guard is needed because this repo publishes only 2 systems while agent-support builds 4, AND a
   locked repo-base rev may predate the package.
3. The home module's enable default ALSO needs `AND (pkgs ? pnwf)` as defense-in-depth: without
   it, before the producer→consumer relock the module enables while `pkgs.pnwf` is absent and
   apply HARD-CRASHES ("pnwf cannot be found in pkgs"). The enable-default evalModules test must
   inject a stub (`pkgs // { pnwf = pkgs.hello; }`) so it still exercises the plugin-enable logic
   rather than the availability term.

## Runtime gotchas

4. `integrate-branch-support` is invoked BARE — it has NO `--json` flag; it emits one JSON object
   unconditionally on stdout (fields: `primary_branch` + `strategy` top-level;
   `canonical.{branch,dirty}` nested; `remote`; `open_pr`; `mr_bead`).
5. `pnwf resolve` MUST run its `pn workspace info --json` discovery under
   `env -u PN_WORKSPACE_ROOT` — pn honors an exported `PN_WORKSPACE_ROOT` before the cwd
   upward-walk, so a stale exported value silently redirects pn to the canonical workspace and
   defeats every stage skill's location guard.
6. A SLASHED workforest branch name (`wf/<id>`) nests the set dir as `.workforests/wf/<id>` and
   breaks pn's `inWorkforest()` basename check — use single-segment branch names for workforests
   (tracked: `pg2-u1ubb`).
7. `mkBashScript`/`mkBashLibrary` inject `set -euo pipefail`: every exit-code-as-boolean git probe
   (`merge-base --is-ancestor` 0/1/128; `rev-parse --verify --quiet`) MUST capture rc via
   `rc=0; cmd || rc=$?` then `case`; and bats MUST prove non-abort INSIDE a real `set -e` shell
   via `run bash -euo pipefail -c "source LIB; probe"` — merely sourcing into bats' own non-e
   shell proves nothing.
