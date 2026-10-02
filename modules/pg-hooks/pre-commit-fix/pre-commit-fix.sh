# shellcheck shell=bash

# pre-commit-fix: a second name for `pg-hooks fix` (design: per-clone hook
# bundle, section 5.2). Delivered as its own tiny wrapper on PATH because agents
# and tool approvers match commands by exact name. All behavior, exit codes and
# messages are pg-hooks fix's.

exec pg-hooks fix "$@"
