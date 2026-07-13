# shellcheck shell=bash
# Sample command WITHOUT a tests/ directory. Exercises mkBashScript's optional
# tests/ handling (bead pg2-d7vvp): its check must still run the assembled
# artifact floor smoke (--version/-v) instead of failing with a raw bats/cp
# "no such directory" error.
echo "sample-cmd-no-tests: hello"
