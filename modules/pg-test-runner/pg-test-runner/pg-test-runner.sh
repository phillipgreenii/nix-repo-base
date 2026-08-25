# shellcheck shell=bash
# pg-test-runner: label-driven direct test runner. Entry point only — argument
# parsing and mode-exclusivity live here; discovery/execution live in
# pg-test-runner.bash (sourced by the builder before this file). See
# docs/superpowers/specs/2026-08-24-pg-test-runner-design.md (rev 6), section 2.2.

show_help() {
  cat <<'HELP'
pg-test-runner: label-driven, nix-free-at-runtime direct test runner.

USAGE
  pg-test-runner [--labels <csv>] --files <file>...
  pg-test-runner [--labels <csv>] [<path>...]
  pg-test-runner [--labels <csv>] --all

The three input modes (--files, path arguments, --all) are mutually
exclusive; combining them is a usage error.

OPTIONS
  --labels <csv>   Comma-separated label list, or "all" (no filtering).
                   Defaults to "unit". Unknown labels pass through to the
                   per-language selection mechanism unvalidated.
  --files <file>...
                   prek mode: resolve each staged file to its owning project
                   (nearest ancestor marker, upward walk only) and run those.
                   A file matching no project contributes nothing.
  --all            Every project under the repo toplevel (downward scan).
  <path>...        Ad hoc mode (default: cwd). Each path resolves per the
                   upward-then-downward-fallback algorithm in section 2.3 of
                   the design doc.
  --config <path>  Override the configuration. Normally the baked-in default
                   (or $PG_TEST_RUNNER_CONFIG) is used.
  -h, --help       Show this help message.

EXIT CODES
  0   All selected tests passed, including vacuous cases.
  1   Generic/unexpected error.
  2   Usage error (bad flags, mode conflict, nonexistent path argument).
  10  One or more test invocations failed (including a timeoutSeconds cap).
  11  A required language tool was missing from PATH.
  12  A path argument resolved to no project, upward or downward.
  13  Configuration missing, unreadable, or invalid.
HELP
}

FILES_FLAG=0
ALL_FLAG=0
LABELS="unit"
CONFIG_FLAG=""
POSITIONAL_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --labels)
    [[ $# -ge 2 ]] || {
      echo "pg-test-runner: --labels needs a value" >&2
      exit 2
    }
    LABELS="$2"
    shift 2
    ;;
  --files)
    FILES_FLAG=1
    shift
    ;;
  --all)
    ALL_FLAG=1
    shift
    ;;
  --config)
    [[ $# -ge 2 ]] || {
      echo "pg-test-runner: --config needs a value" >&2
      exit 2
    }
    CONFIG_FLAG="$2"
    shift 2
    ;;
  --)
    shift
    while [[ $# -gt 0 ]]; do
      POSITIONAL_ARGS+=("$1")
      shift
    done
    ;;
  --*)
    echo "pg-test-runner: unknown option: $1" >&2
    exit 2
    ;;
  *)
    POSITIONAL_ARGS+=("$1")
    shift
    ;;
  esac
done

# Mode exclusivity (section 2.2): --files and --all never combine, and --all
# takes no path arguments.
if [[ $FILES_FLAG == 1 && $ALL_FLAG == 1 ]]; then
  echo "pg-test-runner: --files and --all are mutually exclusive" >&2
  exit 2
fi
if [[ $ALL_FLAG == 1 && ${#POSITIONAL_ARGS[@]} -gt 0 ]]; then
  echo "pg-test-runner: --all does not take path arguments" >&2
  exit 2
fi

if [[ $FILES_FLAG == 1 ]]; then
  MODE="files"
elif [[ $ALL_FLAG == 1 ]]; then
  MODE="all"
else
  MODE="paths"
  if [[ ${#POSITIONAL_ARGS[@]} -eq 0 ]]; then
    POSITIONAL_ARGS=(".")
  fi
fi

# Resolution precedence (section 2.1): --config flag, else
# $PG_TEST_RUNNER_CONFIG, else the baked-in default (injected as a local var
# by mkBashScript's `config`; empty in a raw/unconfigured source run).
if [[ -n $CONFIG_FLAG ]]; then
  CONFIG_PATH="$CONFIG_FLAG"
elif [[ -n ${PG_TEST_RUNNER_CONFIG:-} ]]; then
  CONFIG_PATH="$PG_TEST_RUNNER_CONFIG"
else
  CONFIG_PATH="${PG_TEST_RUNNER_DEFAULT_CONFIG:-}"
fi

# Engine-internal tool resolution (section 2.5's ambient-PATH-only rule is
# about the per-language `tools` gate, NOT this): resolves the ABSOLUTE
# store paths mkBashScript's `config` injects, one time here. This MUST live
# in this file, not pg-test-runner.bash: the builder's shellcheck pass never
# follows a `source` (SC1091 is excluded rather than resolved via `-x`), so
# a read inside a function defined in the sourced library is invisible to
# it regardless of where the corresponding assignment lives -- confirmed
# empirically both directions (config lines read inside the library, and
# these vars themselves assigned here but only read inside the library).
# `export` is what actually satisfies shellcheck's SC2034 here (its own
# message: "or export if used externally") -- it is also otherwise
# harmless: every project invocation these two feed (jq/timeout paths) is a
# child process of THIS script anyway.
export PTR_JQ="${PG_TEST_RUNNER_JQ_BIN:-jq}"
export PTR_TIMEOUT="${PG_TEST_RUNNER_TIMEOUT_BIN:-timeout}"

ptr_run "$MODE" "$LABELS" "$CONFIG_PATH" "${POSITIONAL_ARGS[@]}"
