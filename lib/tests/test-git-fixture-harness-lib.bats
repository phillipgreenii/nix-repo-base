#!/usr/bin/env bats
# bats file_tags=type:unit
# shellcheck disable=SC1090

# Executable spec for lib/scripts/git-fixture-harness.bash (design: pg2-gucfd;
# implementation: epic pg2-ljn47 / work packet pg2-ljn47.1). See that file's
# header for the full contract and design rationale this suite asserts.

# Resolve the library the same way the sibling suites in this directory do
# (test-update-locks-lib.bats, test-update-cache-lib.bats): the
# test-update-locks-lib nix check runs `bats` over this whole lib/tests/
# directory as one derivation and exports UL_LIB_SCRIPTS_DIR pointing at the
# real lib/scripts/ directory (flake-modules/checks.nix testUpdateLocksLib) —
# there is no per-file scoping, so every suite in this directory shares that
# one var. Under that check, lib/tests/ is copied into its own standalone
# store path with no sibling scripts/ directory, so the $BATS_TEST_DIRNAME/
# ../scripts fallback (a plain local `bats lib/tests` run) does not exist and
# must not be reached first.
if [[ -n ${UL_LIB_SCRIPTS_DIR:-} ]]; then
  GFH_LIB="$UL_LIB_SCRIPTS_DIR/git-fixture-harness.bash"
else
  GFH_LIB="$(cd "$BATS_TEST_DIRNAME/../scripts" && pwd)/git-fixture-harness.bash"
fi

# This suite tests the harness ITSELF, so it deliberately does NOT source
# git-fixture-harness.bash in its own setup() the way a CONSUMER suite would
# — that would make bats' own ambient environment (real HOME, real PATH
# extras, etc.) the very thing under test disappear before each @test even
# starts. Instead each @test sources the library and calls gfh_setup/
# gfh_teardown itself, inside `run bash -c '...'` where the property under
# test requires a subprocess (the env-reset tests must not scrub the CURRENT
# bats shell), or directly when it doesn't.

# Run git with the GIT_DIR family removed, so `-C`/cwd discovery is honoured
# even when this suite itself runs inside a commit hook (pg2-6drqh).
_gfh_clean_git() {
  env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR -u GIT_INDEX_FILE \
    -u GIT_PREFIX -u GIT_OBJECT_DIRECTORY git "$@"
}

teardown() {
  # Best-effort: any @test that itself called gfh_setup (in-process, not via
  # `run bash -c`) leaves GFH_ROOT set in THIS shell.
  if [[ -n ${GFH_ROOT:-} ]]; then
    rm -rf "$GFH_ROOT"
  fi
}

# --- gfh_setup: repo creation -----------------------------------------------

@test "gfh_setup creates a real, clean, single-commit repo at GFH_REPO" {
  source "$GFH_LIB"
  gfh_setup "repo-creation"

  [ -d "$GFH_REPO/.git" ]
  run command git -C "$GFH_REPO" rev-parse --verify HEAD
  [ "$status" -eq 0 ]
  run command git -C "$GFH_REPO" status --porcelain
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(cat "$GFH_REPO/file.txt")" = "initial" ]
}

@test "gfh_setup: the repo's branch is main, never init.defaultBranch/git's compiled-in default" {
  source "$GFH_LIB"
  gfh_setup "branch-name"

  run command git -C "$GFH_REPO" symbolic-ref --short HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "main" ]
}

# --- fixture identity (pg2-gucfd D3) ----------------------------------------

@test "gfh_identity_email prints <suite>@bashfixture.invalid" {
  source "$GFH_LIB"
  [ "$(gfh_identity_email "my-suite")" = "my-suite@bashfixture.invalid" ]
}

@test "gfh_identity_name prints a name derived from the suite" {
  source "$GFH_LIB"
  [[ "$(gfh_identity_name "my-suite")" == *"my-suite"* ]]
}

@test "gfh_setup: GFH_REPO's local identity matches the suite's bashfixture.invalid email" {
  source "$GFH_LIB"
  gfh_setup "identity-suite"

  [ "$(command git -C "$GFH_REPO" config user.email)" = "identity-suite@bashfixture.invalid" ]
  [[ "$(command git -C "$GFH_REPO" config user.name)" == *"identity-suite"* ]]
}

