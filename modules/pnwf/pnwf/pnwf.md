# pnwf

> Deterministic helper for the workforest work-cycle (fork/validate/land/cleanup). This build implements the read-only probes: resolve, repos, stage.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Print the resolved workspace location as JSON (`canonical_root`, `in_workforest`, `set_dir`, `pn_workspace_root`):

`pnwf resolve`

- Same, but fail unless the current workspace is a coordinated workforest set:

`pnwf resolve --set`

- List a set's member repos in topological order:

`pnwf repos --set`

- Print the current set's lifecycle stage (`work`, `ready-to-land`, `resuming-land`, or `landed`):

`pnwf stage --set`

- Show usage:

`pnwf --help`
