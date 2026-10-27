_pg_tempo_search() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --lookback)
    mapfile -t COMPREPLY < <(compgen -W "1h 12h 24h 3d 7d" -- "$cur")
    return 0
    ;;
  --select | --limit) return 0 ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--lookback --select --limit --help -h --version -v" -- "$cur")
    return 0
  fi
}
complete -F _pg_tempo_search pg-tempo-search
