# shellcheck shell=bash
# update-locks-lib.bash — Shared library for consumer-side update-locks.sh
# scripts. Provides isolated step execution with per-step commits + rollback.
# Sources update-cache-lib.bash for caching support.
#
# =================================================================
# CONTRACT (referenced by consumer scripts via the named anchors below)
# =================================================================
#
# This contract documents the behaviors consumer update-locks.sh scripts AND
# verifiers (e.g. nix-overlay's verify-provenance.sh) depend on. The producer
# (nix-repo-base) preserves these behaviors across refactors.
#
# Consumed at HEAD: there is NO UL_LIB_VERSION constant; the consumer's
# pin of nix-repo-base IS the version contract. Consumers that need a
# behavior change update their pin and their script together.
#
# -----------------------------------------------------------------
# ul_setup <project-name> <script-dir>
# -----------------------------------------------------------------
# ANCHOR: ul_setup-fsmonitor-disable
#   Disables core.fsmonitor for the duration of the run, but ONLY when it is
#   actually active for the repo — the effective value must be boolean-true
#   (yes/on/1 included); a hook-path fsmonitor spawns no native daemon and is
#   left untouched. A non-destructive trap (_ul_restore_fsmonitor) restores it on
#   EXIT/INT/TERM if the clean-tree gate hasn't yet armed the full cleanup trap:
#   a repo-LOCAL value is put back verbatim, whereas a value INHERITED from
#   global/system config is restored by removing the local override, never by
#   pinning it into the repo. A stale .git/fsmonitor--daemon.ipc socket is
#   removed unconditionally, since it breaks flake evaluation regardless of the
#   current config.
#
# ANCHOR: ul_setup-pre-commit-install
#   Ensures the git-hooks.nix pre-commit hook binary is installed and current
#   BEFORE the clean-tree gate — it evaluates the flake, so it must run after
#   fsmonitor is disabled. Three tiers: (1) build .#install-pre-commit-hooks,
#   (2) reinstall if the hook binary was GC'd, (3) reinstall if the derivation
#   changed since the last run. The generated .pre-commit-config.yaml is a
#   gitignored /nix/store symlink (ADR 0016); it may be regenerated in the
#   working tree here but is never staged or committed, so it stays invisible
#   to the gate.
#
# ANCHOR: ul_setup-clean-tree-gate
#   Asserts `git status --porcelain --untracked-files=normal` is empty: tracked
#   modifications, staged changes, AND non-ignored UNTRACKED files all fail the
#   gate (ignored files, e.g. the .pre-commit-config.yaml symlink, are excluded).
#   Exits 1 with a git status --short dump on a dirty tree.
#
# ANCHOR: ul_setup-full-cleanup-trap
#   AFTER the gate passes, swaps the non-destructive trap for the full
#   cleanup trap (_ul_cleanup) which rolls back per-step failures and
#   restores fsmonitor on EXIT/INT/TERM.
#
# -----------------------------------------------------------------
# ul_run_step <step-name> <commit-msg> <cmd...>
# -----------------------------------------------------------------
# ANCHOR: ul_run_step-cache-skip
#   If the per-step stamp (.update-locks/steps/<step-name>) is within the
#   step's time window (set by UL_TTL_* env or the step's own configuration),
#   ul_should_run returns false and the step is skipped (_UL_STEPS_SKIPPED
#   increments).
#
# ANCHOR: ul_run_step-dirty-tree-fatal
#   Asserts clean tree before invoking <cmd> via `git status --porcelain
#   --untracked-files=normal` (untracked included). A dirty tree here is FATAL
#   (exits 1) — it means a prior step's commit attempt failed silently.
#
# ANCHOR: ul_run_step-success-commit
#   On <cmd> exit 0 AND content changed: runs nix fmt, stages all, writes
#   stamp, commits ONE commit with <commit-msg>. On <cmd> exit 0 AND no
#   content changed: writes stamp, commits stamp-only.
#
# ANCHOR: ul_run_step-deferred
#   On <cmd> exit $UL_RC_ATTEMPTED (75 = EX_TEMPFAIL): rolls back content
#   (git reset --hard HEAD; git clean -fd), writes stamp, commits stamp-only
#   with message "update-locks: <step-name> attempted, no update applied".
#   The deferral counts toward _UL_STEPS_DEFERRED, not _UL_STEPS_FAILED, and
#   ul_finalize does NOT exit non-zero solely due to deferrals.
#
# ANCHOR: ul_run_step-fail-rollback
#   On any other non-zero exit, the step's captured stderr is classified by
#   ul_classify_step_failure into resource | transient | hard (see ANCHOR
#   ul-classify-step-failure). All three roll back content first, then:
#     - transient: write NO stamp, count _UL_STEPS_TRANSIENT, keep the run
#       passing (retried next run — a transient external-source failure means we
#       never learned whether an update exists).
#     - resource: print an actionable message and `exit $UL_RC_ABORT` (77),
#       aborting update-locks.sh so pn stops the whole run (every remaining repo
#       would fail identically). See ADR 0020.
#     - hard: record the step in _UL_FAILED_STEPS, commit nothing; ul_finalize
#       exits 1 with the list of failed step names.
#
# ANCHOR: ul-classify-step-failure
#   ul_classify_step_failure <stderr_file> echoes resource | transient | hard.
#   CONSERVATIVE: only unambiguous signatures leave "hard" (a false "transient"
#   silently skips a real update). Precedence order: ENOSPC → resource; a
#   definitive-hard allowlist (HTTP 4xx, hash/provenance mismatch, signature
#   failures, OOM, "builder for … failed") → hard; a transport-scoped network
#   allowlist → transient; default → hard. Hard is matched BEFORE transient so a
#   genuine failure that co-occurs with a transient retry blip stays hard.
#
# -----------------------------------------------------------------
# ul_reexec_in_dev_shell
# -----------------------------------------------------------------
# ANCHOR: ul_reexec-already-in-shell
#   If $IN_NIX_SHELL is set: prints a notice on stderr and returns 0 (no
#   re-exec). Caller continues in the same process.
#
# ANCHOR: ul_reexec-enter-dev-shell
#   Otherwise: enters nix develop "${UL_FLAKE_DIR:-<script dir>}" --command
#   bash -c '...' ONCE with the original $0 + $@. The dev shell entered defaults
#   to the calling script's directory (correct for a flake at the repo root); a
#   consumer whose flake lives in a subdirectory exports UL_FLAKE_DIR pointing at
#   it (e.g. homelab sets UL_FLAKE_DIR="${SCRIPT_DIR}/nix") so `nix develop`
#   resolves the flake instead of erroring "not part of a flake". A sentinel file
#   distinguishes "shell never started" (file still present after nix exits —
#   e.g. broken/absent flake, or a devShell that cannot build on this host) from
#   "shell ran the script" (file removed by inner shell).
#
# ANCHOR: ul_reexec-fallback-on-broken-flake
#   If the shell never started, prints a warning and returns 0 so the script
#   can still run with host tooling. The nix develop invocation is errexit-guarded
#   (|| rc=$?) so this fallback fires even though consumer scripts run under
#   set -e — otherwise a failing `nix develop` would abort the script before the
#   sentinel check. nix's own stderr is left visible so the user can fix the flake.
#
# ANCHOR: ul_reexec-self-repair-nrb-rev-fallback
#   Propagates UL_LIB_DIR (when set) into the in-shell re-run so the inner
#   process reuses the resolved lib instead of re-resolving via
#   determine-ul-lib-dir. Consumers' update-locks.sh resolve NRB_REV from
#   their flake.lock with a fallback to unpinned HEAD; the unpinned-HEAD
#   fallback is the LAST-RESORT self-repair path used when the consumer's
#   own pinned nix-repo-base is unbuildable (e.g. corrupt flake.lock during
#   recovery).
#
# -----------------------------------------------------------------
# ul_finalize
# -----------------------------------------------------------------
# ANCHOR: ul_finalize-summary
#   Prints a summary line (Ran / Passed / Upgraded / Deferred / Failed / Skipped)
#   followed by per-upgrade detail lines for each step that produced content
#   changes (extracted by _ul_record_upgrade).
#
# ANCHOR: ul_finalize-exit-code
#   Exits 0 if _UL_STEPS_FAILED is 0; exits 1 with the failed step list
#   otherwise. Deferrals AND transient rollbacks do NOT contribute to a non-zero
#   exit (a resource abort exits earlier, from ul_run_step, with UL_RC_ABORT).
#
# -----------------------------------------------------------------
# Exit codes used by step commands invoked under ul_run_step
# -----------------------------------------------------------------
# ANCHOR: ul-exit-codes
#   0                  = success (commit content if any, else stamp-only)
#   $UL_RC_ATTEMPTED (75) = valid attempt, no update applied (deferred; stamp written)
#   any other non-zero = failure; ul_classify_step_failure then decides:
#                          transient → rollback, no stamp, run stays green (retry)
#                          resource  → update-locks.sh exits $UL_RC_ABORT (77), run aborts
#                          hard      → rollback, record failure, ul_finalize exits 1
# ANCHOR: ul-abort-exit-code
#   $UL_RC_ABORT (77) = environmental/resource abort emitted by update-locks.sh
#   itself (ENOSPC step, or unhealthy nix daemon in ul_setup). pn recognizes it
#   as ulExitAbort and stops the whole workspace run. See ADR 0020.
#
# =================================================================

