_pg_go_mutate() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --workers | --timeout) return 0 ;;
  --tags) return 0 ;;
  esac

  if [[ $cur == -* ]]; then
    # -h and -v are offered alongside their long forms because the command
    # accepts both: -h/--help in the script, -v/--version reserved and
    # smoke-tested by the builder. Flags, completions and the tldr page are a
    # hard coupling (spec T2), and the zsh completion offers the same four.
    mapfile -t COMPREPLY < <(compgen -W "--tags --json --timeout --workers --help -h --version -v" -- "$cur")
    return 0
  fi
  mapfile -t COMPREPLY < <(compgen -d -- "$cur")
}
complete -F _pg_go_mutate pg-go-mutate