@test "gfh_setup: two different suite names get two DISTINCT identities" {
  source "$GFH_LIB"
  gfh_setup "suite-one"
  local email_one
  email_one="$(command git -C "$GFH_REPO" config user.email)"
  local root_one="$GFH_ROOT"

  gfh_setup "suite-two"
  local email_two
  email_two="$(command git -C "$GFH_REPO" config user.email)"
  rm -rf "$root_one"

  [ "$email_one" != "$email_two" ]
  [ "$email_one" = "suite-one@bashfixture.invalid" ]
  [ "$email_two" = "suite-two@bashfixture.invalid" ]
}

@test "gfh_identity_email: the DOMAIN half is bashfixture.invalid, deliberately distinct from the Go side's gitfixture.invalid" {
  source "$GFH_LIB"
  [[ "$(gfh_identity_email "s")" == *"@bashfixture.invalid" ]]
  [[ "$(gfh_identity_email "s")" != *"@gitfixture.invalid" ]]
}

# --- hooks disabled (pg2-gucfd D2) ------------------------------------------

@test "gfh_setup: core.hooksPath is disabled (points at a non-directory)" {
  source "$GFH_LIB"
  gfh_setup "hooks-disabled"

  [ "$(command git -C "$GFH_REPO" config core.hooksPath)" = "/dev/null" ]
}

@test "gfh_setup: a planted pre-commit hook never runs" {
  source "$GFH_LIB"
  gfh_setup "hooks-never-run"

  local sentinel="$GFH_ROOT/hook-ran"
  cat >"$GFH_REPO/.git/hooks/pre-commit" <<HOOK
#!/bin/sh
touch "$sentinel"
exit 1
HOOK
  chmod +x "$GFH_REPO/.git/hooks/pre-commit"

  echo more >"$GFH_REPO/file.txt"
  command git -C "$GFH_REPO" commit -q -am "second"

  [ ! -e "$sentinel" ]
}

@test "gfh_init_repo: hooks disabled on a repo used standalone (no gfh_setup)" {
  source "$GFH_LIB"
  local root
  root="$(mktemp -d)"
  gfh_init_repo "$root/second-repo" "standalone-suite"

  [ "$(command git -C "$root/second-repo" config core.hooksPath)" = "/dev/null" ]
  [ "$(command git -C "$root/second-repo" config user.email)" = "standalone-suite@bashfixture.invalid" ]
  rm -rf "$root"
}

# Regression for pg2-510ya: a stray [user] block with exactly this fixture
# identity ("standalone-suite@bashfixture.invalid") was found in the
# CANONICAL clone's real .git/config after a bats run. Root cause: `-C
# <path>` does NOT protect against a leaked GIT_DIR -- when GIT_DIR is set,
# git's repository discovery is pinned there and bypasses `-C` entirely, so
# an inherited GIT_DIR (e.g. from a linked-worktree hook environment, the
# pg2-jjlm8/pg2-12795 mechanism) silently redirects gfh_init_repo's "init a
# fixture repo at <path>" onto the leaked real repo instead. This is exactly
# the standalone-usage shape of the test above, but WITH that leak present
# and NO prior gfh_setup call to have scrubbed it (gfh_init_repo's own
# documented, intended standalone use) -- discriminating: it must prove BOTH
# that the leaked repo is untouched AND that the real target got initialised,
# not just that the call didn't error.
@test "gfh_init_repo: a leaked GIT_DIR does not redirect init/config onto it (pg2-510ya)" {
  local leaked target
  leaked="$(mktemp -d)"
  target="$(mktemp -d)"
  # pg2-6drqh: this test's OWN setup must be scrubbed too. Run raw under a
  # commit-hook environment (GIT_DIR exported by a linked-worktree commit),
  # `git init "$leaked"` re-inits the REAL repo and the three `config` calls
  # below wrote core.hooksPath=/dev/null, user.email=real@example.com and
  # user.name="Real User" into the canonical clone's .git/config -- the
  # exact leaked keys that bead reported. Every git call here (setup AND
  # assertions: a read under a leaked GIT_DIR would pass vacuously against
  # the real repo) goes through _gfh_clean_git.
  _gfh_clean_git init -q -b main "$leaked"
  _gfh_clean_git -C "$leaked" config core.hooksPath /dev/null
  _gfh_clean_git -C "$leaked" config user.email "real@example.com"
  _gfh_clean_git -C "$leaked" config user.name "Real User"

  run env GIT_DIR="$leaked/.git" bash -c "
    source '$GFH_LIB'
    gfh_init_repo '$target/second-repo' 'leak-suite'
  "
  [ "$status" -eq 0 ]

  # The leaked repo's real identity must survive untouched.
  [ "$(_gfh_clean_git -C "$leaked" config user.email)" = "real@example.com" ]
  # The actual target must be the one that got initialised with the fixture
  # identity -- not left without a .git at all (what happened before the fix).
  [ -d "$target/second-repo/.git" ]
  [ "$(_gfh_clean_git -C "$target/second-repo" config user.email)" = "leak-suite@bashfixture.invalid" ]

  rm -rf "$leaked" "$target"
}