_UL_LOCKS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Exit code a step returns to mean "valid attempt, no update applied" — roll
# back content but record the timestamp (so it isn't retried until the window
# expires) and keep the run passing. 75 = EX_TEMPFAIL: clear of generic 1/2 and
# of Nix's 100/101, so a real tool failure is never misread as a deferral.
UL_RC_ATTEMPTED=75
ul_attempted() { exit "$UL_RC_ATTEMPTED"; }

# Sentinel exit code update-locks.sh uses to abort the whole run for an
# environmental / resource-exhaustion failure (out of disk, unhealthy nix
# daemon). Unlike a per-step failure, it means every remaining repo would fail
# identically, so pn stops the run rather than marching on. Distinct from 75
# (per-step deferral), generic 1/2, and Nix's 100/101; recognized on the Go side
# as ulExitAbort. See ADR 0020.
UL_RC_ABORT=77

# Classify a FAILED step's captured stderr into one of: resource | transient | hard.
# CONSERVATIVE by design: only UNAMBIGUOUS signatures map away from "hard", because
# a false "transient" silently skips a real update — worse than a visible failure
# (same philosophy as the opt-in 75 deferral above). Pure: reads the file only, no
# git/nix/network. See ADR 0020 for the signature policy.
ul_classify_step_failure() {
  local stderr_file="$1"
  [[ -r $stderr_file ]] || {
    echo hard
    return 0
  }

  # Resource exhaustion FIRST — it must win over any co-occurring network noise,
  # since deferring is pointless when the disk is full (every later step/repo hits
  # it too). ONLY disk (ENOSPC): OOM is handled as a hard signal below.
  if grep -qiE 'No space left on device|ENOSPC' "$stderr_file" 2>/dev/null; then
    echo resource
    return 0
  fi

  # Definitive HARD signatures SECOND — these WIN over any co-occurring transient
  # line. A retry-heavy run often logs a transient blip (a network retry) and THEN
  # hits a genuine failure (broken pin, hash mismatch, build error) in the same
  # step; classifying that step transient would silently skip a real update. So we
  # match unambiguous genuine-failure signatures before the transient allowlist.
  # Erring toward hard is the SAFE direction (a visible failure, which the policy
  # explicitly prefers over a false deferral). See ADR 0020.
  local hard_re
  hard_re='HTTP error 4[0-9][0-9]|returned error: 4[0-9][0-9]'     # broken pin (4xx), not 5xx
  hard_re+='|hash mismatch|does not match'                         # FOD / provenance mismatch
  hard_re+='|refusing to verify'                                   # verify-provenance TOCTOU guard
  hard_re+='|(cosign|attestation|verify-blob|gpg|signature).*fail' # provenance/signature failures
  hard_re+='|cannot allocate memory'                               # OOM (ambiguous → visible failure)
  hard_re+='|builder for .* failed'                                # genuine nix build failure
  if grep -qiE "$hard_re" "$stderr_file" 2>/dev/null; then
    echo hard
    return 0
  fi

  # Transient external-source / connectivity failures from nix, curl, git, go, uv.
  # Transport-scoped so a package's own log text is not misread as a fetch failure;
  # 5xx/timeouts defer, 4xx (above) does not. Extend deliberately (ADR 0020).
  local net_re
  net_re='Could not resolve host'
  net_re+='|Temporary failure in name resolution'
  net_re+='|TLS handshake timeout|handshake timed out'
  net_re+='|Failed to connect to [^ ]+ port'
  net_re+='|connect: (connection refused|network is unreachable|operation timed out|no route to host)'
  net_re+='|dial tcp .*(i/o timeout|connection refused|no such host|network is unreachable)'
  net_re+='|read: connection reset by peer'
  net_re+='|unable to download .*: (Couldn|Timeout|Connection|SSL|Resolving|Failed to connect)'
  net_re+='|HTTP error 5[0-9][0-9]|The requested URL returned error: 5[0-9][0-9]'
  net_re+='|429 Too Many Requests'
  # git remote transients (a persistent auth failure prints different text and
  # falls through to hard; these are disconnect/timeout signatures only)
  net_re+='|The remote end hung up unexpectedly'
  net_re+='|RPC failed; (curl|HTTP|result)'
  net_re+='|early EOF'
  net_re+='|fetch-pack: unexpected disconnect'
  net_re+='|ssh: connect to host .* (Connection|Operation) '
  if grep -qiE "$net_re" "$stderr_file" 2>/dev/null; then
    echo transient
    return 0
  fi

  echo hard
}

