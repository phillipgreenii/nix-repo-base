#!/usr/bin/env bats
# bats file_tags=type:unit
# shellcheck disable=SC1090,SC2030,SC2031
#
# Tests for ul_refresh_action_pins (lib/scripts/update-action-pins-lib.bash, sourced by
# update-locks-lib.bash): the shared update-locks step that refreshes GitHub Action SHA pins
# (bead pg2-ehu9q). HERMETIC: nothing here contacts the network. The resolver is either an
# injected function (UL_ACTION_PINS_RESOLVER), a `gh` stub on PATH, or real `git ls-remote`
# against local repositories under the fixture root (UL_ACTION_PINS_GIT_URL=file://...).

if [[ -n ${UL_LIB_SCRIPTS_DIR:-} ]]; then
  UL_LOCKS_LIB="$UL_LIB_SCRIPTS_DIR/update-locks-lib.bash"
else
  UL_LOCKS_LIB="$(cd "$BATS_TEST_DIRNAME/../scripts" && pwd)/update-locks-lib.bash"
fi

SHA_A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
SHA_B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
SHA_C=cccccccccccccccccccccccccccccccccccccccc

# Same shebang fix as test-update-locks-lib.bats (no /usr/bin/env in the nix sandbox).
_fix_mock_shebang() {
  local f="$1" tmp
  tmp=$(mktemp)
  {
    printf '#!%s\n' "$(command -v bash)"
    tail -n +2 "$f"
  } >"$tmp"
  cat "$tmp" >"$f"
  rm -f "$tmp"
}

setup() {
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${UL_LIB_SCRIPTS_DIR:-$BATS_TEST_DIRNAME/../scripts}/git-fixture-harness.bash"
  gfh_save_env UL_LIB_SCRIPTS_DIR
  gfh_setup "test-update-action-pins-lib"
  gfh_restore_env

  TEST_DIR="$GFH_REPO"
  export XDG_STATE_HOME="$GFH_WORK/state"
  mkdir -p "$XDG_STATE_HOME"
  export NIX_UL_FORCE_UPDATE="true"

  # Mocks live OUTSIDE the repo so `git clean -fd` in a rolled-back step never eats them.
  MOCK_BIN="$GFH_WORK/mock-bin"
  mkdir -p "$MOCK_BIN"
  local cmd
  for cmd in nix pg-hooks; do
    cat >"$MOCK_BIN/$cmd" <<'MOCK'
#!/usr/bin/env bash
if [[ $(basename "$0") == pg-hooks && $1 == status ]]; then echo "state=present"; fi
exit 0
MOCK
    _fix_mock_shebang "$MOCK_BIN/$cmd"
    chmod +x "$MOCK_BIN/$cmd"
  done
  export PATH="$MOCK_BIN:$PATH"

  WF_DIR="$TEST_DIR/.github/workflows"
  mkdir -p "$WF_DIR"
  cd "$TEST_DIR" || return 1
  source "$UL_LOCKS_LIB"
  # Default injected resolver: a fixed table. Individual tests override the table.
  UL_AP_TABLE="$GFH_WORK/resolver-table"
  : >"$UL_AP_TABLE"
  export UL_AP_TABLE
  export UL_ACTION_PINS_RESOLVER=fake_resolver
}

teardown() {
  cd /
  gfh_teardown
}

# fake_resolver <owner/repo> <ref>: looks "<owner/repo> <ref> <sha>" up in $UL_AP_TABLE. A line
# with the sha FAIL simulates a resolution failure. Unlisted pairs fail as "not found".
fake_resolver() {
  local r ref sha
  while read -r r ref sha; do
    if [[ $r == "$1" && $ref == "$2" ]]; then
      if [[ $sha == FAIL ]]; then
        echo "fake resolver: simulated failure for $1@$2" >&2
        return 1
      fi
      echo "$sha"
      return 0
    fi
  done <"$UL_AP_TABLE"
  echo "fake resolver: no such ref $1@$2" >&2
  return 1
}

