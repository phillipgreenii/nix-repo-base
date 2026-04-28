# shellcheck shell=bash
# Internal command (not on PATH)

show_help() {
  cat <<'HELP'
sample-internal: An internal helper command

Usage: sample-internal [OPTIONS]

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Examples:
  sample-internal
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
  esac
  shift
done

echo "internal command ran"
