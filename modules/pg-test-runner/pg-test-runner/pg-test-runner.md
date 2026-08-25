# pg-test-runner

> Label-driven, nix-free-at-runtime direct test runner.
> Runs a project's unit tests straight from source — no `nix build`, no `nix develop`.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Run the unit tier for the current directory (default when no labels are given):

`pg-test-runner`

- Run the unit tier for a specific project path:

`pg-test-runner {{modules/ul/determine-ul-lib-dir}}`

- prek mode: resolve staged files to their owning projects and run those:

`pg-test-runner --files {{file1 file2}}`

- Every project under the repo, regardless of location:

`pg-test-runner --all`

- Run a specific non-unit label (never in a commit hook):

`pg-test-runner --labels {{integration}} {{path}}`

- Run every label, unfiltered (an explicit human action; costs live credentials/tokens on
  `contract`-kind suites):

`pg-test-runner --labels all --all`

- Override the configuration registry:

`pg-test-runner --config {{/path/to/config.json}} {{path}}`

- Show usage:

`pg-test-runner --help`

## Exit codes

| Code | Meaning                                                                |
| ---- | ---------------------------------------------------------------------- |
| `0`  | All selected tests passed, including vacuous cases (nothing to run).   |
| `1`  | Generic/unexpected error.                                              |
| `2`  | Usage error: bad flags, mode conflict, or a nonexistent path argument. |
| `10` | One or more test invocations failed, including a `timeoutSeconds` cap. |
| `11` | A required language tool was missing from `PATH`.                      |
| `12` | A path argument resolved to no project, upward or downward.            |
| `13` | Configuration missing, unreadable, or invalid.                         |

`--files`, path arguments, and `--all` are mutually exclusive input modes; combining them is a
usage error. The runner never invokes `nix`, never inspects git state, and only reads whichever
languages its configuration registers — adding a language, changing a command, or registering a
label is a configuration change, never a code change.