_UL_STEPS_RAN=0
_UL_STEPS_SUCCEEDED=0
_UL_STEPS_FAILED=0
_UL_STEPS_SKIPPED=0
_UL_STEPS_DEFERRED=0
_UL_STEPS_TRANSIENT=0
_UL_FAILED_STEPS=()
_UL_UPGRADED_STEPS=()
_UL_UPGRADE_NOTES=()
_UL_SCRIPT_DIR=""
_UL_CHILD_PID=""
_UL_CAUGHT_SIGNAL=""
# Per-step stderr capture (set by ul_run_step). Module-level so _ul_cleanup can
# remove the temp file + fifo and reap the tee on an interrupted run.
_ul_step_err_file=""
_ul_err_fifo=""
_ul_tee_pid=""

_ul_cleanup() {
  local signal="${1:-EXIT}"
  _UL_CAUGHT_SIGNAL="$signal"

  # Kill running child if any
  if [[ -n $_UL_CHILD_PID ]] && kill -0 "$_UL_CHILD_PID" 2>/dev/null; then
    kill -TERM "$_UL_CHILD_PID" 2>/dev/null
    wait "$_UL_CHILD_PID" 2>/dev/null || true
  fi
  _UL_CHILD_PID=""

  # Reap the stderr-capture tee and remove its fifo + capture file. The tee
  # normally exits on its own once the killed child closes fd2 (EOF); this just
  # guarantees no stray process/temp survives an interrupted run.
  if [[ -n $_ul_tee_pid ]] && kill -0 "$_ul_tee_pid" 2>/dev/null; then
    kill -TERM "$_ul_tee_pid" 2>/dev/null
    wait "$_ul_tee_pid" 2>/dev/null || true
  fi
  _ul_tee_pid=""
  [[ -n $_ul_err_fifo ]] && rm -f "$_ul_err_fifo"
  _ul_err_fifo=""
  [[ -n $_ul_step_err_file ]] && rm -f "$_ul_step_err_file"
  _ul_step_err_file=""

  # Clean dirty git state. This trap is armed only AFTER the ul_setup gate, which
  # guarantees no pre-existing non-ignored untracked files, so the porcelain check
  # + `git clean -fd` here target step-created files only (bead pg2-31h9y).
  if [[ -n $_UL_SCRIPT_DIR ]] && [[ -d "$_UL_SCRIPT_DIR/.git" ]]; then
    cd "$_UL_SCRIPT_DIR" 2>/dev/null || true
    if [[ -n "$(git status --porcelain --untracked-files=normal 2>/dev/null)" ]]; then
      git reset --hard HEAD 2>/dev/null || true
      git clean -fd 2>/dev/null || true
    fi
  fi

  # Restore fsmonitor (scoping rules documented on _ul_restore_fsmonitor).
  _ul_restore_fsmonitor

  # Exit with 128+signum so parent sees signal-like exit status
  if [[ $signal != "EXIT" ]]; then
    trap - "$signal" EXIT
    exit $((128 + $(kill -l "$signal")))
  fi
}