# `! cmd` never fails a bats test (SC2314), so negative file assertions go through this.
refute_in_file() {
  if grep -qF -- "$1" "$2"; then
    echo "unexpected '$1' found in $2" >&2
    return 1
  fi
}

table() { printf '%s\n' "$@" >>"$UL_AP_TABLE"; }

# --- bump / keep comment ---

@test "bumps the SHA when the ref moved and keeps the trailing comment" {
  cat >"$WF_DIR/ci.yml" <<EOF
jobs:
  a:
    steps:
      - name: checkout
        uses: actions/checkout@$SHA_A # v6.0.2
      - run: echo hi
EOF
  table "actions/checkout v6.0.2 $SHA_B"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "        uses: actions/checkout@$SHA_B # v6.0.2" "$WF_DIR/ci.yml"
  refute_in_file "$SHA_A" "$WF_DIR/ci.yml"
  # untouched neighbours
  grep -qF "      - run: echo hi" "$WF_DIR/ci.yml"
  [[ $output == *"actions/checkout"* ]]
}

@test "leaves a file byte-identical when every pin is already current" {
  printf '      - uses: actions/checkout@%s # v6.0.2\n' "$SHA_A" >"$WF_DIR/ci.yml"
  local before
  before=$(cat "$WF_DIR/ci.yml")
  table "actions/checkout v6.0.2 $SHA_A"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  [ "$(cat "$WF_DIR/ci.yml")" = "$before" ]
}

@test "resolves a branch ref and a reusable-workflow path (owner/repo/path@sha # main)" {
  cat >"$WF_DIR/update-flakes.yml" <<EOF
jobs:
  update:
    uses: phillipgreenii/nix-repo-base/.github/workflows/update-flakes-reusable.yml@$SHA_A # main
    secrets: inherit
EOF
  table "phillipgreenii/nix-repo-base main $SHA_B"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "update-flakes-reusable.yml@$SHA_B # main" "$WF_DIR/update-flakes.yml"
}

@test "keeps quoting and any text after the ref in the comment" {
  cat >"$WF_DIR/q.yml" <<EOF
      - uses: "actions/setup-go@$SHA_A" # v5 (pinned by audit)
EOF
  table "actions/setup-go v5 $SHA_B"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF -- "- uses: \"actions/setup-go@$SHA_B\" # v5 (pinned by audit)" "$WF_DIR/q.yml"
}

@test "scans every workflow file, both .yml and .yaml" {
  printf '  - uses: a/b@%s # v1\n' "$SHA_A" >"$WF_DIR/one.yml"
  printf '  - uses: c/d@%s # v2\n' "$SHA_A" >"$WF_DIR/two.yaml"
  table "a/b v1 $SHA_B" "c/d v2 $SHA_C"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "a/b@$SHA_B # v1" "$WF_DIR/one.yml"
  grep -qF "c/d@$SHA_C # v2" "$WF_DIR/two.yaml"
}

@test "resolves each distinct repo@ref once" {
  printf '  - uses: a/b@%s # v1\n  - uses: a/b@%s # v1\n' "$SHA_A" "$SHA_A" >"$WF_DIR/one.yml"
  table "a/b v1 $SHA_B"
  export UL_AP_CALLS="$GFH_WORK/calls"
  : >"$UL_AP_CALLS"
  counting_resolver() {
    echo "$1@$2" >>"$UL_AP_CALLS"
    fake_resolver "$@"
  }
  export UL_ACTION_PINS_RESOLVER=counting_resolver
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$UL_AP_CALLS" | tr -d ' ')" -eq 1 ]
}

@test "ignores local actions, docker refs and commented-out uses lines" {
  cat >"$WF_DIR/skip.yml" <<EOF
      - uses: ./.github/actions/local
      - uses: docker://alpine:3.20
      # - uses: a/b@$SHA_A # v1
      - run: echo "uses: a/b@$SHA_A # v1"
EOF
  local before
  before=$(cat "$WF_DIR/skip.yml")
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  [ "$(cat "$WF_DIR/skip.yml")" = "$before" ]
}

