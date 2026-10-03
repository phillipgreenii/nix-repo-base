#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

# Every test builds its own tree under a fresh temp dir and plants the violation
# it names; nothing outside that dir is read or written.

setup() {
  TEST_DIR="$(mktemp -d)"
  TREE="$TEST_DIR/tree"
  ALLOW="$TEST_DIR/allowlist.tsv"
  mkdir -p "$TREE/src" "$TREE/docs"
  # A realistic clean tree: ordinary source, prose that merely mentions hooks,
  # an absolute core.hooksPath and a bare mention of the config file name.
  printf '#!/bin/sh\necho hello\n' >"$TREE/src/run.sh"
  {
    printf 'core.hooksPath is never written by tooling.\n'
    printf 'Set core.hooksPath to the absolute /repo/.git/hooks.\n'
    printf 'git config core.hooksPath /repo/.git/hooks\n'
    printf 'The old .pre-commit-config.yaml is gitignored.\n'
  } >"$TREE/docs/notes.md"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SUT="$SCRIPT_UNDER_TEST"
  else
    # Raw source: the script has no library, so the builder's preamble is all it
    # is missing.
    SUT="$TEST_DIR/pg-hooks-drift-guard"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "${BATS_TEST_DIRNAME}/../pg-hooks-drift-guard.sh"
    } >"$SUT"
    chmod +x "$SUT"
  fi
}

teardown() {
  rm -rf "$TEST_DIR"
}

# allow <path-glob> <categories> <reason>: append one allowlist entry.
allow() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$ALLOW"
}

@test "a clean tree passes with no output" {
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$stderr" ]
}

@test "a clean tree passes with an allowlist that matches nothing, noting the stale entry" {
  allow 'nowhere/*' '*' 'a stale entry'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
  [[ $stderr == *"allowlist entry matched nothing: nowhere/*"* ]]
}

@test "a .githooks directory fails" {
  mkdir -p "$TREE/.githooks"
  printf '#!/bin/sh\n' >"$TREE/.githooks/pre-commit"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 10 ]
  [[ $stderr == *"githooks-dir: .githooks"* ]]
}

@test "a nested .githooks directory fails" {
  mkdir -p "$TREE/sub/.githooks"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 10 ]
  [[ $stderr == *"githooks-dir: sub/.githooks"* ]]
}

@test "text naming .githooks fails as githooks-ref with file and line" {
  printf 'line one\nrun .githooks/pre-commit here\n' >"$TREE/src/readme.txt"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 10 ]
  [[ $stderr == *"githooks-ref: src/readme.txt:2:"* ]]
}

@test "a relative core.hooksPath fails in each spelling" {
  local spelling
  for spelling in \
    'git config core.hooksPath .githooks' \
    'core.hooksPath=.githooks' \
    'core.hooksPath = "./hooks"' \
    'core.hooksPath ../shared/hooks' \
    "core.hooksPath='scripts/hooks'"; do
    printf '%s\n' "$spelling" >"$TREE/src/cfg.txt"
    run --separate-stderr "$SUT" --root "$TREE"
    [ "$status" -eq 10 ] || {
      echo "spelling not caught: $spelling" >&2
      return 1
    }
    [[ $stderr == *"relative-hooks-path: src/cfg.txt:1:"* ]]
  done
}

@test "an absolute or variable core.hooksPath and plain prose do not fail" {
  {
    printf 'core.hooksPath=/abs/.git/hooks\n'
    printf 'core.hooksPath = "$HOME/hooks"\n'
    printf 'core.hooksPath ~/hooks\n'
    printf 'It must not write core.hooksPath. Next sentence.\n'
    printf 'core.hooksPath is a git setting\n'
  } >"$TREE/src/cfg.txt"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 0 ]
}

@test "link code for the generated config fails as config-link" {
  local code
  for code in \
    'ln -s "$canonical/.pre-commit-config.yaml" "$wt/.pre-commit-config.yaml"' \
    'ln -sf target .pre-commit-config.yaml' \
    'os.Symlink(target, filepath.Join(dir, ".pre-commit-config.yaml"))' \
    'target=$(readlink .pre-commit-config.yaml)' \
    'Link the .pre-commit-config.yaml into each worktree'; do
    printf '%s\n' "$code" >"$TREE/src/link.txt"
    run --separate-stderr "$SUT" --root "$TREE"
    [ "$status" -eq 10 ] || {
      echo "link not caught: $code" >&2
      return 1
    }
    [[ $stderr == *"config-link: src/link.txt:1:"* ]]
  done
}

@test "deleted machinery identifiers fail as removed-machinery" {
  local id
  for id in commitTimeShim shimLinkConfig linkPreCommitConfig PREK_ALLOW_NO_CONFIG pre-commit-githooks-wired; do
    printf 'use %s here\n' "$id" >"$TREE/src/old.txt"
    run --separate-stderr "$SUT" --root "$TREE"
    [ "$status" -eq 10 ] || {
      echo "identifier not caught: $id" >&2
      return 1
    }
    [[ $stderr == *"removed-machinery: src/old.txt:1:"* ]]
  done
}

