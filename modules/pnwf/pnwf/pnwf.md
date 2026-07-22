# pnwf

> Deterministic helper for the workforest work-cycle (fork/validate/land/cleanup).
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Print the resolved workspace location as JSON (`canonical_root`, `in_workforest`, `set_dir`, `pn_workspace_root`):

`pnwf resolve`

- Same, but fail unless the current workspace is a coordinated workforest set:

`pnwf resolve --set`

- List a set's member repos in topological order:

`pnwf repos --set`

- Print the current set's lifecycle stage (`work`, `ready-to-land`, `resuming-land`, or `landed`):

`pnwf stage --set`

- Pre-flight checks before forking a workforest set on a branch (prints `proceed`, `resume`, or `stop` plus a reason):

`pnwf fork-preflight {{branch}}`

- Same, restricted to a subset of the canonical workspace's repos:

`pnwf fork-preflight {{branch}} --repos {{repoA,repoB}}`

- Print the topo-ordered member repos of a branch's set that still need landing:

`pnwf land-plan {{branch}}`

- Best-effort teardown of a branch's set from the canonical clone (removes worktree + branch for every landed member; keeps and reports the rest):

`pnwf cleanup {{branch}}`

- Same, forcing removal of a dirty worktree or a not-yet-landed branch:

`pnwf cleanup {{branch}} --force-dirty-worktree-removal --force-unlanded-branch-removal`

- Print a per-repo status table for a branch's set (member, label, reason):

`pnwf status {{branch}}`

- Show usage:

`pnwf --help`
