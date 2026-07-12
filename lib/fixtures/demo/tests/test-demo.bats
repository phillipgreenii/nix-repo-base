#!/usr/bin/env bats
# Drives the ASSEMBLED artifact (SCRIPT_UNDER_TEST), not the raw source, proving
# mkBashScript's injected version handler AND injected config line run in the
# shipped script. See bead pg2-28wwb.

setup() {
  [[ -n ${SCRIPT_UNDER_TEST:-} ]] || skip "assembled artifact only present in the nix check"
}

@test "assembled demo --version emits '<name> <version>' and exits 0 (raw source has no handler)" {
  run "$SCRIPT_UNDER_TEST" --version
  [ "$status" -eq 0 ]
  [[ "$output" == "demo "* ]]
}

@test "assembled demo runs its body" {
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -eq 0 ]
  [[ "$output" == *"hello from demo"* ]]
}

@test "assembled demo carries the injected config value (config injection is assembly-only)" {
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -eq 0 ]
  [[ "$output" == *"greeting=howdy"* ]]
}