@test "each finding is reported and the count summarizes them" {
  mkdir -p "$TREE/.githooks"
  printf 'core.hooksPath=.githooks\n' >"$TREE/src/cfg.txt"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 10 ]
  [[ $stderr == *"githooks-dir: .githooks"* ]]
  [[ $stderr == *"githooks-ref: src/cfg.txt:1:"* ]]
  [[ $stderr == *"relative-hooks-path: src/cfg.txt:1:"* ]]
  [[ $stderr == *"3 finding(s) outside the allowlist"* ]]
}

@test "an allowlist entry for the path and category lets the violation pass" {
  printf 'ln -s a .pre-commit-config.yaml\n' >"$TREE/src/link.txt"
  allow 'src/link.txt' 'config-link' 'test: the line is a fixture'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
  [[ $stderr != *"config-link"* ]]
}

@test "an allowlist entry does not cover another category in the same file" {
  printf 'ln -s a .pre-commit-config.yaml\nuse commitTimeShim\n' >"$TREE/src/link.txt"
  allow 'src/link.txt' 'config-link' 'test: only the link is allowed'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 10 ]
  [[ $stderr == *"removed-machinery: src/link.txt:2:"* ]]
  [[ $stderr != *"config-link: src/link.txt"* ]]
}

@test "an allowlist entry does not cover another file" {
  printf 'ln -s a .pre-commit-config.yaml\n' >"$TREE/src/link.txt"
  printf 'ln -s a .pre-commit-config.yaml\n' >"$TREE/src/other.txt"
  allow 'src/link.txt' 'config-link' 'test: one file only'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 10 ]
  [[ $stderr == *"config-link: src/other.txt:1:"* ]]
  [[ $stderr != *"config-link: src/link.txt"* ]]
}

@test "a glob entry covers a whole directory and a category list covers several categories" {
  mkdir -p "$TREE/frozen/deep"
  printf 'use commitTimeShim in .githooks\n' >"$TREE/frozen/deep/old.md"
  allow 'frozen/*' 'removed-machinery,githooks-ref' 'test: a frozen snapshot'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
}

@test "the star category allows every category" {
  mkdir -p "$TREE/frozen"
  printf 'use commitTimeShim in .githooks with ln -s x .pre-commit-config.yaml\n' >"$TREE/frozen/old.md"
  allow 'frozen/*' '*' 'test: everything in the snapshot'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
}

@test "a .githooks directory can be allowlisted by its path" {
  mkdir -p "$TREE/.githooks"
  allow '.githooks' 'githooks-dir' 'test: fixture directory'
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
}

@test "the allowlist file inside the tree is never scanned" {
  ALLOW="$TREE/drift-allowlist.tsv"
  printf 'x/*\t*\tmentions commitTimeShim and .githooks on purpose\n' >"$ALLOW"
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
  [[ $stderr != *"drift-allowlist.tsv:"* ]]
}

@test "comments and blank lines in the allowlist are ignored" {
  printf 'ln -s a .pre-commit-config.yaml\n' >"$TREE/src/link.txt"
  {
    printf '# a comment\n\n'
    printf 'src/link.txt\tconfig-link\ttest: reason\n'
  } >"$ALLOW"
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 0 ]
}

@test "an allowlist line without a reason is refused with exit 2" {
  printf 'src/link.txt\tconfig-link\n' >"$ALLOW"
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 2 ]
  [[ $stderr == *"all three mandatory"* ]]
}

@test "an allowlist line with an empty reason is refused with exit 2" {
  printf 'src/link.txt\tconfig-link\t  \n' >"$ALLOW"
  run --separate-stderr "$SUT" --root "$TREE" --allowlist "$ALLOW"
  [ "$status" -eq 2 ]
}

@test ".git, .worktrees and .workforests directories are not scanned" {
  mkdir -p "$TREE/.git" "$TREE/.worktrees/wt" "$TREE/.workforests/set"
  printf 'core.hooksPath=.githooks\n' >"$TREE/.git/config"
  printf 'use commitTimeShim\n' >"$TREE/.worktrees/wt/x.txt"
  printf 'use commitTimeShim\n' >"$TREE/.workforests/set/x.txt"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 0 ]
}

@test "binary files are not scanned" {
  printf 'use commitTimeShim\0binary\n' >"$TREE/src/blob.bin"
  run --separate-stderr "$SUT" --root "$TREE"
  [ "$status" -eq 0 ]
}

@test "a missing --root exits 2" {
  run --separate-stderr "$SUT"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--root is required"* ]]
}

@test "a --root that is not a directory exits 2" {
  run --separate-stderr "$SUT" --root "$TEST_DIR/nope"
  [ "$status" -eq 2 ]
}

@test "an unknown argument exits 2" {
  run --separate-stderr "$SUT" --root "$TREE" --bogus
  [ "$status" -eq 2 ]
}

@test "--help is answered with exit 0 and the usage block" {
  run "$SUT" --help
  [ "$status" -eq 0 ]
  [[ $output == "pg-hooks-drift-guard: "* ]]
}

@test "a relative --root works" {
  cd "$TEST_DIR"
  run --separate-stderr "$SUT" --root tree
  [ "$status" -eq 0 ]
  mkdir -p tree/.githooks
  run --separate-stderr "$SUT" --root tree
  [ "$status" -eq 10 ]
}
