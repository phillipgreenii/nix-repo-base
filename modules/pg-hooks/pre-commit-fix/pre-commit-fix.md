# pre-commit-fix

> Second name for `pg-hooks fix`: apply the repo's fixers to the staged files and restage them.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Format the staged files (stage them with `git add` first):

`pre-commit-fix`

- The usual sequence before a commit:

`git add {{path/to/file}} && pre-commit-fix && git commit`

- Exit codes are those of `pg-hooks fix`: `0` fixed or nothing to do, `2` refused (merge, rebase or cherry-pick in progress) or no repo, `10` a fixer failed, `11` files skipped (they have staged and unstaged changes), `12` bundle broken, `13` no bundle.
