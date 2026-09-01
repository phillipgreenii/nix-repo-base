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
