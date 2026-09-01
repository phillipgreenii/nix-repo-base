_gogate() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --pkg) return 0 ;;
  esac

  if [[ $cur == -* ]]; then
    # -h and -v are offered alongside their long forms because the command
    # accepts both: -h/--help in the script, -v/--version reserved and
    # smoke-tested by the builder (mirrors pg-go-mutate's completions).
    mapfile -t COMPREPLY < <(compgen -W "--pkg --quick --help -h --version -v" -- "$cur")
    return 0
  fi
}
complete -F _gogate gogate