@test "a missing .github/workflows directory is a no-op success" {
  rm -rf "$TEST_DIR/.github"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
}

@test "--workflows-dir scans an explicit directory" {
  mkdir -p "$GFH_WORK/elsewhere"
  printf '  - uses: a/b@%s # v1\n' "$SHA_A" >"$GFH_WORK/elsewhere/x.yml"
  table "a/b v1 $SHA_B"
  run ul_refresh_action_pins --workflows-dir "$GFH_WORK/elsewhere"
  [ "$status" -eq 0 ]
  grep -qF "a/b@$SHA_B # v1" "$GFH_WORK/elsewhere/x.yml"
}

# --- mutable refs ---

@test "a mutable ref (no SHA) is reported and NOT rewritten" {
  cat >"$WF_DIR/m.yml" <<EOF
      - uses: actions/checkout@v3
      - uses: phillipgreenii/nix-repo-base/.github/workflows/update-flakes-reusable.yml@main
EOF
  local before
  before=$(cat "$WF_DIR/m.yml")
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  [ "$(cat "$WF_DIR/m.yml")" = "$before" ]
  [[ $output == *"mutable"* ]]
  [[ $output == *"m.yml:1"* ]]
  [[ $output == *"actions/checkout@v3"* ]]
  [[ $output == *"m.yml:2"* ]]
  [[ $output == *"--pin-mutable"* ]]
}

@test "--pin-mutable pins a mutable ref to its SHA and records the ref in the comment" {
  cat >"$WF_DIR/m.yml" <<EOF
      - uses: actions/checkout@v3
      - uses: 'a/b@main'
EOF
  table "actions/checkout v3 $SHA_B" "a/b main $SHA_C"
  run ul_refresh_action_pins --pin-mutable
  [ "$status" -eq 0 ]
  grep -qF "      - uses: actions/checkout@$SHA_B # v3" "$WF_DIR/m.yml"
  grep -qF "      - uses: 'a/b@$SHA_C' # main" "$WF_DIR/m.yml"
}

@test "--pin-mutable failing to resolve is a failure and leaves the file alone" {
  printf '      - uses: a/b@v3\n' >"$WF_DIR/m.yml"
  table "a/b v3 FAIL"
  run ul_refresh_action_pins --pin-mutable
  [ "$status" -eq 3 ]
  [ "$(cat "$WF_DIR/m.yml")" = "      - uses: a/b@v3" ]
}

@test "a SHA pin with no ref comment is reported as unrefreshable, not failed" {
  printf '      - uses: a/b@%s\n' "$SHA_A" >"$WF_DIR/n.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  [[ $output == *"no ref comment"* ]]
  [[ $output == *"n.yml:1"* ]]
  [ "$(cat "$WF_DIR/n.yml")" = "      - uses: a/b@$SHA_A" ]
}

# --- failures are reported, not swallowed ---

@test "a resolution failure exits 3 with a clear message and writes NOTHING (all-or-nothing)" {
  printf '  - uses: a/b@%s # v1\n  - uses: c/d@%s # gone\n' "$SHA_A" "$SHA_A" >"$WF_DIR/f.yml"
  local before
  before=$(cat "$WF_DIR/f.yml")
  table "a/b v1 $SHA_B" "c/d gone FAIL"
  run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve"* ]]
  [[ $output == *"c/d@gone"* ]]
  [[ $output == *"f.yml:2"* ]]
  [[ $output == *"simulated failure"* ]]
  # the resolvable pin was NOT applied: a failed run leaves no partial rewrite
  [ "$(cat "$WF_DIR/f.yml")" = "$before" ]
}

@test "a resolver that prints something other than a 40-hex SHA is a failure" {
  printf '  - uses: a/b@%s # v1\n' "$SHA_A" >"$WF_DIR/f.yml"
  bad_resolver() { echo "not-a-sha"; }
  export UL_ACTION_PINS_RESOLVER=bad_resolver
  run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve"* ]]
}

