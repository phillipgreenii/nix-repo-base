_pg_go_mutate() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --workers | --timeout) return 0 ;;
  --tags) return 0 ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--tags --json --timeout --workers --help" -- "$cur")
    return 0
  fi
  mapfile -t COMPREPLY < <(compgen -d -- "$cur")
}
complete -F _pg_go_mutate pg-go-mutate