# Disable core.fsmonitor for the duration of the run, recording enough state for
# the restore below to be EXACT. Split out from ul_setup so both halves of the
# dance stay symmetric and can be unit-tested without running ul_setup's full
# path (which refreshes the index, and so cannot be exercised with fsmonitor
# live — the native daemon is wedged on some setups, bead pg2-mgcv5).
#
# Two different values matter here and must not be conflated:
#
#   * the EFFECTIVE (merged) value decides WHETHER the dance is needed — it is
#     what governs whether git spawns the native daemon at all;
#   * the repo-LOCAL value decides HOW to undo it.
#
# Conflating them is a real defect: writing a blanket `git config core.fsmonitor
# true` on restore pins a value that may have come from the user's GLOBAL config
# into this repo permanently, turning one global setting into per-repo drift on
# every run (bead pg2-znsmo; the split state recorded in pg2-pi5u1 is the
# symptom).
#
# --type=bool is what makes "enabled" correct. It normalises git's other boolean
# spellings (yes/on/1), and it FAILS on a hook-path fsmonitor
# (core.fsmonitor=/path/to/hook) — which is the right outcome, because a
# hook-based monitor runs no native daemon and creates no .ipc socket, so it
# needs no dance and must not be rewritten.
_ul_disable_fsmonitor() {
  _fsmonitor_was_active=false
  _fsmonitor_had_local=false
  _fsmonitor_local_value=""

  if [[ "$(git config --type=bool --get core.fsmonitor 2>/dev/null)" == "true" ]]; then
    _fsmonitor_was_active=true
    if _fsmonitor_local_value="$(git config --local --get core.fsmonitor 2>/dev/null)"; then
      _fsmonitor_had_local=true
    else
      _fsmonitor_local_value=""
    fi
    git config core.fsmonitor false
    git fsmonitor--daemon stop 2>/dev/null || true
  fi

  # Unconditional: a socket left behind by an earlier crashed run makes `nix
  # flake` import fail with "unsupported type" even when fsmonitor is already
  # off, so removal must not be gated on the dance above.
  rm -f .git/fsmonitor--daemon.ipc
}

# Restore the fsmonitor config _ul_disable_fsmonitor changed. Used as a
# NON-destructive EXIT/INT/TERM trap during ul_setup's pre-gate phase, where the
# working tree may still hold the user's uncommitted work — so _ul_cleanup's
# reset --hard / clean -fd must NOT run there.
_ul_restore_fsmonitor() {
  [[ ${_fsmonitor_was_active:-false} == "true" ]] || return 0

  if [[ ${_fsmonitor_had_local:-false} == "true" ]]; then
    # The repo owned the setting — put its own value back verbatim.
    git config core.fsmonitor "$_fsmonitor_local_value" 2>/dev/null || true
  else
    # The value was inherited from an outer scope (global/system). Drop only the
    # local override we added and let that outer scope govern again.
    git config --unset-all core.fsmonitor 2>/dev/null || true
  fi
}

