_lint_grafana_dashboards() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --error | --list)
    mapfile -t COMPREPLY < <(compgen -W "L1" -- "$cur")
    return 0
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--error --list --help -h --version -v" -- "$cur")
    return 0
  fi
  mapfile -t COMPREPLY < <(compgen -f -- "$cur")
}
complete -F _lint_grafana_dashboards lint-grafana-dashboards
