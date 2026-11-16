#!/usr/bin/env bash
# S48: the default spool path. telemetry.toml says enabled = true and nothing
# else, so pn writes OTLP-JSON files to $XDG_STATE_HOME/otel-spool (traces/ and
# metrics/), connects to nothing, and prints nothing extra on stderr. The Go
# assertion hook then re-runs pn disabled (nothing written) and with a leftover
# endpoint key (exactly one deprecation notice).
set -euo pipefail

mkdir -p "$XDG_CONFIG_HOME/pn"
printf 'enabled = true\n' >"$XDG_CONFIG_HOME/pn/telemetry.toml"
