# pn-workspace: graceful handling of repos without an upstream

Date: 2026-04-29
Status: Design — pending user review

## Problem

`pn-workspace-update`, `pn-workspace-push`, and `pn-workspace-rebase` invoke
remote-dependent git operations (`git pull --rebase --autostash`, `git push`,
`git mu`) without first checking whether the current branch has an upstream
configured. When a project repo has no remote, or the current branch has no
tracking branch, those commands fail and the entire workspace operation aborts
mid-loop.

This is disruptive in workspaces that contain local-only repos (sandbox
projects, branches not yet pushed, repos cloned without a remote) where the
local lock-file refresh is still useful even if remote sync is not possible.

## Goal

For each of the three scripts, when a project has no usable upstream:

- Skip the remote-dependent git step(s).
- Print a single informational line that names the project and current branch.
- Continue processing remaining projects.
- Treat the project as a successful iteration (do not abort the script).

For `pn-workspace-update` specifically, still run `./update-locks.sh` so local
lock files refresh even without remote sync.

`pn-workspace-upgrade` inherits the fix transitively because it shells out to
`pn-workspace-update` followed by `pn-workspace-apply`.

## Non-goals

- Network-based remote reachability checks. A configured-but-unreachable remote
  remains a hard failure; the helper performs only a local config check.
- Other `git pull --rebase --autostash` failure modes (merge conflicts,
  dirty-tree autostash failures) — these continue to abort the script.
- Changes to `pn-workspace-status`, `pn-workspace-build`, `pn-workspace-check`,
  or other scripts not listed above.

## Design

### New helper in `modules/pn/pn-lib/pn-lib.bash`

Added under the `# ─── Workspace functions ───` section:

```bash
# Returns 0 if the current repo HEAD has a usable upstream (remote exists AND
# current branch has tracking branch configured). Returns 1 otherwise.
# Does NOT fetch — purely a local config check. Run from inside the repo
# working tree.
workspace_has_upstream() {
  [[ -n $(git remote 2>/dev/null) ]] || return 1
  git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1
}
```

Two checks because:

1. `git remote` may be empty (no remotes configured at all).
2. A remote may exist but the current branch has no tracking branch (e.g., a
   freshly created local-only branch).

Either condition disqualifies the repo from remote-dependent operations.

### Skip-message format

Each script captures the current branch and constructs a label that handles
detached HEAD (where `git branch --show-current` emits an empty string):

```bash
_branch=$(git branch --show-current)
_branch_label="${_branch:-DETACHED HEAD}"
```

Per-script skip-message text:

| Script                | Message                                                                          |
| --------------------- | -------------------------------------------------------------------------------- |
| `pn-workspace-update` | `no upstream for branch '$_branch_label' — skipping pull/push for $project_name` |
| `pn-workspace-push`   | `no upstream for branch '$_branch_label' — skipping push for $project_name`      |
| `pn-workspace-rebase` | `no upstream for branch '$_branch_label' — skipping rebase for $project_name`    |

### `pn-workspace-update.sh`

Per-project loop body becomes:

```bash
echo "  --== Update $project_name ==--  "
cd "$project_path" || exit 1

if workspace_has_upstream; then
  git pull --rebase --autostash &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""
fi

./update-locks.sh &
_child_pid=$!
wait "$_child_pid" || exit $?
_child_pid=""

if workspace_has_upstream; then
  git push &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""
else
  _branch=$(git branch --show-current)
  _branch_label="${_branch:-DETACHED HEAD}"
  echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
fi
```

Notes:

- The skip message is printed once per project, after `update-locks.sh`, so the
  user sees that local update happened but remote sync did not.
- `workspace_has_upstream` is invoked twice (before pull, before push) rather
  than cached in a variable; the cost is two cheap git config reads per project
  and the code stays straightforward. If a future refactor needs caching, do it
  then.

### `pn-workspace-push.sh`

Per-project loop body becomes:

```bash
project_name=$(basename "$project_path")
echo "  --== Push $project_name ==--  "
cd "$project_path" || exit 1

if workspace_has_upstream; then
  git push
else
  _branch=$(git branch --show-current)
  _branch_label="${_branch:-DETACHED HEAD}"
  echo "no upstream for branch '$_branch_label' — skipping push for $project_name"
fi
```

### `pn-workspace-rebase.sh`

Per-project loop body becomes:

```bash
project_name=$(basename "$project_path")
echo "  --== Rebase $project_name ==--  "
cd "$project_path" || exit 1

if workspace_has_upstream; then
  git mu
else
  _branch=$(git branch --show-current)
  _branch_label="${_branch:-DETACHED HEAD}"
  echo "no upstream for branch '$_branch_label' — skipping rebase for $project_name"
fi
```

### Help-text updates

Append one sentence to the Purpose paragraph of each script's `--help`:

- `pn-workspace-update`: "Projects without a configured upstream are still
  refreshed locally (`update-locks.sh` runs); pull and push are skipped with an
  informational message."
- `pn-workspace-push`: "Projects without a configured upstream are skipped with
  an informational message."
- `pn-workspace-rebase`: "Projects without a configured upstream are skipped
  with an informational message."

### Exit-code behavior

A skipped project is treated as a successful iteration. The loop continues, and
`pn-workspace-update` still regenerates `pn-workspace.lock` at the end. Overall
script exit status is unchanged from current success-path behavior unless a
non-skipped project fails.

## Tests

### `modules/pn/pn-lib/tests/`

New unit-test cases for `workspace_has_upstream`:

| Setup                                                   | Expected return |
| ------------------------------------------------------- | --------------- |
| Repo with no remotes configured                         | 1               |
| Repo with remote but current branch has no tracking     | 1               |
| Repo with remote and current branch has tracking branch | 0               |
| Repo in detached-HEAD state                             | 1               |

### `modules/pn/pn-workspace-update/tests/`

Add bats case: workspace with one project that has no remote.

Assertions:

- Script exits 0.
- Output contains `no upstream for branch '<expected-branch>' — skipping pull/push for <project_name>`.
- `update-locks.sh` was invoked (existing test fixtures use a stub script that
  records invocations — reuse the same mechanism).
- No `git pull` and no `git push` ran (verify via remote tip unchanged or via
  command-recording stubs, whichever the existing test harness uses).

### `modules/pn/pn-workspace-push/tests/`

Add bats case: workspace with one project that has no remote.

Assertions:

- Script exits 0.
- Output contains `no upstream for branch '<expected-branch>' — skipping push for <project_name>`.
- No `git push` ran.

Also add a detached-HEAD case asserting `'DETACHED HEAD'` appears literally in
the skip message.

### `modules/pn/pn-workspace-rebase/tests/`

Add bats case: workspace with one project that has no remote.

Assertions:

- Script exits 0.
- Output contains `no upstream for branch '<expected-branch>' — skipping rebase for <project_name>`.
- `git mu` was not invoked.

## Open questions

None at design time. Implementation may surface concerns about the existing
test harness's ability to assert non-invocation of `git pull`/`git push`/`git
mu`; if so, the implementation plan will adapt.