_ul_ensure_pre_commit_hooks() {
  # Tier 1: does the flake declare install-pre-commit-hooks?
  # --no-link avoids polluting the project dir. Distinguish "flake does not
  # declare the attribute" (a legitimate silent skip) from a genuine build
  # failure, which was previously swallowed by a blanket `|| return 0`
  # (pg2-k8a6i): a real failure is now surfaced instead of hiding a broken
  # hook install.
  local drv_path errfile err
  errfile=$(mktemp)
  if ! drv_path=$(nix build .#install-pre-commit-hooks --no-link --print-out-paths 2>"$errfile"); then
    err=$(<"$errfile")
    rm -f "$errfile"
    if [[ $err == *"does not provide attribute"* ]]; then
      return 0 # attribute not declared → nothing to install
    fi
    echo "==> warning: 'nix build .#install-pre-commit-hooks' failed (not an attr-missing error); skipping hook install:" >&2
    printf '%s\n' "$err" >&2
    return 0
  fi
  rm -f "$errfile"

  # Tier 2: is the hook binary still valid (not GC'd)?
  #
  # `git rev-parse --git-path hooks` is the only correct resolution here, and its
  # output is ALREADY the full hooks dir — it must NOT be joined onto the repo
  # dir. It honours core.hooksPath when set (including an ABSOLUTE value, which
  # every clone in this workspace holds and which the old
  # "${_UL_SCRIPT_DIR}/${hooks_dir}" join doubled into a path that never existed),
  # and with core.hooksPath unset it resolves to the COMMON hooks dir for a LINKED
  # WORKTREE, which the old relative ".git/hooks" fallback could not (a worktree's
  # .git is a FILE). Either miss made this tier unreachable: it always fell to the
  # else branch, so every run reported "hook not found" and reinstalled, and the
  # GC check below never ran. --path-format=absolute is load-bearing — without it
  # the output can be relative to cwd (e.g. "../.git/hooks" from a subdir). A bare
  # `git` is in-repo here: ul_setup cd's to script_dir before calling, which
  # Tier 1's bare `.#install-pre-commit-hooks` flake ref already depends on.
  # See bead pg2-rltuo.
  local hooks_dir hook_file needs_install
  hooks_dir=$(git rev-parse --path-format=absolute --git-path hooks)
  hook_file="${hooks_dir}/pre-commit"
  needs_install=false

  if [[ -f $hook_file ]]; then
    # The GC test is FORMAT-AGNOSTIC by design: scan the hook for every
    # /nix/store path it NAMES, wherever it names it, and reinstall if any is
    # gone. It must NOT key on a particular line shape. The previous version
    # parsed the `^exec ` line on the assumption the store path was the exec
    # TARGET; prek's current template puts the path on its own `PREK=` line and
    # execs the VARIABLE, so the parse yielded the literal 7-character string
    # `"$PREK"` — never executable — and this tier reported a GC'd binary on
    # EVERY run once pg2-rltuo made it reachable. Retargeting the parse at the
    # `PREK=` line would break again at prek's next template change (this parse
    # had already been patched once for one, see pg2-k8a6i), so the extraction
    # is deliberately positional-agnostic. Bead pg2-hk08h.
    #
    # This tier is NOT redundant with the others and must not be dropped:
    # Tier 3 reinstalls only when the DERIVATION PATH changes, so a
    # garbage-collected binary under an UNCHANGED derivation is exactly the case
    # only Tier 2 catches. And although prek's hook falls back to a `prek` on
    # PATH, that fallback may resolve a DIFFERENT version than the pinned one,
    # so restoring the pinned binary has real value.
    #
    # -e, not -x: what a GC removes is the path's EXISTENCE, and the paths found
    # are no longer guaranteed to be the executable (they may be a config, a
    # directory, or a non-executable data path the hook references).
    # The character class is the store-name alphabet, which deliberately
    # excludes the `"` the paths are wrapped in, plus whitespace and the `:` of
    # a PATH list, so no closing quote or separator is captured. `|| true`
    # because grep exits 1 when the hook names none — a non-nix install has
    # nothing to validate, and an empty scan must leave needs_install false.
    local store_path
    while IFS= read -r store_path; do
      if [[ ! -e $store_path ]]; then
        echo "==> pre-commit hook binary missing (GC'd), reinstalling..."
        needs_install=true
        break
      fi
    done < <(grep -oE '/nix/store/[a-zA-Z0-9+._?=-]+(/[a-zA-Z0-9+._?=-]+)*' "$hook_file" | sort -u || true)
  else
    echo "==> pre-commit hook not found, installing..."
    needs_install=true
  fi

  # Tier 3: has the derivation changed since last install?
  if [[ $needs_install != "true" ]]; then
    local marker="$UL_STATE_DIR/$_UL_PROJECT/pre-commit-drv-path"
    if [[ -f $marker ]] && [[ "$(cat "$marker")" == "$drv_path" ]]; then
      return 0
    fi
    echo "==> pre-commit hooks config changed, reinstalling..."
    needs_install=true
  fi

  nix run .#install-pre-commit-hooks
  mkdir -p "$UL_STATE_DIR/$_UL_PROJECT"
  echo "$drv_path" >"$UL_STATE_DIR/$_UL_PROJECT/pre-commit-drv-path"
}

# Re-exec the calling script inside its flake's devShells.default if possible.
# Safe to call from any update-locks.sh as the first thing after sourcing this lib.
# Behavior:
#   - If IN_NIX_SHELL is already set, prints a notice and returns 0 (no re-exec).
#   - Otherwise makes a SINGLE `nix develop ... --command bash` entry. A sentinel
#     file distinguishes the outcomes once nix returns:
#       * sentinel still present -> the dev shell never started (e.g. broken
#         flake); prints a warning and returns 0 so the script can still run
#         with host tooling (and the user can fix the flake).
#       * sentinel gone -> the script ran inside the shell; exits with its status.
#   - Exports UL_LIB_DIR (when set) so the in-shell re-run reuses it instead of
#     resolving determine-ul-lib-dir a second time.
ul_reexec_in_dev_shell() {
  local script="$0"
  local script_dir
  script_dir="$(cd "$(dirname "$script")" && pwd)"

  # The dev shell to enter. Defaults to the script's directory, which is correct
  # when the flake sits at the repo root. Consumers whose flake lives in a
  # subdirectory export UL_FLAKE_DIR pointing at it (e.g. homelab's nix/), so
  # `nix develop` resolves the flake rather than erroring "not part of a flake".
  local flake_dir="${UL_FLAKE_DIR:-$script_dir}"

  if [[ -n ${IN_NIX_SHELL:-} ]]; then
    echo "==> already in nix shell (IN_NIX_SHELL=$IN_NIX_SHELL); using current shell" >&2
    return 0
  fi

  if [[ -n ${UL_LIB_DIR:-} ]]; then
    export UL_LIB_DIR
  fi

  echo "==> entering dev shell at $flake_dir..." >&2

  local sentinel
  sentinel="$(mktemp)"
  # The in-shell command removes the sentinel as its first act, so its presence
  # afterward means we never entered the shell. The `|| rc=$?` guard is essential:
  # consumer scripts run under `set -e`, and a bare `nix develop` that fails
  # (absent/broken flake, or a devShell that cannot build on this host — e.g. a
  # devShell pulling in hooks pinned to another system) would otherwise abort the
  # script HERE, before the sentinel check, defeating the host-tools fallback.
  # nix's own stderr is left visible (not suppressed) so the real error is shown.
  local rc=0
  # shellcheck disable=SC2016  # $UL_DEVSHELL_SENTINEL and $@ are expanded by the inner shell, intentionally
  UL_DEVSHELL_SENTINEL="$sentinel" \
    nix develop "$flake_dir" --command bash -c 'rm -f "$UL_DEVSHELL_SENTINEL"; exec bash "$@"' ul-reexec "$script" "$@" || rc=$?

  if [[ -e $sentinel ]]; then
    rm -f "$sentinel"
    echo "WARNING: nix develop failed at $flake_dir — falling back to system tools" >&2
    return 0
  fi
  rm -f "$sentinel"
  exit "$rc"
}

ul_setup() {
  local project_name="$1"
  local script_dir="$2"
  _UL_SCRIPT_DIR="$script_dir"

  # shellcheck disable=SC1091
  source "${_UL_LOCKS_LIB_DIR}/update-cache-lib.bash"
  ul_init "$project_name" "$script_dir"

  cd "$script_dir"

  # Disable fsmonitor before any flake evaluation — a live .ipc socket makes
  # `nix flake` import fail with "unsupported type". The pre-commit hook install
  # below evaluates the flake, so this is hoisted above the clean-tree gate.
  # Until the gate passes (full cleanup trap armed), use a NON-destructive trap
  # that only restores fsmonitor: the tree may still hold the user's uncommitted
  # work here, so _ul_cleanup's reset --hard / clean -fd must not run on an
  # early exit.
  _ul_disable_fsmonitor
  trap '_ul_restore_fsmonitor' EXIT INT TERM

  # A wedged/unreachable nix daemon fails every repo identically, so treat it as
  # an environmental abort (UL_RC_ABORT) — pn then stops the whole run instead of
  # marching into the same wall. ul_check_nix_daemon already printed actionable
  # guidance (update-cache-lib.bash). This runs under the non-destructive pre-gate
  # trap, so exiting here restores fsmonitor without touching the working tree.
  ul_check_nix_daemon || {
    echo "Aborting update: nix daemon is unhealthy." >&2
    exit "$UL_RC_ABORT"
  }

  # Ensure the pre-commit hook binary is installed/current BEFORE the clean-tree
  # gate — it evaluates the flake, so it must run after fsmonitor is disabled
  # above. The generated .pre-commit-config.yaml is a gitignored /nix/store
  # symlink (ADR 0016): regenerating it here never touches the tracked tree, so
  # it cannot trip the gate below and nothing is committed on its behalf.
  _ul_ensure_pre_commit_hooks

  # Gate on `git status --porcelain --untracked-files=normal` (NOT git diff):
  # git diff is tracked-only, so a pre-existing UNTRACKED user file would pass
  # and then be swept into a step commit by `git add -A` (_ul_commit_updated) or
  # destroyed by a rollback `git clean -fd`. Porcelain also lists non-ignored
  # untracked files, matching `git clean -fd`'s scope, so a passing gate
  # guarantees the tree holds only step-created files thereafter. The explicit
  # --untracked-files=normal defeats a user's `status.showUntrackedFiles=no`
  # git config, which would otherwise silently reintroduce this bug. (Ignored
  # files — e.g. the gitignored .pre-commit-config.yaml symlink, ADR 0016 — are
  # excluded by both porcelain and `git clean -fd`, so regenerating it is safe.)
  # Rejected alt (git add -u / tracked-only commit): steps legitimately create
  # new files that must commit (e.g. a first gomod2nix.toml). See bead pg2-31h9y.
  if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
    echo "ERROR: Working directory is not clean (tracked changes or untracked files present)."
    echo "       Commit, stash (git stash --include-untracked), or remove untracked files first."
    git status --short
    exit 1
  fi

  # Tree is clean of user changes — now safe to arm the full cleanup trap, which
  # rolls back per-step failures (and still restores fsmonitor on exit).
  trap '_ul_cleanup EXIT' EXIT
  trap '_ul_cleanup INT' INT
  trap '_ul_cleanup TERM' TERM

  _UL_STEPS_RAN=0
  _UL_STEPS_SUCCEEDED=0
  _UL_STEPS_FAILED=0
  _UL_STEPS_SKIPPED=0
  _UL_STEPS_DEFERRED=0
  _UL_STEPS_TRANSIENT=0
  _UL_FAILED_STEPS=()
  _UL_UPGRADED_STEPS=()
  _UL_UPGRADE_NOTES=()
}

ul_run_step() {
  local step_name="$1"
  local commit_msg="$2"
  shift 2

  if [[ $# -eq 0 ]]; then
    echo "FATAL: ul_run_step '${step_name}' requires a command"
    exit 1
  fi

  if ! ul_should_run "$step_name"; then
    _UL_STEPS_SKIPPED=$((_UL_STEPS_SKIPPED + 1))
    return 0
  fi

  # Porcelain (untracked included) so a leftover untracked file — meaning a
  # prior step's commit failed silently — is also caught here. See pg2-31h9y.
  if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
    echo "FATAL: workspace dirty before step '${step_name}'. Stopping."
    git status --short
    exit 1
  fi

  echo "==> ${step_name}..."
  _UL_STEPS_RAN=$((_UL_STEPS_RAN + 1))

  local rc=0
  local _ul_restore_e
  if [[ -o errexit ]]; then _ul_restore_e="set -e"; else _ul_restore_e="set +e"; fi
  set +e
  # The step runs in a backgrounded subshell so _UL_CHILD_PID lets the
  # EXIT/INT/TERM trap (_ul_cleanup) TERM and reap it for a clean rollback. This
  # does NOT SIGTTIN-hang a prompting step in the normal path (pg2-k8a6i):
  # update-locks.sh runs non-interactively (monitor mode off), so the subshell
  # shares the shell's process group and can read /dev/tty, while a step reading
  # plain stdin gets bash's async /dev/null. (A prompting step is an anti-pattern
  # here regardless — steps are meant to be non-interactive.)
  #
  # The step's stderr is captured for failure classification (ul_classify_step_failure)
  # while STILL streaming live to fd2 (pn tees fd2 into the terminal transcript). A
  # named fifo read by a tracked `tee` is race-free: the subshell's fd2 close makes
  # tee see EOF, and `wait "$_ul_tee_pid"` guarantees the capture file is complete
  # before we read it. `$!` still names the step subshell (backgrounded last), so
  # _UL_CHILD_PID and the SIGTERM path are unchanged; stdout is untouched (fd1).
  # Steps MUST be synchronous one-shot commands (the non-interactive contract
  # above): a step that backgrounds an fd2-inheriting process would hold the fifo
  # open and stall the flush wait.
  _ul_step_err_file="$(mktemp)"
  _ul_err_fifo="$(mktemp -u)"
  if mkfifo "$_ul_err_fifo" 2>/dev/null; then
    tee "$_ul_step_err_file" <"$_ul_err_fifo" >&2 &
    _ul_tee_pid=$!
    (
      set -e
      "$@"
    ) 2>"$_ul_err_fifo" &
    _UL_CHILD_PID=$!
    wait "$_UL_CHILD_PID"
    rc=$?
    _UL_CHILD_PID=""
    wait "$_ul_tee_pid" 2>/dev/null || true
    _ul_tee_pid=""
    rm -f "$_ul_err_fifo"
    _ul_err_fifo=""
  else
    # mkfifo unavailable — run without stderr capture (classifier then sees an
    # empty file → "hard", i.e. today's behavior). Never block the step on capture.
    : >"$_ul_step_err_file"
    (
      set -e
      "$@"
    ) &
    _UL_CHILD_PID=$!
    wait "$_UL_CHILD_PID"
    rc=$?
    _UL_CHILD_PID=""
  fi
  $_ul_restore_e

  if [[ $rc -eq 0 ]]; then
    if _ul_commit_updated "$step_name" "$commit_msg"; then
      _UL_STEPS_SUCCEEDED=$((_UL_STEPS_SUCCEEDED + 1))
    fi
  elif [[ $rc -eq $UL_RC_ATTEMPTED ]]; then
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    if _ul_commit_stamp_only "$step_name"; then
      echo "  ⊘ Step '${step_name}' attempted — no update applied (deferred)"
      _UL_STEPS_DEFERRED=$((_UL_STEPS_DEFERRED + 1))
    fi
  else
    # Non-zero, non-deferral: classify the captured stderr. Rollback is common to
    # all three sub-cases; only the accounting (and abort) differs.
    local _ul_class
    _ul_class="$(ul_classify_step_failure "$_ul_step_err_file")"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    case "$_ul_class" in
    transient)
      # Transient external-source failure: roll back, write NO stamp (so it is
      # retried next run, not skipped until the TTL), keep the run passing.
      echo "  ⟳ Step '${step_name}' hit a transient external-source error — rolled back, will retry next run"
      _UL_STEPS_TRANSIENT=$((_UL_STEPS_TRANSIENT + 1))
      ;;
    resource)
      # Resource exhaustion (disk full): every later step/repo would fail the
      # same way, so abort the whole run with the sentinel pn recognizes.
      echo "  ✗ Step '${step_name}' failed: out of a system resource (disk full)." >&2
      echo "     Free space, then re-run 'pn workspace update'. Aborting the run." >&2
      rm -f "$_ul_step_err_file"
      _ul_step_err_file=""
      exit "$UL_RC_ABORT"
      ;;
    *)
      echo "  ✗ Step '${step_name}' failed (exit code ${rc})"
      _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
      _UL_FAILED_STEPS+=("$step_name")
      ;;
    esac
  fi

  rm -f "$_ul_step_err_file"
  _ul_step_err_file=""
}

