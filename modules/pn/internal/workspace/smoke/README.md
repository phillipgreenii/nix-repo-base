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

## Git fixtures: Go builder is hermetic, `setup.sh` is exempt

Operator-visible rule: Go tests that need a git repository MUST use `x/gittest` and MUST NOT run
`git init` or `exec.Command("git", ...)` themselves (`code-file-standards.md`, "Tests That Need
Git", in `nix-agent-support`). The smoke tier follows it in two parts (decision b+, bead
`pg2-km0jx`, applied by `pg2-wsvch`; an agent decision, reversible by the operator):

- **Go side: in scope.** `smoke_bare_remote.go` builds its bare remotes and seed repositories with
  `x/gitfixture` (`setupBareRemote`) and reads repository state with `x/gitclient` (`gitRead`).
  Both have an allowlisted child environment, a fixture `HOME`, no system config and hooks
  redirected to an empty directory, so no `GIT_*` variable in the test process can reach them. The
  file has no `exec.Command("git", ...)` and no enumerated scrub list.
- **`scenarios/*/setup.sh`: exempt.** The 44 scripts keep their hand-written `git init`, `git clone`
  and `git config`. Reason: a `setup.sh` is not a Go test. It only ever runs inside the runner,
  which executes it as `bash setup.sh` with the environment from `buildScrubbedEnv`
  (`smoke_env.go`): a throwaway `HOME`, `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_SYSTEM` set to
  `/dev/null`, a fixed identity, and the whole GIT_DIR family removed. Rewriting 44 scripts onto a
  shell harness would add a second fixture mechanism to the scenarios without closing any leak the
  scrubbed environment does not already close.

`buildScrubbedEnv` is therefore the only isolation the exempt scripts have, and the following MUST
hold:

- It MUST strip the GIT_DIR family (`gitDirEnvVars` in `smoke_env.go`: `GIT_DIR`, `GIT_WORK_TREE`,
  `GIT_INDEX_FILE`, `GIT_CEILING_DIRECTORIES`, `GIT_COMMON_DIR`, `GIT_PREFIX`,
  `GIT_OBJECT_DIRECTORY`) on every call, from the live `os.Environ()`. `TestMain`'s one-time
  `os.Unsetenv` of the same list is not enough on its own: it cannot stop a variable set later with
  `t.Setenv`.
- A `setup.sh` MUST NOT be run any other way than through `runSetupScript` with that environment,
  and MUST NOT `export` or `source` anything that re-adds a GIT_DIR-family variable.
- `TestSmoke_ScrubbedEnvStripsGitDirFamilyAfterTestMain` (`smoke_env_test.go`) proves it: it sets
  the whole family at a decoy repository with `t.Setenv` after `TestMain`, runs a script that does
  `git init`, and asserts that the script saw none of the variables and that the decoy's
  `.git/config` and `HEAD` are unchanged.

New Go-side git in this package MUST use `x/gitfixture` (builders) or `x/gitclient` (reads). If a
scenario's `setup.sh` needs something the scrubbed environment cannot give it, fix
`buildScrubbedEnv`; do not add a second scrub list.

## Authoring rules

- Scenarios MUST NOT hard-require nix: reuse the nix-available-or-fake-build-command setup from
  s18/s20. (s23 once pinned its fixture to `x86_64-linux` and failed on darwin hosts; its
  `setup.sh` now resolves the host system — keep it that way.)
- `UL_LIB_DIR` is injected via `buildScrubbedEnv` (tempHome/ullib), satisfying the worktree
  flow's non-empty requirement.
- Bare remotes are `file://` via `smoke_bare_remote.go` — no network.
- A scenario that needs stand-in executables (e.g. s37's fake `nix`) creates them in `setup.sh`
  under the workspace root and lists their root-relative directories, one per line, in a
  `path-prepend` file; `runScenario` puts them first on `PATH` for every command and extra
  assertion. A stand-in that shadows a real tool MUST delegate every call it does not handle to
  the real one (strip its own directory from `PATH` by physical path, then `exec`), or it loops.
