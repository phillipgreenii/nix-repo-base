# shellcheck shell=bash

show_help() {
  cat <<'HELP'
pg-git-check-identity: reject a commit whose author or committer identity looks like a test/placeholder account

Usage: pg-git-check-identity [OPTIONS]

Checks the identity git would record for the next commit -- via `git var
GIT_AUTHOR_IDENT` / `GIT_COMMITTER_IDENT`, which resolve identity the same
way git itself does (environment variables, then config, then fallback) --
against a blocklist of placeholder email domains (example.com/.org/.net) and
placeholder names (test, t, tester, ...). Exits non-zero if either identity
matches.

Meant to run as a git hook -- either the global pre-commit hook wired via
programs.git.hooks.pre-commit in phillipgreenii-nix-personal's git module
(covers any repo on the machine), or the check-git-identity entry in this
repo's shared flakeModules.pre-commit base hook set (covers every repo that
imports it) -- but safe to run manually inside any repo to sanity-check the
current identity.

Options:
  -h, --help     Show this help message

Exit codes:
  0  both identities look fine
  2  usage error (unknown option)
  3  author or committer identity looks like a placeholder account
HELP
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    break
    ;;
  *)
    echo "pg-git-check-identity: unknown argument: $1" >&2
    exit 2
    ;;
  esac
  shift
done

# Assigned as their own statements (not inline as a function argument) so
# that `set -e` actually catches a `git var` failure -- e.g. identity fully
# unresolved (no env, no config, no fallback) -- rather than silently
# feeding pgci_check_identity an empty string, which would pass neither
# blocklist and wrongly report success.
author_ident="$(git var GIT_AUTHOR_IDENT)"
committer_ident="$(git var GIT_COMMITTER_IDENT)"

status=0
pgci_check_identity author "$author_ident" || status=3
pgci_check_identity committer "$committer_ident" || status=3
exit "$status"
