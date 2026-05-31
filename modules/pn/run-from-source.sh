#!/usr/bin/env bash
# run-from-source.sh - Run pn from source via `go run`.
#
# Used by tests, CI, and developers when the compiled binary may be stale
# or absent. NOT on PATH. NOT exported via Nix. The compiled binary is
# gitignored (see .gitignore).
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
cd "$SCRIPT_DIR"
exec go run ./cmd/pn "$@"
