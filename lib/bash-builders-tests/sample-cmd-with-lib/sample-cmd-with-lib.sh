# shellcheck shell=bash
# Command that uses sample-lib

show_help() {
  cat <<'HELP'
sample-cmd-with-lib: Command that uses a shared library

Usage: sample-cmd-with-lib [OPTIONS] NAME

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Examples:
  sample-cmd-with-lib Alice
HELP
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    break
    ;;
  --*)
    echo "Unknown option: $1" >&2
    exit 1
    ;;
  *) NAME="$1" ;;
  esac
  shift
done

if [[ -z ${NAME:-} ]]; then
  echo "error: NAME required" >&2
  exit 1
fi

sample_greet "$NAME"