@test "an unknown option is a usage error (exit 2)" {
  run ul_refresh_action_pins --bogus
  [ "$status" -eq 2 ]
  [[ $output == *"unknown option"* ]]
}

# --- real resolver: gh backend (stubbed on PATH) ---

_install_gh_stub() {
  # $1 = body of the stub
  printf '#!/usr/bin/env bash\n%s\n' "$1" >"$MOCK_BIN/gh"
  _fix_mock_shebang "$MOCK_BIN/gh"
  chmod +x "$MOCK_BIN/gh"
}

@test "gh backend: asks the commits API for the ref (GitHub peels annotated tags) and uses the .sha" {
  _install_gh_stub "echo \"\$*\" >>\"$GFH_WORK/gh-args\"; echo $SHA_B"
  unset UL_ACTION_PINS_RESOLVER
  export UL_ACTION_PINS_BACKEND=gh
  printf '  - uses: a/b@%s # v1.2\n' "$SHA_A" >"$WF_DIR/g.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "a/b@$SHA_B # v1.2" "$WF_DIR/g.yml"
  grep -qF "api repos/a/b/commits/v1.2 --jq .sha" "$GFH_WORK/gh-args"
}

@test "gh backend: a gh failure is a reported failure carrying gh's own error" {
  _install_gh_stub 'echo "gh: Not Found (HTTP 404)" >&2; exit 1'
  unset UL_ACTION_PINS_RESOLVER
  export UL_ACTION_PINS_BACKEND=gh
  printf '  - uses: a/b@%s # v1.2\n' "$SHA_A" >"$WF_DIR/g.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve a/b@v1.2"* ]]
  [[ $output == *"Not Found"* ]]
  grep -qF "a/b@$SHA_A" "$WF_DIR/g.yml"
}

@test "gh backend: gh not installed is a reported failure" {
  unset UL_ACTION_PINS_RESOLVER
  export UL_ACTION_PINS_BACKEND=gh
  # A PATH holding only the tools the step needs, so no real gh on the developer's machine
  # can leak in.
  local bare="$GFH_WORK/bare-bin" t
  mkdir -p "$bare"
  for t in bash cat mktemp rm tr; do
    ln -s "$(command -v "$t")" "$bare/$t"
  done
  printf '  - uses: a/b@%s # v1.2\n' "$SHA_A" >"$WF_DIR/g.yml"
  PATH="$bare" run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve a/b@v1.2"* ]]
  [[ $output == *"gh is not installed"* ]]
}

# --- real resolver: git ls-remote backend (local repos, no network) ---

# _make_remote <owner/repo>: a repo at $GFH_WORK/remotes/<owner/repo>.git with one commit on
# main. Sets REMOTE_SHA to its commit.
_make_remote() {
  local r="$GFH_WORK/remotes/$1.git"
  mkdir -p "$(dirname "$r")"
  gfh_init_repo "$r" "test-update-action-pins-lib"
  git -C "$r" commit -q --allow-empty -m "c1"
  git -C "$r" branch -M main
  REMOTE_SHA=$(git -C "$r" rev-parse HEAD)
  export UL_ACTION_PINS_BACKEND=git
  export UL_ACTION_PINS_GIT_URL="file://$GFH_WORK/remotes"
  unset UL_ACTION_PINS_RESOLVER
}

@test "git backend: an annotated tag is PEELED to the commit, not the tag object" {
  _make_remote o/r
  local r="$GFH_WORK/remotes/o/r.git"
  git -C "$r" tag -a v1 -m "annotated v1"
  local tag_obj
  tag_obj=$(git -C "$r" rev-parse refs/tags/v1)
  [ "$tag_obj" != "$REMOTE_SHA" ] # sanity: annotated tag really has its own object
  printf '  - uses: o/r@%s # v1\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "o/r@$REMOTE_SHA # v1" "$WF_DIR/t.yml"
  refute_in_file "$tag_obj" "$WF_DIR/t.yml"
}

