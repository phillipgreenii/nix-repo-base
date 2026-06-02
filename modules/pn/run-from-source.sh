#!/usr/bin/env bash
# run-from-source.sh - Run pn from source via `go run`.
#
# Injects a valid (non-"dev") version via -ldflags so the binary's version
# guard passes, letting you run pn straight from source without `nix build`.
# The injected version mirrors mkVersion's "<YYYYMMDD>-<7-char rev>" shape.
#
# Used by tests, CI, and developers when the compiled binary may be stale
# or absent. NOT on PATH. NOT exported via Nix. The compiled binary is
# gitignored (see .gitignore).
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
cd "$SCRIPT_DIR"
rev="$(git rev-parse --short=7 HEAD 2>/dev/null || echo source)"
version="$(date +%Y%m%d)-${rev}"
exec go run -ldflags "-X main.Version=${version}" ./cmd/pn "$@"
