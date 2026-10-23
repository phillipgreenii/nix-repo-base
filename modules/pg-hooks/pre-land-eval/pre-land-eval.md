# pre-land-eval

> Eval-only pre-land check: evaluate a repo's flake `checks`, `packages` and `devShells` at a commit, without building them.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Check a branch tip (prints one word: `ok`, `eval-failed`, `eval-inconclusive` or `not-enabled`; details go to stderr). It takes 1 to 2 minutes, so run it through `bgrun`:

`bgrun {{name}} -- pre-land-eval {{path/to/repo}} {{branch}}`

- Wait for the result and read the word and the exit code:

`bgwait {{name}} --timeout 540`

- Treat a needed import-from-derivation as inconclusive (exit 11) instead of building it:

`pre-land-eval --no-ifd {{path/to/repo}} {{HEAD}}`

- Opt a clone in (a repo without the key reports `not-enabled`, exit 0):

`git -C {{path/to/repo}} config pgii-integrate-branch.preLandEval true`

- Exit codes: `0` ok or not-enabled, `10` eval-failed, `11` eval-inconclusive, `2` usage error or an unresolvable ref.