# Regression for pg2-6drqh: run the leak test above (and the whole bats file
# machinery around it) the way a linked-worktree commit hook does -- with
# GIT_DIR/GIT_INDEX_FILE exported and pointing at a "canonical" repo -- and
# assert that repo's .git/config is BYTE-IDENTICAL afterwards. Nested bats
# runs only the pg2-510ya test (filter excludes this test, so no recursion).
@test "suite run under a leaked GIT_DIR leaves the pointed-at repo's config byte-identical (pg2-6drqh)" {
  command -v bats >/dev/null || skip "bats not on PATH"
  local canon before after
  canon="$(mktemp -d)"
  _gfh_clean_git init -q -b main "$canon"
  before="$(cat "$canon/.git/config")"

  run env GIT_DIR="$canon/.git" GIT_INDEX_FILE="$canon/.git/index" \
    UL_LIB_SCRIPTS_DIR="$(dirname "$GFH_LIB")" \
    bats --filter 'pg2-510ya' "$BATS_TEST_FILENAME"
  [ "$status" -eq 0 ]

  after="$(cat "$canon/.git/config")"
  [ "$before" = "$after" ]
  rm -rf "$canon"
}

# --- HOME isolation ----------------------------------------------------------

@test "gfh_setup: HOME is a fresh, empty directory, never the developer's real one" {
  source "$GFH_LIB"
  local real_home="$HOME"
  gfh_setup "home-isolation"

  [ "$HOME" != "$real_home" ]
  [ -d "$HOME" ]
  [ -z "$(ls -A "$HOME")" ]
}

@test "gfh_setup: a SECOND call gets a DIFFERENT fresh HOME (no reuse across tests)" {
  source "$GFH_LIB"
  gfh_setup "home-first"
  local home_one="$HOME"
  local root_one="$GFH_ROOT"

  gfh_setup "home-second"
  local home_two="$HOME"
  rm -rf "$root_one"

  [ "$home_one" != "$home_two" ]
}

# --- GIT_CONFIG_SYSTEM neutralised ------------------------------------------

@test "gfh_setup: GIT_CONFIG_SYSTEM is neutralised to /dev/null" {
  source "$GFH_LIB"
  gfh_setup "system-config"

  [ "$GIT_CONFIG_SYSTEM" = "/dev/null" ]
}

# --- GIT_CEILING_DIRECTORIES wiring -----------------------------------------

@test "gfh_setup: GIT_CEILING_DIRECTORIES is exported and is GFH_WORK's own physical path" {
  source "$GFH_LIB"
  gfh_setup "ceiling-wiring"

  [ -n "$GIT_CEILING_DIRECTORIES" ]
  [ -d "$GIT_CEILING_DIRECTORIES" ]
  local physical
  physical="$(cd "$GFH_WORK" && pwd -P)"
  [ "$GIT_CEILING_DIRECTORIES" = "$physical" ]
}

