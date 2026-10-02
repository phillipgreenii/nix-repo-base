# shellcheck shell=bash

# pre-commit-fix: a second name for `pg-hooks fix` (design: per-clone hook
# bundle, section 5.2). Delivered as its own tiny wrapper on PATH because agents
# and tool approvers match commands by exact name. All behavior, exit codes and
# messages are pg-hooks fix's.

if [[ ${1:-} == -h || ${1:-} == --help ]]; then
  cat <<'HELP'
pre-commit-fix: a second name for `pg-hooks fix`

Usage: pre-commit-fix

Apply the repo's fixers to the staged files and restage the files they changed.
Run `git add <files>` first. Exit codes are those of `pg-hooks fix` (see
`pg-hooks --help`): 0 ok, 2 refused, 10 a fixer failed, 11 files skipped,
12 bundle broken, 13 no bundle.
HELP
  exit 0
fi

exec pg-hooks fix "$@"
