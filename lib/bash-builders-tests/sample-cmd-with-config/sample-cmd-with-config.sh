# shellcheck shell=bash
# Command that uses config injection

show_help() {
  cat <<'HELP'
sample-cmd-with-config: Command with config injection

Usage: sample-cmd-with-config [OPTIONS]

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Examples:
  sample-cmd-with-config
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

# Use scalar config
echo "greeting: ${SAMPLE_GREETING}"

# Use JSON config (parse with jq)
name=$(jq -r '.name' "$SAMPLE_CONFIG")
echo "name: ${name}"

# Use exported config (verify it's in environment)
echo "exported: ${SAMPLE_EXPORTED}"
