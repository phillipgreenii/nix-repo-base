# shellcheck shell=bash
_pg_hooks() {
  local cur prev cmd stages
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"
  cmd="${COMP_WORDS[1]:-}"
  stages="pre-commit pre-merge-commit pre-push pre-rebase commit-msg prepare-commit-msg post-checkout post-commit post-merge post-rewrite manual pre-land"

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "status list explain run fix --help -h --version -v" -- "$cur")
    return 0
  fi

  case "$cmd" in
  status)
    mapfile -t COMPREPLY < <(compgen -W "--porcelain --help" -- "$cur")
    ;;
  explain)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "$stages" -- "$cur")
    fi
    ;;
  run)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "$stages" -- "$cur")
    elif [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--all-files --" -- "$cur")
    elif [[ $prev != "--" ]]; then
      mapfile -t COMPREPLY < <(compgen -f -- "$cur")
    fi
    ;;
  esac
  return 0
}
complete -F _pg_hooks pg-hooks