# Record what a content-changing step upgraded, for the end-of-run summary.
# Best-effort and never fails the step: extracts `version = "X"` → `version = "Y"`
# deltas from changed *.nix / *.toml files, and the set of flake.lock inputs whose
# locked revision moved. Falls back to the bare step name when it cannot
# characterize the change. Called BEFORE `nix fmt` so reformatting can't hide the
# version lines. Each `|| true` guards against the caller's `set -e`/pipefail
# (e.g. `diff` exits 1 on differing inputs, which is the normal case here).
_ul_record_upgrade() {
  local step_name="$1"
  local detail="" olds="" news="" nix_diff=""

  # Package version-string bumps (covers `nix-update -F` and hand-pinned packages).
  # --no-ext-diff/--no-textconv/--no-color force git's built-in unified diff so this
  # parses `-`/`+` lines regardless of the repo's configured diff driver (e.g.
  # difftastic emits columnar structural output that has no `-`/`+` lines).
  nix_diff=$(git diff --no-ext-diff --no-textconv --no-color HEAD -- '*.nix' '*.toml' 2>/dev/null) || true
  if [[ -n $nix_diff ]]; then
    olds=$(printf '%s\n' "$nix_diff" | sed -nE 's/^-[[:space:]]*version = "([^"]+)".*/\1/p' | paste -sd, - 2>/dev/null) || true
    news=$(printf '%s\n' "$nix_diff" | sed -nE 's/^\+[[:space:]]*version = "([^"]+)".*/\1/p' | paste -sd, - 2>/dev/null) || true
    if [[ -n $news ]]; then
      if [[ -n $olds ]]; then detail="${olds} → ${news}"; else detail="$news"; fi
    fi
  fi

  # flake.lock input bumps: name the inputs whose locked rev/narHash changed.
  if ! git diff --quiet HEAD -- flake.lock 2>/dev/null; then
    local inputs=""
    if command -v jq >/dev/null 2>&1; then
      local before="" after=""
      before=$(git show HEAD:flake.lock 2>/dev/null) || true
      after=$(cat flake.lock 2>/dev/null) || true
      inputs=$(
        diff \
          <(printf '%s' "${before:-{\}}" | jq -r '(.nodes // {}) | to_entries[] | "\(.key)=\(.value.locked.rev // .value.locked.narHash // "")"' 2>/dev/null | sort) \
          <(printf '%s' "${after:-{\}}" | jq -r '(.nodes // {}) | to_entries[] | "\(.key)=\(.value.locked.rev // .value.locked.narHash // "")"' 2>/dev/null | sort) \
          2>/dev/null | sed -nE 's/^> ([^=]+)=.*/\1/p' | sort -u | paste -sd, - 2>/dev/null
      ) || true
    fi
    if [[ -n $inputs ]]; then
      if [[ -n $detail ]]; then detail="${detail}; inputs: ${inputs}"; else detail="inputs: ${inputs}"; fi
    elif [[ -z $detail ]]; then
      detail="flake.lock updated"
    fi
  fi

  _UL_UPGRADED_STEPS+=("$step_name")
  if [[ -n $detail ]]; then
    _UL_UPGRADE_NOTES+=("${step_name}: ${detail}")
  else
    _UL_UPGRADE_NOTES+=("$step_name")
  fi
  return 0
}

