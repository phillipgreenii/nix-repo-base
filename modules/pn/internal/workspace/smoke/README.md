# pn workspace smoke harness — scenario authoring notes

(Extracted 2026-08-17 from bd memories.)

## Execution model: no mid-flow hook

Each scenario's `command.txt` runs ONLY pn commands — `runScenario` always invokes
`pnBin <args>`, stripping a leading `pn` token — and `setup.sh` runs ONCE before all commands.
There is NO hook to run arbitrary git/shell BETWEEN pn subcommands (e.g. between
`workspace clone` and `workspace update`). Scenarios needing mid-flow divergence (local main
ahead + origin advanced) therefore cannot be built incrementally; the options are (a) add a
post-clone hook to the harness (e.g. an optional `after-clone.sh` between clone and update), or
(b) fully hand-build the clone + divergence in `setup.sh`, bypassing pn's own clone/lock
(risky/flaky). Context: `pg2-hbt5` (an optional s34 worktree-update-defer scenario needs such a
hook).

## Authoring rules

- Scenarios MUST NOT hard-require nix: reuse the nix-available-or-fake-build-command setup from
  s18/s20. (s23 once pinned its fixture to `x86_64-linux` and failed on darwin hosts; its
  `setup.sh` now resolves the host system — keep it that way.)
- `UL_LIB_DIR` is injected via `buildScrubbedEnv` (tempHome/ullib), satisfying the worktree
  flow's non-empty requirement.
- Bare remotes are `file://` via `smoke_bare_remote.go` — no network.