# --- GIT_CEILING_DIRECTORIES: the by-construction backstop (pg2-8wnhc) -----
#
# Discriminating pair: a decoy .git is planted at GFH_ROOT — one level ABOVE
# GFH_WORK (the ceiling), but still entirely inside THIS test's own private
# mktemp tree, never touching the shared system temp directory, so this is
# safe under parallel/`bats --jobs` runs. From a non-repo directory nested
# under GFH_WORK, git's upward discovery walk would (without the ceiling)
# eventually reach and resolve to that decoy; WITH the ceiling in place
# (git's own semantics: a ceiling directory is excluded from consideration,
# and the walk stops there) it must fail instead. Both halves are asserted so
# this cannot pass vacuously (e.g. if the decoy were simply unreachable for
# an unrelated reason).

_gfh_plant_decoy_and_target() {
  # $GFH_ROOT/.git makes the CEILING'S PARENT a real repo — never inside
  # GFH_WORK itself, so it can only be found by walking OUT of GFH_WORK.
  command git init -q -b main "$GFH_ROOT" >/dev/null
  command git -C "$GFH_ROOT" config core.hooksPath /dev/null
  mkdir -p "$GFH_WORK/not-a-repo/nested"
  printf '%s\n' "$GFH_WORK/not-a-repo/nested"
}

@test "GIT_CEILING_DIRECTORIES: WITH the ceiling set, discovery does NOT find a decoy repo above it" {
  source "$GFH_LIB"
  gfh_setup "ceiling-blocks"
  local target
  target="$(_gfh_plant_decoy_and_target)"

  run env GIT_CEILING_DIRECTORIES="$GIT_CEILING_DIRECTORIES" bash -c "cd '$target' && command git rev-parse --show-toplevel"
  [ "$status" -ne 0 ]
}

@test "GIT_CEILING_DIRECTORIES: WITHOUT it, the SAME layout leaks into the decoy (proves the guard is discriminating)" {
  source "$GFH_LIB"
  gfh_setup "ceiling-discriminating"
  local target
  target="$(_gfh_plant_decoy_and_target)"

  run env -u GIT_CEILING_DIRECTORIES bash -c "cd '$target' && command git rev-parse --show-toplevel"
  [ "$status" -eq 0 ]
  [ "$output" = "$(cd "$GFH_ROOT" && pwd -P)" ]
}

# --- gfh_reset_env: allowlist, not a denylist (pg2-8wnhc) -------------------
#
# The discriminating property: a var this library's author never enumerated
# must STILL be removed, because the mechanism is "keep what's allowed" not
# "remove what's known-bad". GIT_TOTALLY_MADE_UP_FUTURE_VAR stands in for any
# such variable, including a real GIT_DIR-family one.

@test "gfh_reset_env: removes an arbitrary exported var NOT on any denylist, by construction" {
  run bash -c "
    source '$GFH_LIB'
    export GIT_TOTALLY_MADE_UP_FUTURE_VAR=leak
    export GIT_DIR=/some/leaked/worktree/gitdir
    export XDG_CONFIG_HOME=/some/real/xdg/config
    gfh_reset_env
    echo \"made_up=\${GIT_TOTALLY_MADE_UP_FUTURE_VAR:-gone}\"
    echo \"git_dir=\${GIT_DIR:-gone}\"
    echo \"xdg=\${XDG_CONFIG_HOME:-gone}\"
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"made_up=gone"* ]]
  [[ "$output" == *"git_dir=gone"* ]]
  [[ "$output" == *"xdg=gone"* ]]
}

# --- gfh_reset_env without `compgen` (pg2-ji9rf, root cause pg2-31f13) -------
#
# nixpkgs' non-interactive `bash` (the one nix check derivations run bats
# under) is built without programmable completion, so `compgen -e` fails with
# "command not found" and a `$(compgen -e)` loop silently scrubs nothing. We
# reproduce that on ANY bash by disabling the builtin (`enable -n compgen`),
# so the test is discriminating under an interactive bash AND under the nix
# sandbox bash (where compgen is already absent and `enable -n` is a no-op
# on it, hence `|| true`).

