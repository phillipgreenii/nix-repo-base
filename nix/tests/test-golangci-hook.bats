#!/usr/bin/env bats
# The opt-in golangci hook (nix/golangci-hook.nix) against the REAL golangci-lint
# and go in a throwaway git repo. Env (set by the flake check): HOOK.

setup() {
  [[ -n "${HOOK-}" ]] || skip "HOOK comes from the flake check golangci-hook"
  work="$BATS_TEST_TMPDIR"
  export HOME="$work/home" GOCACHE="$work/gocache" GOLANGCI_LINT_CACHE="$work/lintcache"
  export GOFLAGS=-mod=mod GOPATH="$work/gopath" GOPROXY=off
  mkdir -p "$HOME"
  unset NIX_BUILD_TOP IN_NIX_BUILD PG_GOLANGCI_BASE_REV
  repo="$work/repo"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" config user.email t@example.com
  git -C "$repo" config user.name t
  for m in moda modb; do
    mkdir -p "$repo/$m"
    printf 'module example.com/%s\n\ngo 1.21\n' "$m" >"$repo/$m/go.mod"
    printf 'package %s\n\nfunc F() int { return 1 }\n' "$m" >"$repo/$m/x.go"
  done
  printf 'version: "2"\nlinters:\n  default: none\n  enable:\n    - ineffassign\n' >"$repo/.golangci.yml"
  # modb carries a PRE-EXISTING finding in the base commit.
  printf 'package modb\n\nfunc G() int {\n\tx := 1\n\tx = 2\n\treturn 3\n}\n' >"$repo/modb/old.go"
  git -C "$repo" add -A
  git -C "$repo" commit -q -m base
  git -C "$repo" checkout -q -b feature
}

run_hook() {
  cd "$repo" || return 1
  run "$HOOK" "$@"
}

@test "fails on a planted new violation in a touched module" {
  printf 'package moda\n\nfunc H() int {\n\ty := 1\n\ty = 2\n\treturn 3\n}\n' >"$repo/moda/new.go"
  git -C "$repo" add -A
  run_hook moda/new.go
  [ "$status" -ne 0 ]
  [[ "$output" == *"ineffassign"* ]]
}

@test "passes when the only finding is pre-existing" {
  printf 'package modb\n\nfunc K() int { return 4 }\n' >"$repo/modb/ok.go"
  git -C "$repo" add -A
  git -C "$repo" commit -q -m ok
  run_hook modb/ok.go
  [ "$status" -eq 0 ]
}

@test "ignores an untouched module with a violation" {
  printf 'package moda\n\nfunc H() int {\n\ty := 1\n\ty = 2\n\treturn 3\n}\n' >"$repo/moda/new.go"
  git -C "$repo" add -A
  run_hook modb/x.go
  [ "$status" -eq 0 ]
  [[ "$output" != *"moda"* ]]
}

@test "exits 0 with a notice inside the nix sandbox" {
  printf 'package moda\n\nfunc H() int {\n\ty := 1\n\ty = 2\n\treturn 3\n}\n' >"$repo/moda/new.go"
  git -C "$repo" add -A
  cd "$repo"
  NIX_BUILD_TOP=/build run "$HOOK" moda/new.go
  [ "$status" -eq 0 ]
  [[ "$output" == *"inside the nix build sandbox"* ]]
}

@test "invokes no nix: reset PATH holds no nix and a caller nix is never run" {
  cd "$repo"
  run env PG_GOLANGCI_SHOW_PATH=1 "$HOOK"
  [ "$status" -eq 0 ]
  hook_path="$output"
  IFS=: read -ra dirs <<<"$hook_path"
  for d in "${dirs[@]}"; do
    [ ! -e "$d/nix" ] || { echo "nix found in $d"; return 1; }
  done
  mkdir -p "$work/fakebin"
  printf '#!/bin/sh\ntouch "%s/nix-ran"\nexit 99\n' "$work" >"$work/fakebin/nix"
  chmod +x "$work/fakebin/nix"
  printf 'package moda\n\nfunc J() int { return 5 }\n' >"$repo/moda/j.go"
  git -C "$repo" add -A
  PATH="$work/fakebin:$PATH" run "$HOOK" moda/j.go
  [ "$status" -eq 0 ]
  [ ! -e "$work/nix-ran" ]
}
