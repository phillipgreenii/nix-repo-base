# pre-land-eval

> Eval-only pre-land check: a caller of `pn workspace flake-check --no-build` that evaluates a repo's flake `checks`, `packages` and `devShells` at a commit, against the local workspace siblings, without building anything.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Check a branch tip (prints one word: `ok` or `eval-failed`; details go to stderr). It takes 1 to 3 minutes, so run it through `bgrun`:

`bgrun {{name}} -- pre-land-eval {{path/to/repo}} {{branch}}`

- Wait for the result and read the word and the exit code:

`bgwait {{name}} --timeout 540`

- Fail the check on any import from derivation instead of building it (passes `--no-ifd` to pn):

`pre-land-eval --no-ifd {{path/to/repo}} {{HEAD}}`

- Enable it for landings: list it as a `before_move` hook of `pg-git-lander land` in the repo's `.pg-git-lander.toml`; the command has no opt-in key and runs whenever it is invoked.

- The check itself, without the one-word verdict (every repo, or one repo or worktree; one JSON line per run is appended to `~/.local/state/pn/flake-check-no-build.jsonl`):

`pn workspace flake-check --no-build {{repo-or-path}}`

- Exit codes: `0` ok, `10` eval-failed (pn exited 10; there is no retry and no inconclusive class), `2` usage error, an unresolvable ref, a repo that is no workspace member, or a check that could not run (pn exited 1, its generic error, or any code other than 0, 2 and 10), `127` pn is not installed.