@test "gfh_reset_env: scrubs GIT_DIR, GIT_WORK_TREE and an unlisted var even when compgen is unavailable" {
  run bash -c "
    enable -n compgen 2>/dev/null || true
    if compgen -e >/dev/null 2>&1; then echo 'compgen still usable' >&2; exit 90; fi
    source '$GFH_LIB'
    export GIT_DIR=/some/leaked/gitdir
    export GIT_WORK_TREE=/some/leaked/worktree
    export FIXTURE_T_UNLISTED_VAR=leak
    gfh_reset_env
    echo \"git_dir=\${GIT_DIR:-gone}\"
    echo \"work_tree=\${GIT_WORK_TREE:-gone}\"
    echo \"unlisted=\${FIXTURE_T_UNLISTED_VAR:-gone}\"
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"git_dir=gone"* ]]
  [[ "$output" == *"work_tree=gone"* ]]
  [[ "$output" == *"unlisted=gone"* ]]
}

@test "gfh_reset_env: a compgen -e based scrub is a silent no-op when compgen is unavailable (control)" {
  # The OLD implementation, inlined, run under the same compgen-less bash.
  # It must leave the leaked vars in place -- proving the test above fails
  # against the old compgen -e implementation and passes only with export -p.
  run bash -c '
    enable -n compgen 2>/dev/null || true
    old_reset_env() {
      local var
      for var in $(compgen -e 2>/dev/null); do
        case "$var" in PATH|BATS_*|GFH_*) continue ;; esac
        unset "$var"
      done
    }
    export GIT_DIR=/some/leaked/gitdir
    export GIT_WORK_TREE=/some/leaked/worktree
    export FIXTURE_T_UNLISTED_VAR=leak
    old_reset_env
    echo "git_dir=${GIT_DIR:-gone}"
    echo "work_tree=${GIT_WORK_TREE:-gone}"
  '
  [ "$status" -eq 0 ]
  [[ "$output" == *"git_dir=/some/leaked/gitdir"* ]]
  [[ "$output" == *"work_tree=/some/leaked/worktree"* ]]
}

@test "gfh_reset_env: exported arrays and exported-but-unset variables are scrubbed; unexported vars survive" {
  run bash -c "
    source '$GFH_LIB'
    export FIXTURE_T_EXPORTED_UNSET
    declare -ax FIXTURE_T_EXPORTED_ARRAY=(a b)
    FIXTURE_T_NOT_EXPORTED=keep
    gfh_reset_env
    [ -z \"\${FIXTURE_T_EXPORTED_ARRAY+x}\" ] && echo array_gone
    export -p | grep -q FIXTURE_T_EXPORTED_UNSET || echo unset_gone
    echo \"unexported=\${FIXTURE_T_NOT_EXPORTED:-lost}\"
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"array_gone"* ]]
  [[ "$output" == *"unset_gone"* ]]
  [[ "$output" == *"unexported=keep"* ]]
}

@test "gfh_reset_env: preserves the allowlisted process-plumbing vars" {
  run bash -c "
    source '$GFH_LIB'
    export TERM=xterm-fixture-test
    gfh_reset_env
    echo \"term=\${TERM:-gone}\"
    [ -n \"\${PATH:-}\" ] && echo path_survived
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"term=xterm-fixture-test"* ]]
  [[ "$output" == *"path_survived"* ]]
}

@test "gfh_reset_env: preserves this library's own GFH_* state" {
  source "$GFH_LIB"
  gfh_setup "reset-preserves-gfh"
  local root_before="$GFH_ROOT"
  local suite_before="$GFH_SUITE"

  gfh_reset_env

  [ "$GFH_ROOT" = "$root_before" ]
  [ "$GFH_SUITE" = "$suite_before" ]
}

# --- gfh_teardown ------------------------------------------------------------

@test "gfh_teardown removes GFH_ROOT entirely" {
  source "$GFH_LIB"
  gfh_setup "teardown-removes"
  local root="$GFH_ROOT"
  [ -d "$root" ]

  gfh_teardown

  [ ! -e "$root" ]
}

@test "gfh_teardown is a safe no-op when GFH_ROOT was never set" {
  run bash -c "source '$GFH_LIB'; gfh_teardown; echo survived"
  [ "$status" -eq 0 ]
  [[ "$output" == *"survived"* ]]
}

# --- gfh_init_bare (pg2-1msck) ----------------------------------------------

