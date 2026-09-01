# shellcheck shell=bash
# git-fixture-harness.bash — shared hermetic-by-construction bats git-fixture
# harness (design: pg2-gucfd, decided 2026-08-29; implementation: epic
# pg2-ljn47 / work packet pg2-ljn47.1).
#
# WHY THIS EXISTS: 17 bats suites across 4 repos hand-rolled their own git
# fixture with no GIT_DIR-family scrub at all, and the 12 that WERE protected
# copy-pasted the same six-line env-var-unset block into three different
# repos (pg2-jjlm8). Three separate passes over even the suites in THIS repo
# (pg2-klyn6, pg2-7hr6o, pg2-zjcp6) each closed one half of the problem and
# left the rest open — because enumerate-and-unset is only ever as complete
# as the enumeration. pg2-8wnhc's ruling ("hermetic by construction, not by
# scrubbing") is applied here the way it was already applied to the Go side's
# x/gittest (D2/D6 on pg2-67h4y): no fixture created by this library can
# reach a real repository or a real HOME, regardless of which env vars a
# future git version invents.
#
# THE THREE STRUCTURAL MECHANISMS (pg2-gucfd D2):
#   1. GIT_CEILING_DIRECTORIES pinned to a physical path that contains every
#      fixture this call creates. git's own repository-discovery walk-up
#      refuses to search — or search past — a ceiling directory, so a
#      fixture's real repo can never be shadowed by (or accidentally resolve
#      to) something outside the fixture tree, even from a non-repo
#      subdirectory a caller forgot to `-C` into explicitly.
#   2. gfh_reset_env rebuilds the exported environment from an ALLOWLIST, not
#      a denylist of known-leaky GIT_*/XDG_*/CI_* names. Every exported
#      variable not on the allowlist is unset — including any GIT_DIR-family
#      variable inherited from a linked-worktree hook environment (the exact
#      pg2-jjlm8/pg2-12795 mechanism), and including one this library's
#      author never heard of, because the allowlist names what MAY survive
#      rather than what must be removed.
#   3. HOME is a fresh, empty mktemp directory on every gfh_setup call, and
#      GIT_CONFIG_SYSTEM is pointed at /dev/null — the one config scope an
#      env reset cannot neutralise (a real /etc/gitconfig).
#
# FIXTURE IDENTITY (pg2-gucfd D3): every repo this library creates gets a
# PER-SUITE distinct identity under the domain "bashfixture.invalid" —
# deliberately different from the Go side's "gitfixture.invalid" (x/gittest),
# so a leaked identity string alone tells forensics which side produced it,
# and a distinct per-suite local part means a leak identifies its own
# offending suite without a workspace-wide grep.
#
# NO AUTOMATED ENFORCEMENT (pg2-gucfd D4): nothing gates a bats file on using
# this library. Suites adopt it by convention/review when touched — see the
# beads blocked on this one landing (pg2-31f13, pg2-whgx5, pg2-vn1nk) for the
# migration work.
#
# CONTRACT — see lib/tests/test-git-fixture-harness-lib.bats for the
# executable spec.
#
#   gfh_setup <suite-name>
#     One call per bats test, before any git call the test makes. Sets:
#       GFH_ROOT   — outer fixture root (mktemp -d); rm -rf'd by gfh_teardown
#       GFH_WORK   — the GIT_CEILING_DIRECTORIES boundary itself
#                    (GFH_ROOT/work); everything below is confined here
#       GFH_REPO   — a real, initialised, single-commit repo at
#                    GFH_WORK/repo, hooks disabled, identity
#                    "<suite-name>@bashfixture.invalid"
#       GFH_SUITE  — the suite name passed in
#       HOME                    (exported) — fresh empty dir under GFH_WORK
#       GIT_CEILING_DIRECTORIES (exported) — physical path of GFH_WORK
#       GIT_CONFIG_SYSTEM       (exported) — /dev/null
#     A caller needing anything beyond this minimal set (a mock PATH prefix,
#     XDG_STATE_HOME, a second GIT_CONFIG_GLOBAL, ...) exports it AFTER this
#     call returns — the same "opt in explicitly, never touch the real one"
#     contract every hermetic suite in this repo already follows.
#
#   gfh_teardown
#     rm -rf "$GFH_ROOT". Call from the test's own teardown().
#
#   gfh_init_repo <path> <suite-name>
#     Lower-level primitive: git-init a repo at <path> with hooks disabled
#     and the per-suite fixture identity, WITHOUT touching HOME, env, or
#     GIT_CEILING_DIRECTORIES. For a caller that needs a SECOND repository
#     inside one gfh_setup call (e.g. a bare remote for push/fetch tests) —
#     gfh_setup itself calls this for GFH_REPO.
#
#   gfh_identity_email <suite-name> / gfh_identity_name <suite-name>
#     Pure functions printing the email/name half of the fixture identity.
#     Useful for a test asserting the identity it expects to see.
#
#   gfh_reset_env
#     Rebuilds the exported environment from the allowlist. gfh_setup calls
#     this itself; exposed separately for a caller that wants the scrub
#     without the rest of gfh_setup's repo-creation side effects.

