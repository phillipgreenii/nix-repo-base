# shellcheck shell=bash

show_help() {
  cat <<'HELP'
sample-cmd: A sample command for testing

Usage: sample-cmd [OPTIONS] [NAME]

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
  -u, --upper    Convert output to uppercase

Arguments:
  NAME           Name to greet (default: world)

Examples:
  sample-cmd
  sample-cmd --upper Alice
HELP
}

UPPER=false
NAME="world"

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  -u | --upper) UPPER=true ;;
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

greeting="hello, ${NAME}"
if [[ $UPPER == "true" ]]; then
  greeting=$(echo "$greeting" | tr '[:lower:]' '[:upper:]')
fi
echo "$greeting"