@test "gfh_init_bare: creates a bare repo on branch main inside the fixture root" {
  source "$GFH_LIB"
  gfh_setup "bare-basic"
  gfh_init_bare "$GFH_WORK/remote.git"

  run command git -C "$GFH_WORK/remote.git" rev-parse --is-bare-repository
  [ "$status" -eq 0 ]
  [ "$output" = "true" ]
  run command git -C "$GFH_WORK/remote.git" symbolic-ref --short HEAD
  [ "$output" = "main" ]
  [ "$(command git -C "$GFH_WORK/remote.git" config core.hooksPath)" = "/dev/null" ]
}

@test "gfh_init_bare: a push from GFH_REPO lands, and a planted receive hook never runs" {
  source "$GFH_LIB"
  gfh_setup "bare-push"
  gfh_init_bare "$GFH_WORK/remote.git"
  local sentinel="$GFH_ROOT/hook-ran"
  mkdir -p "$GFH_WORK/remote.git/hooks"
  cat >"$GFH_WORK/remote.git/hooks/pre-receive" <<HOOK
#!/bin/sh
touch "$sentinel"
exit 1
HOOK
  chmod +x "$GFH_WORK/remote.git/hooks/pre-receive"

  command git -C "$GFH_REPO" remote add origin "$GFH_WORK/remote.git"
  run command git -C "$GFH_REPO" push -q origin main
  [ "$status" -eq 0 ]
  [ ! -e "$sentinel" ]
  [ "$(command git -C "$GFH_WORK/remote.git" rev-parse main)" = "$(command git -C "$GFH_REPO" rev-parse main)" ]
}

# --- gfh_clone (pg2-1msck) --------------------------------------------------

@test "gfh_clone: clones a fixture repo with hooks disabled and the suite identity" {
  source "$GFH_LIB"
  gfh_setup "clone-basic"
  gfh_clone "$GFH_REPO" "$GFH_WORK/clone" "clone-basic"

  [ "$(cat "$GFH_WORK/clone/file.txt")" = "initial" ]
  [ "$(command git -C "$GFH_WORK/clone" rev-parse HEAD)" = "$(command git -C "$GFH_REPO" rev-parse HEAD)" ]
  [ "$(command git -C "$GFH_WORK/clone" config core.hooksPath)" = "/dev/null" ]
  [ "$(command git -C "$GFH_WORK/clone" config user.email)" = "clone-basic@bashfixture.invalid" ]
  [ "$(command git -C "$GFH_WORK/clone" config remote.origin.url)" = "$GFH_REPO" ]
}

@test "gfh_clone: round-trips through a bare remote (push from one clone, fetch in another)" {
  source "$GFH_LIB"
  gfh_setup "clone-roundtrip"
  gfh_init_bare "$GFH_WORK/remote.git"
  gfh_clone "$GFH_WORK/remote.git" "$GFH_WORK/one" "clone-roundtrip"
  gfh_clone "$GFH_WORK/remote.git" "$GFH_WORK/two" "clone-roundtrip"

  echo hi >"$GFH_WORK/one/new.txt"
  command git -C "$GFH_WORK/one" add new.txt
  command git -C "$GFH_WORK/one" commit -q -m "from one"
  command git -C "$GFH_WORK/one" push -q origin main
  command git -C "$GFH_WORK/two" pull -q origin main

  [ "$(cat "$GFH_WORK/two/new.txt")" = "hi" ]
}

@test "gfh_clone: an empty bare remote clones cleanly (unborn main)" {
  source "$GFH_LIB"
  gfh_setup "clone-empty"
  gfh_init_bare "$GFH_WORK/remote.git"

  run gfh_clone "$GFH_WORK/remote.git" "$GFH_WORK/clone" "clone-empty"
  [ "$status" -eq 0 ]
  run command git -C "$GFH_WORK/clone" symbolic-ref --short HEAD
  [ "$output" = "main" ]
}

