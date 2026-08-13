# wsplan

> Read-only land-plan emitter: detect workspace shape and emit a typed JSON plan for the executor.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Emit the plan for a whole `pn` workspace (every member's unlanded work areas):

`wsplan land-plan --root {{/absolute/path/to/workspace}}`

- Emit the plan for one repo only (pointed repo wins; siblings are ignored):

`wsplan land-plan --root {{/absolute/path/to/workspace/some-repo}}`

- Disambiguate a repo that has several unlanded work areas, by pointing at the intended one:

`wsplan land-plan --root {{/absolute/path/to/repo/.worktrees/my-branch}}`

- Emit the plan for a coordinated workforest set (overrides the pointed-repo rule):

`wsplan land-plan --root {{/absolute/path/to/workspace}} --set-branch {{branch}}`

- Emit the plan for a standalone repo outside any `pn` workspace:

`wsplan land-plan --root {{/absolute/path/to/standalone/repo}}`

- Read just the discriminator the executor branches on (`plan`, `nothing-to-do`, `refuse`, `stopped`):

`wsplan land-plan --root {{/absolute/path}} | jq -r '.outcome, .reason'`

- List the steps to run, in order:

`wsplan land-plan --root {{/absolute/path}} | jq -r '.steps[] | "\(.handler) \(.targetWorktree)"'`

- Show usage:

`wsplan --help`
