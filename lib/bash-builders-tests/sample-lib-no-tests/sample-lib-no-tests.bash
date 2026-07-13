# shellcheck shell=bash
# Sample library WITHOUT a tests/ directory. Exercises mkBashLibrary's optional
# tests/ handling (bead pg2-d7vvp): its check must still shellcheck the source
# instead of failing with a raw bats "no such directory" error.
sample_lib_no_tests_greet() {
  echo "hello from sample-lib-no-tests"
}