@test "gfh_clone: a post-checkout hook in the source's template never runs during the clone" {
  source "$GFH_LIB"
  gfh_setup "clone-no-hook"
  local sentinel="$GFH_ROOT/hook-ran" tmpl="$GFH_ROOT/template"
  mkdir -p "$tmpl/hooks"
  cat >"$tmpl/hooks/post-checkout" <<HOOK
#!/bin/sh
touch "$sentinel"
HOOK
  chmod +x "$tmpl/hooks/post-checkout"
  # Control: the same template DOES fire for a plain clone, so the assertion
  # below is not vacuous.
  command git clone -q --template="$tmpl" "$GFH_REPO" "$GFH_WORK/control"
  [ -e "$sentinel" ]
  rm -f "$sentinel"

  GIT_TEMPLATE_DIR="$tmpl" gfh_clone "$GFH_REPO" "$GFH_WORK/clone" "clone-no-hook"
  [ ! -e "$sentinel" ]
}

# --- --no-identity mode (pg2-1msck) -----------------------------------------

@test "gfh_init_repo --no-identity: no local identity, and git cannot resolve one" {
  source "$GFH_LIB"
  gfh_setup "noid-repo"
  gfh_init_repo "$GFH_WORK/noid" "noid-repo" --no-identity

  run command git -C "$GFH_WORK/noid" config --local user.email
  [ "$status" -ne 0 ]
  run command git -C "$GFH_WORK/noid" config --local user.name
  [ "$status" -ne 0 ]
  [ "$(command git -C "$GFH_WORK/noid" config core.hooksPath)" = "/dev/null" ]
  run command git -C "$GFH_WORK/noid" var GIT_AUTHOR_IDENT
  [ "$status" -ne 0 ]
  echo x >"$GFH_WORK/noid/f"
  command git -C "$GFH_WORK/noid" add f
  run command git -C "$GFH_WORK/noid" commit -q -m "should fail"
  [ "$status" -ne 0 ]
}

@test "gfh_init_repo --no-identity: discriminating - the default form DOES resolve an identity" {
  source "$GFH_LIB"
  gfh_setup "noid-control"
  gfh_init_repo "$GFH_WORK/withid" "noid-control"

  run command git -C "$GFH_WORK/withid" var GIT_AUTHOR_IDENT
  [ "$status" -eq 0 ]
  [[ "$output" == *"noid-control@bashfixture.invalid"* ]]
}

@test "gfh_clone --no-identity: the clone has no resolvable identity" {
  source "$GFH_LIB"
  gfh_setup "noid-clone"
  gfh_clone "$GFH_REPO" "$GFH_WORK/clone" "noid-clone" --no-identity

  run command git -C "$GFH_WORK/clone" config --local user.email
  [ "$status" -ne 0 ]
  run command git -C "$GFH_WORK/clone" var GIT_AUTHOR_IDENT
  [ "$status" -ne 0 ]
}

@test "gfh_init_repo: an unknown option is rejected" {
  source "$GFH_LIB"
  gfh_setup "bad-option"
  run gfh_init_repo "$GFH_WORK/x" "bad-option" --bogus
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option"* ]]
}

# --- hostile environment: new primitives never touch a leaked repo ----------
#
# Same shape as the pg2-510ya test: a decoy repo is pointed at via GIT_DIR (and
# GIT_WORK_TREE), the new primitive runs with NO prior gfh_setup, and the
# decoy's .git/config must be byte-identical afterwards while the real target
# got created. Decoy setup and every read go through _gfh_clean_git so this
# test cannot itself be redirected by an ambient hook environment (pg2-6drqh).

_gfh_make_decoy() {
  DECOY="$(mktemp -d)"
  _gfh_clean_git init -q -b main "$DECOY"
  _gfh_clean_git -C "$DECOY" config user.email "real@example.com"
  DECOY_CONFIG_BEFORE="$(cat "$DECOY/.git/config")"
}

@test "hostile env: gfh_init_bare under a leaked GIT_DIR/GIT_WORK_TREE leaves the decoy untouched" {
  _gfh_make_decoy
  local target
  target="$(mktemp -d)"

  run env GIT_DIR="$DECOY/.git" GIT_WORK_TREE="$DECOY" bash -c "
    source '$GFH_LIB'
    gfh_init_bare '$target/remote.git'
  "
  [ "$status" -eq 0 ]

  [ "$(cat "$DECOY/.git/config")" = "$DECOY_CONFIG_BEFORE" ]
  [ "$(_gfh_clean_git -C "$target/remote.git" rev-parse --is-bare-repository)" = "true" ]
  [ "$(_gfh_clean_git -C "$target/remote.git" config core.hooksPath)" = "/dev/null" ]
  rm -rf "$DECOY" "$target"
}