# The bash-side fixture-identity domain (D3).
GFH_IDENTITY_DOMAIN="bashfixture.invalid"

# Exported-variable names gfh_reset_env preserves across its rebuild.
# Everything else currently exported in the calling shell is unset.
#
#   - BATS_*  — bats' own machinery. gfh_reset_env runs IN-PROCESS with bats
#     (there is no subshell to reset instead), so unsetting these would break
#     `run`, `skip`, and tmpdir resolution for the rest of the current test
#     and every later test bats runs in the same file.
#   - GFH_*   — this library's own state (GFH_ROOT, GFH_WORK, GFH_REPO,
#     GFH_SUITE), so a caller can read them back after the reset.
#   - The bare-minimum shell/process plumbing git, bash and coreutils need to
#     keep functioning at all.
_gfh_allow_exact=(
  PATH
  SHELL
  TERM
  TMPDIR
  PWD
  OLDPWD
  IFS
  LANG
  LC_ALL
  LC_CTYPE
  USER
  LOGNAME
  SHLVL
)

# Rebuild the exported environment from the allowlist above. Idempotent and
# safe to call more than once. `compgen -e` enumerates only EXPORTED shell
# variables — shell functions and unexported locals are untouched.
gfh_reset_env() {
  local var keep allowed
  for var in $(compgen -e); do
    case "$var" in
    BATS_* | GFH_*) continue ;;
    esac
    keep=0
    for allowed in "${_gfh_allow_exact[@]}"; do
      if [[ $var == "$allowed" ]]; then
        keep=1
        break
      fi
    done
    if [[ $keep -eq 0 ]]; then
      unset "$var"
    fi
  done
}

# Print the EMAIL half of the per-suite fixture identity.
gfh_identity_email() {
  local suite="$1"
  printf '%s@%s\n' "$suite" "$GFH_IDENTITY_DOMAIN"
}

# Print the NAME half of the per-suite fixture identity.
gfh_identity_name() {
  local suite="$1"
  printf '%s fixture\n' "$suite"
}

# Initialise a real git repository at $1 with hooks disabled (D2) and this
# library's per-suite identity (D3) as its LOCAL user.email/user.name. Does
# NOT touch HOME, the exported environment, or GIT_CEILING_DIRECTORIES —
# gfh_setup below calls this for GFH_REPO; a caller wanting an EXTRA repo
# under one gfh_setup call (a bare remote, a second clone) calls this
# directly for it.
gfh_init_repo() {
  local repo="$1" suite="$2"
  mkdir -p "$repo"
  command git -C "$repo" init -q -b main
  # Hooks disabled (D2): core.hooksPath pointed at a non-directory means git
  # can never resolve a hook file under it, so no hook — planted, inherited,
  # or supplied by an init template — ever runs for this repo.
  command git -C "$repo" config core.hooksPath /dev/null
  command git -C "$repo" config user.email "$(gfh_identity_email "$suite")"
  command git -C "$repo" config user.name "$(gfh_identity_name "$suite")"
}

# Full harness setup for ONE bats test. See the CONTRACT block above this
# function for exactly what it sets.
gfh_setup() {
  local suite="$1"
  # shellcheck disable=SC2034  # read by callers after gfh_setup returns, not in this file
  GFH_SUITE="$suite"

  GFH_ROOT="$(mktemp -d)"

  gfh_reset_env

  # GFH_WORK, not GFH_ROOT itself, is the ceiling boundary: this lets a
  # regression test plant a decoy repository IN GFH_ROOT (above the ceiling,
  # but still entirely inside this call's own private mktemp tree — never
  # touching the shared system temp directory) to prove the ceiling holds,
  # without any risk to concurrently running tests or processes.
  GFH_WORK="$GFH_ROOT/work"
  mkdir -p "$GFH_WORK"

  GIT_CEILING_DIRECTORIES="$(cd "$GFH_WORK" && pwd -P)"
  export GIT_CEILING_DIRECTORIES

  # The one config scope an env reset cannot neutralise: a real
  # /etc/gitconfig is a file, not an environment variable.
  GIT_CONFIG_SYSTEM=/dev/null
  export GIT_CONFIG_SYSTEM

  HOME="$GFH_WORK/home"
  mkdir -p "$HOME"
  export HOME

  GFH_REPO="$GFH_WORK/repo"
  gfh_init_repo "$GFH_REPO" "$suite"
  printf 'initial\n' >"$GFH_REPO/file.txt"
  command git -C "$GFH_REPO" add file.txt
  command git -C "$GFH_REPO" commit -q -m "initial"
}

# Tear down everything gfh_setup created for this test. Call from teardown().
gfh_teardown() {
  if [[ -n ${GFH_ROOT:-} ]]; then
    rm -rf "$GFH_ROOT"
  fi
}
