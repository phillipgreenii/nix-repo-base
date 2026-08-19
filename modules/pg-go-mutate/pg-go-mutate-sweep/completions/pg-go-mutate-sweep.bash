_pg_go_mutate_sweep() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --root)
    mapfile -t COMPREPLY < <(compgen -d -- "$cur")
    return 0
    ;;
  # Free-form values with nothing enumerable to offer: a project key, a
  # comma-separated tag list, a <project>#<package> unit key, and four integers.
  --only | --auto-tags | --redo) return 0 ;;
  --unit-timeout | --unit-kill-grace | --mutant-timeout | --workers) return 0 ;;
  --retry)
    # 'transient' (the cohort shorthand) plus every status pgms_classify can
    # RECORD. 'fatal' is deliberately absent: it aborts the sweep with exit 4
    # and is never written to the ledger, so it could never match a retry spec.
    mapfile -t COMPREPLY < <(compgen -W "transient done no-tests failed timeout unhealthy not-enumerable vanished inconclusive" -- "$cur")
    return 0
    ;;
  esac

  if [[ $cur == -* ]]; then
    # -h and -v are offered alongside their long forms because the command
    # accepts both: -h/--help in the script, -v/--version injected and
    # smoke-tested by the builder. Flags, completions and the tldr page are a
    # hard coupling (spec T2), and the zsh completion offers the same set.
    mapfile -t COMPREPLY < <(compgen -W "--root --only --unit-timeout --unit-kill-grace --mutant-timeout --workers --auto-tags --retry --redo --dry-run --no-beads --force-unlock --help -h --version -v" -- "$cur")
    return 0
  fi
  # No positional fallback, unlike the sibling: pg-go-mutate-sweep takes NO
  # positional arguments -- its arg loop rejects any bare word with exit 2.
}
complete -F _pg_go_mutate_sweep pg-go-mutate-sweep