@test "hostile env: gfh_clone under a leaked GIT_DIR/GIT_WORK_TREE leaves the decoy untouched" {
  _gfh_make_decoy
  # The clone's source comes from the harness itself: a hand-rolled commit
  # here would run under the real HOME's identity/hooks.
  source "$GFH_LIB"
  gfh_setup "hostile-clone-src"

  run env GIT_DIR="$DECOY/.git" GIT_WORK_TREE="$DECOY" bash -c "
    source '$GFH_LIB'
    gfh_clone '$GFH_REPO' '$GFH_WORK/clone' 'hostile-clone'
  "
  [ "$status" -eq 0 ]

  [ "$(cat "$DECOY/.git/config")" = "$DECOY_CONFIG_BEFORE" ]
  [ -f "$GFH_WORK/clone/file.txt" ]
  [ "$(_gfh_clean_git -C "$GFH_WORK/clone" config user.email)" = "hostile-clone@bashfixture.invalid" ]
  rm -rf "$DECOY"
}

@test "hostile env: gfh_init_repo --no-identity under a leaked GIT_DIR/GIT_WORK_TREE leaves the decoy untouched" {
  _gfh_make_decoy
  local target
  target="$(mktemp -d)"

  run env GIT_DIR="$DECOY/.git" GIT_WORK_TREE="$DECOY" bash -c "
    source '$GFH_LIB'
    gfh_init_repo '$target/noid' 'hostile-noid' --no-identity
  "
  [ "$status" -eq 0 ]

  [ "$(cat "$DECOY/.git/config")" = "$DECOY_CONFIG_BEFORE" ]
  [ -d "$target/noid/.git" ]
  [ "$(_gfh_clean_git -C "$target/noid" config user.useConfigOnly)" = "true" ]
  rm -rf "$DECOY" "$target"
}

# --- gfh_save_env / gfh_restore_env (pg2-1msck) ------------------------------

@test "gfh_save_env/gfh_restore_env: an exported var survives gfh_setup, and is exported to children" {
  run bash -c "
    source '$GFH_LIB'
    export SCRIPTS_DIR='/nix/store/xyz-scripts'
    export OTHER_VAR='with space'
    gfh_save_env SCRIPTS_DIR OTHER_VAR
    gfh_setup 'save-env'
    echo \"after_setup=\${SCRIPTS_DIR:-gone}\"
    gfh_restore_env
    echo \"restored=\${SCRIPTS_DIR:-gone}\"
    echo \"other=\${OTHER_VAR:-gone}\"
    echo \"child=\$(bash -c 'echo \${SCRIPTS_DIR:-gone}')\"
    gfh_teardown
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"after_setup=gone"* ]]
  [[ "$output" == *"restored=/nix/store/xyz-scripts"* ]]
  [[ "$output" == *"other=with space"* ]]
  [[ "$output" == *"child=/nix/store/xyz-scripts"* ]]
}

@test "gfh_save_env/gfh_restore_env: a var that was unset stays unset, and un-saved vars stay wiped" {
  run bash -c "
    source '$GFH_LIB'
    unset NEVER_SET_VAR
    export NOT_SAVED=leak
    gfh_save_env NEVER_SET_VAR
    gfh_setup 'save-env-unset'
    gfh_restore_env
    echo \"never=\${NEVER_SET_VAR-unset}\"
    echo \"not_saved=\${NOT_SAVED:-gone}\"
    gfh_teardown
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"never=unset"* ]]
  [[ "$output" == *"not_saved=gone"* ]]
}

@test "gfh_restore_env: is a safe no-op when gfh_save_env was never called (set -u)" {
  run bash -c "
    set -u
    source '$GFH_LIB'
    gfh_restore_env
    echo survived
  "
  [ "$status" -eq 0 ]
  [[ "$output" == *"survived"* ]]
}
