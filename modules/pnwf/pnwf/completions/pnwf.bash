_pnwf() {
  local cur words cword
  _init_completion || return

  local subcommands="resolve repos stage fork-preflight land-plan cleanup status sync-fetch"

  # First positional arg: complete a subcommand name (or a top-level flag).
  if [[ $cword -eq 1 ]]; then
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    else
      mapfile -t COMPREPLY < <(compgen -W "$subcommands" -- "$cur")
    fi
    return
  fi

  # After the subcommand: resolve/repos/stage take --set; fork-preflight
  # takes --repos; cleanup takes the two --force-… flags; every subcommand
  # takes --help.
  case "${words[1]}" in
  resolve | repos | stage)
    mapfile -t COMPREPLY < <(compgen -W "--set --help -h" -- "$cur")
    ;;
  fork-preflight)
    mapfile -t COMPREPLY < <(compgen -W "--repos --help -h" -- "$cur")
    ;;
  cleanup)
    mapfile -t COMPREPLY < <(compgen -W "--force-dirty-worktree-removal --force-unlanded-branch-removal --help -h" -- "$cur")
    ;;
  *)
    mapfile -t COMPREPLY < <(compgen -W "--help -h" -- "$cur")
    ;;
  esac
}

complete -F _pnwf pnwf
