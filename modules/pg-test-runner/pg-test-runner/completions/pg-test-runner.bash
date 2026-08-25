_pg_test_runner() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --labels | --config) return 0 ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--labels --files --all --config --help -h --version -v" -- "$cur")
    return 0
  fi
  mapfile -t COMPREPLY < <(compgen -f -- "$cur")
}
complete -F _pg_test_runner pg-test-runner
