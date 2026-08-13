_wsplan() {
  local cur prev words cword
  _init_completion || return

  local subcommands="land-plan"

  # First positional arg: complete the subcommand name (or a top-level flag).
  if [[ $cword -eq 1 ]]; then
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    else
      mapfile -t COMPREPLY < <(compgen -W "$subcommands" -- "$cur")
    fi
    return
  fi

  # --root takes a DIRECTORY (it must be an absolute path to an existing one);
  # --set-branch takes a set branch name, which only the caller's tracker item
  # knows, so it is deliberately left uncompleted.
  case "$prev" in
  --root)
    _filedir -d
    return
    ;;
  --set-branch)
    return
    ;;
  esac

  case "${words[1]}" in
  land-plan)
    mapfile -t COMPREPLY < <(compgen -W "--root --set-branch --help -h" -- "$cur")
    ;;
  *)
    mapfile -t COMPREPLY < <(compgen -W "--help -h" -- "$cur")
    ;;
  esac
}

complete -F _wsplan wsplan
