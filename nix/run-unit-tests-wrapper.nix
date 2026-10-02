# The `run-unit-tests` prek hook entry (see the extraHooks comment in
# flake.nix and docs/superpowers/specs/2026-08-24-pg-test-runner-design.md,
# section 3). Extracted into its own file so a hermetic check can exercise the
# REAL wrapper against a fake pg-test-runner (checks.<system>.run-unit-tests-
# core-worktree-tripwire).
#
# `configPath` is the rendered pg-test-runner config handed to the runner.
#
# core.worktree tripwire (bead pg2-4c4nv): twice a stray `core.worktree`
# pointing at a drain worktree appeared in the CANONICAL clone's shared
# .git/config, making git report a phantom dirty tree and halting every land.
# The writer was never identified. Test processes run under this wrapper, so
# it snapshots the shared config's core.worktree before and after the runner
# and FAILS LOUDLY (exit 12) if it changed, so the next occurrence is
# attributed to the commit window rather than found hours later. The wrapper
# only reports; it never edits the config (R-3).
{ pkgs, configPath }:
pkgs.writeShellScript "run-unit-tests" ''
  set -eo pipefail
  if [[ -n "$IN_NIX_BUILD" || -n "$NIX_BUILD_TOP" ]]; then
    echo "run-unit-tests: inside the nix sandbox; skipping (checks.* already covers this)"
    exit 0
  fi
  if ! command -v pg-test-runner >/dev/null 2>&1; then
    echo "run-unit-tests: pg-test-runner not found on PATH -- provision it via the HM profile before committing (see docs/superpowers/specs/2026-08-24-pg-test-runner-design.md)" >&2
    exit 11
  fi

  # The shared (common-dir) config is the file a linked worktree's stray
  # core.worktree lands in. Unresolvable => tripwire disabled, but say so.
  common_dir=""
  if common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
    && [[ -f "$common_dir/config" ]]; then
    :
  else
    echo "run-unit-tests: warning: cannot resolve the shared .git/config; core.worktree tripwire disabled" >&2
    common_dir=""
  fi

  read_core_worktree() {
    [[ -n "$common_dir" ]] || return 0
    git config --file "$common_dir/config" --get core.worktree 2>/dev/null || true
  }

  before="$(read_core_worktree)"
  if [[ -n "$before" ]]; then
    echo "run-unit-tests: warning: $common_dir/config already sets core.worktree=$before before the tests ran" >&2
  fi

  rc=0
  pg-test-runner --config ${configPath} --labels unit --files "$@" || rc=$?

  after="$(read_core_worktree)"
  if [[ "$after" != "$before" ]]; then
    {
      echo "run-unit-tests: TRIPWIRE: core.worktree in $common_dir/config changed while the unit tests ran (pg2-4c4nv)"
      echo "  before: ''${before:-<unset>}"
      echo "  after:  ''${after:-<unset>}"
      echo "  git now misreports the canonical clone (phantom dirty tree). Do NOT reset the clone's state;"
      echo "  report this with the cwd, GIT_* env and the test run above. Test exit code was $rc."
    } >&2
    exit 12
  fi
  exit "$rc"
''