# Commit a successful step: format content if any changed, write the stamp,
# and commit everything in one commit (content + stamp, or stamp-only on a
# no-op success). On fmt/commit failure: roll back, record failure, return 1.
_ul_commit_updated() {
  local step_name="$1" commit_msg="$2"
  if ! git diff --quiet || ! git diff --cached --quiet; then
    _ul_record_upgrade "$step_name"
    if ! nix fmt; then
      echo "  ✗ Step '${step_name}' nix fmt failed"
      git reset --hard HEAD 2>/dev/null || true
      git clean -fd 2>/dev/null || true
      _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
      _UL_FAILED_STEPS+=("$step_name")
      return 1
    fi
  fi
  ul_write_stamp "$step_name"
  if ! git add -A || ! git commit -m "$commit_msg" >/dev/null; then
    echo "  ✗ Step '${step_name}' commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}

# Commit only the step's stamp (used after a deferral rolled back content).
_ul_commit_stamp_only() {
  local step_name="$1"
  ul_write_stamp "$step_name"
  if ! git add -- "$_UL_STAMP_DIR/$step_name" ||
    ! git commit -m "update-locks: ${step_name} attempted, no update applied" >/dev/null; then
    echo "  ✗ Step '${step_name}' stamp commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}

ul_finalize() {
  echo ""
  echo "=== Update Summary ==="
  echo "  Ran:     ${_UL_STEPS_RAN}"
  echo "  Passed:  ${_UL_STEPS_SUCCEEDED}"
  echo "  Upgraded: ${#_UL_UPGRADED_STEPS[@]}"
  echo "  Deferred: ${_UL_STEPS_DEFERRED}"
  echo "  Transient: ${_UL_STEPS_TRANSIENT}"
  echo "  Failed:  ${_UL_STEPS_FAILED}"
  echo "  Skipped: ${_UL_STEPS_SKIPPED}"

  # Machine-readable result line for the bash↔Go boundary (ADR 0020). pn runs
  # update-locks.sh and, on the green path, sees only exit 0 — it has NO view of
  # the transient count, so a step misclassified transient every run silently
  # skips a real update while keeping `pn workspace update` green. pn parses the
  # LAST "UL_RESULT transient=<N>" line from this script's captured stdout and
  # warns when N>0. Emitted here (before BOTH the failure `exit 1` and the
  # success `exit 0` below) so the count always crosses the boundary; a resource
  # abort exits from ul_run_step earlier and never reaches this line (pn stops
  # the whole run on that exit anyway). Stable key=value form, extensible later.
  echo "UL_RESULT transient=${_UL_STEPS_TRANSIENT}"

  if [[ ${#_UL_UPGRADED_STEPS[@]} -gt 0 ]]; then
    echo ""
    echo "Upgrades applied:"
    local note
    for note in "${_UL_UPGRADE_NOTES[@]}"; do
      echo "  ⬆ ${note}"
    done
  fi

  if [[ ${_UL_STEPS_FAILED} -gt 0 ]]; then
    echo ""
    echo "Failed steps:"
    for step in "${_UL_FAILED_STEPS[@]}"; do
      echo "  ✗ ${step}"
    done
    exit 1
  fi

  echo ""
  if [[ ${#_UL_UPGRADED_STEPS[@]} -eq 0 ]]; then
    echo "✓ All steps completed successfully — no upgrades (everything already current)."
  else
    echo "✓ All steps completed successfully (${#_UL_UPGRADED_STEPS[@]} upgrade(s) applied)!"
  fi
  exit 0
}