@test "git backend: a lightweight tag resolves to its commit" {
  _make_remote o/r
  git -C "$GFH_WORK/remotes/o/r.git" tag v2
  printf '  - uses: o/r@%s # v2\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "o/r@$REMOTE_SHA # v2" "$WF_DIR/t.yml"
}

@test "git backend: a branch resolves to its tip" {
  _make_remote o/r
  printf '  - uses: o/r@%s # main\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "o/r@$REMOTE_SHA # main" "$WF_DIR/t.yml"
}

@test "git backend: a tag wins over a same-named branch" {
  _make_remote o/r
  local r="$GFH_WORK/remotes/o/r.git"
  git -C "$r" tag v3
  git -C "$r" commit -q --allow-empty -m "c2"
  git -C "$r" branch v3 HEAD
  local tip
  tip=$(git -C "$r" rev-parse HEAD)
  [ "$tip" != "$REMOTE_SHA" ]
  printf '  - uses: o/r@%s # v3\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 0 ]
  grep -qF "o/r@$REMOTE_SHA # v3" "$WF_DIR/t.yml"
}

@test "git backend: an unknown ref is a reported failure" {
  _make_remote o/r
  printf '  - uses: o/r@%s # v99\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve o/r@v99"* ]]
  grep -qF "o/r@$SHA_A" "$WF_DIR/t.yml"
}

@test "git backend: an unreachable repo is a reported failure" {
  _make_remote o/r
  printf '  - uses: nobody/nothing@%s # v1\n' "$SHA_A" >"$WF_DIR/t.yml"
  run ul_refresh_action_pins
  [ "$status" -eq 3 ]
  [[ $output == *"cannot resolve nobody/nothing@v1"* ]]
}

# --- wiring through ul_run_step / ul_finalize (consumer usage) ---

_commit_workflows() {
  git add -A
  git commit -q -m "add workflows"
}

@test "runs as an update-locks step: one commit with the bumped pin, finalize exits 0" {
  printf '      - uses: a/b@%s # v1\n' "$SHA_A" >"$WF_DIR/ci.yml"
  _commit_workflows
  table "a/b v1 $SHA_B"
  ul_setup "test-project" "$TEST_DIR"
  ul_run_step "github-action-pins" "update-locks: refresh GitHub Action SHA pins" ul_refresh_action_pins
  [ "$(git log -1 --format=%s)" = "update-locks: refresh GitHub Action SHA pins" ]
  grep -qF "a/b@$SHA_B # v1" "$WF_DIR/ci.yml"
  [ "$_UL_STEPS_FAILED" -eq 0 ]
  run bash -c "source '$UL_LOCKS_LIB'; ul_setup test-project '$TEST_DIR'; ul_finalize"
  [ "$status" -eq 0 ]
}

@test "a resolution failure fails the step: rolled back, recorded, finalize exits 1" {
  printf '      - uses: a/b@%s # gone\n' "$SHA_A" >"$WF_DIR/ci.yml"
  _commit_workflows
  table "a/b gone FAIL"
  ul_setup "test-project" "$TEST_DIR"
  ul_run_step "github-action-pins" "update-locks: refresh GitHub Action SHA pins" ul_refresh_action_pins
  [ "$_UL_STEPS_FAILED" -eq 1 ]
  [ "${_UL_FAILED_STEPS[0]}" = "github-action-pins" ]
  grep -qF "a/b@$SHA_A # gone" "$WF_DIR/ci.yml"
  git diff --quiet
}

@test "the --pin-mutable flag passes through ul_run_step" {
  printf '      - uses: a/b@v1\n' >"$WF_DIR/ci.yml"
  _commit_workflows
  table "a/b v1 $SHA_B"
  ul_setup "test-project" "$TEST_DIR"
  ul_run_step "github-action-pins" "update-locks: pin GitHub Actions" ul_refresh_action_pins --pin-mutable
  grep -qF "a/b@$SHA_B # v1" "$WF_DIR/ci.yml"
}
