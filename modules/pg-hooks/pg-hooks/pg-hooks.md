# pg-hooks

> Inspect and run the per-clone hook bundle that git hooks use.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Show the bundle state of the current clone (exit code carries the state):

`pg-hooks status`

- Machine-readable state, for scripts (parse these lines, never the prose):

`pg-hooks status --porcelain`

- List the stages that have configured hooks:

`pg-hooks list`

- Explain what a stage runs and which hooks it holds:

`pg-hooks explain {{pre-commit}}`

- Run the pre-commit hooks on the staged files:

`pg-hooks run pre-commit`

- Run a stage on every tracked file, or on named files:

`pg-hooks run pre-commit --all-files`

`pg-hooks run pre-commit {{path/to/file1 path/to/file2}}`

- Forward raw flags to prek:

`pg-hooks run pre-commit -- {{--verbose}}`

- Run the pre-commit hooks over the branch diff before landing (the ref must be checked out):

`pg-hooks run pre-land`

- Apply the repo's fixers to the staged files and restage them (also installed as `pre-commit-fix`):

`git add {{path/to/file}} && pg-hooks fix`

## Fix

`pg-hooks fix` works on the STAGED files only, runs the bundle's fixers in order, and restages only the
files a fixer changed. Untracked and unstaged files are never touched. A file with both staged and
unstaged changes is skipped untouched; every skipped file is listed and the run exits `11`
(`git add` the file, or `git restore --staged` it, then rerun). It refuses (exit `2`) during a merge,
rebase or cherry-pick. It is silent on success; a failing fixer prints its name, files and full output
and exits `10`. Per-fixer timings are appended to `~/.local/state/pg-hooks/timings.jsonl`
(`$XDG_STATE_HOME` when set).

## Exit codes

| Code | Meaning                                                                                       |
| ---- | --------------------------------------------------------------------------------------------- |
| `0`  | Success or nothing to do.                                                                     |
| `1`  | Generic failure; never has a branchable meaning.                                              |
| `2`  | Usage error, unknown stage, refused operation (`pre-land` on a ref not checked out), no repo. |
| `10` | A hook or fixer failed.                                                                       |
| `11` | File(s) skipped (staged and unstaged changes).                                                |
| `12` | Bundle broken (pointer invalid or `bin/prek` not runnable).                                   |
| `13` | No bundle. `run`, `fix`, `list` and `explain` print the notice and exit 13.                   |
| `14` | `status` only: bundle stale.                                                                  |
| `15` | `status` only: hooks unreachable (`core.hooksPath` bypasses `<common-dir>/hooks`).            |
| `16` | `status` only: clone relocated (`clone_path` differs).                                        |

`status --porcelain` prints `state=`, `bundle=`, `generation=`, `stages=` and `reinstall=` lines.
`state` is one of `present`, `stale`, `missing`, `broken`, `unreachable`, `relocated`.
Messages go to stderr and start with `pg-hooks:`; stdout carries only `status`, `list` and
`explain` output.
