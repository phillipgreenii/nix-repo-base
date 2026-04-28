# shellcheck shell=bash

# sample-lib: A minimal bash library for testing mkBashLibrary
# Provides sample_greet and sample_add functions.

sample_greet() {
  local name="${1:?Usage: sample_greet <name>}"
  echo "Hello, ${name}!"
}

sample_add() {
  local a="${1:?Usage: sample_add <a> <b>}"
  local b="${2:?Usage: sample_add <a> <b>}"
  echo $((a + b))
}
