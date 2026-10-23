# pre-land-eval

> Eval-only pre-land check: a caller of `pn workspace flake-check --no-build` that evaluates a repo's flake `checks`, `packages` and `devShells` at a commit, against the local workspace siblings, without building anything.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Check a branch tip (prints one word: `ok`, `eval-failed` or `not-enabled`; details go to stderr). It takes 1 to 3 minutes, so run it through `bgrun`:

`bgrun {{name}} -- pre-land-eval {{path/to/repo}} {{branch}}`

- Wait for the result and read the word and the exit code:

`bgwait {{name}} --timeout 540`

- Fail the check on any import from derivation instead of building it (passes `--no-ifd` to pn):

`pre-land-eval --no-ifd {{path/to/repo}} {{HEAD}}`

- Opt a clone in (a repo without the key reports `not-enabled`, exit 0):

`git -C {{path/to/repo}} config pgii-integrate-branch.preLandEval true`

- The check itself, without the opt-in key and the one-word verdict (every repo, or one repo or worktree; one JSON line per run is appended to `~/.local/state/pn/flake-check-no-build.jsonl`):

`pn workspace flake-check --no-build {{repo-or-path}}`

- Exit codes: `0` ok or not-enabled, `10` eval-failed (there is no retry and no inconclusive class), `2` usage error, an unresolvable ref or a repo that is no workspace member, `127` pn is not installed.
