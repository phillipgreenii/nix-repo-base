# bash completion for sample-cmd

_sample_cmd() {
  local cur opts
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  opts="--help --version --upper"

  if [[ ${cur} == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "${opts}" -- "${cur}")
  fi
}

complete -F _sample_cmd sample-cmd
