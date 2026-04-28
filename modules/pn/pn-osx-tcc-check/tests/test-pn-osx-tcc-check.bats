#!/usr/bin/env bats

# Tests for pn-osx-tcc-check script

# Resolve scripts directory
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

# Load test support
if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

create_tcc_db() {
  sqlite3 "$TCC_DB_PATH" "CREATE TABLE IF NOT EXISTS access (
    service TEXT NOT NULL,
    client TEXT NOT NULL,
    client_type INTEGER NOT NULL DEFAULT 0,
    auth_value INTEGER NOT NULL DEFAULT 0,
    auth_reason INTEGER NOT NULL DEFAULT 0,
    auth_version INTEGER NOT NULL DEFAULT 1,
    csreq BLOB,
    policy_id INTEGER,
    indirect_object_identifier_type INTEGER,
    indirect_object_identifier TEXT DEFAULT 'UNUSED',
    indirect_object_code_identity BLOB,
    flags INTEGER NOT NULL DEFAULT 0,
    last_modified INTEGER NOT NULL DEFAULT 0
  );"
}

insert_tcc_entry() {
  local service="$1" client="$2" last_modified="$3" auth_value="${4:-2}"
  sqlite3 "$TCC_DB_PATH" "INSERT INTO access (service, client, last_modified, auth_value) VALUES ('$service', '$client', $last_modified, $auth_value);"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    export TCC_DB_PATH="$TEST_DIR/TCC.db"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-osx-tcc-check shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-osx-tcc-check shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-osx-tcc-check FDA gate: skips with warning when DB is non-existent" {
    export TCC_DB_PATH="$TEST_DIR/nonexistent.db"
    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "TCC check skipped"
}

@test "pn-osx-tcc-check no duplicates: unique Nix entries show no-duplicates message" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/aaa111-sleepwatcher/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/bbb222-other/bin/tool" 2000
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/ccc333-camera/bin/cam" 3000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "No TCC duplicates found"
}

@test "pn-osx-tcc-check detects duplicates: 3 sleepwatcher entries with different store hashes" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/old111-sleepwatcher/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/old222-sleepwatcher/bin/sleepwatcher" 2000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/new333-sleepwatcher/bin/sleepwatcher" 3000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "kTCCServiceListenEvent"
    echo "$output" | grep -q "sleepwatcher"
    echo "$output" | grep -q "3 entries"
    echo "$output" | grep -q "2 stale"
    echo "$output" | grep -q "(current)"
    echo "$output" | grep -q "(stale)"
}

@test "pn-osx-tcc-check marks newest as current: higher last_modified gets checkmark" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/old-sleepwatcher/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/new-sleepwatcher/bin/sleepwatcher" 2000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "new-sleepwatcher.*(current)"
    echo "$output" | grep -q "old-sleepwatcher.*(stale)"
}

@test "pn-osx-tcc-check multiple services: both service names appear" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/aaa-sleepwatcher/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/bbb-sleepwatcher/bin/sleepwatcher" 2000
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/ccc-cam/bin/camera" 1000
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/ddd-cam/bin/camera" 2000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "kTCCServiceListenEvent"
    echo "$output" | grep -q "kTCCServiceCamera"
}

@test "pn-osx-tcc-check non-nix entries ignored: paths outside /nix/store produce no output" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/usr/local/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/Applications/SomeApp.app/Contents/MacOS/app" 2000
    insert_tcc_entry "kTCCServiceListenEvent" "/opt/homebrew/bin/tool" 3000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "No TCC duplicates found"
}

@test "pn-osx-tcc-check groups different versions of same binary together" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceMicrophone" "/nix/store/aaa-bash-5.2p37/bin/bash" 1000
    insert_tcc_entry "kTCCServiceMicrophone" "/nix/store/bbb-bash-5.2p37/bin/bash" 2000
    insert_tcc_entry "kTCCServiceMicrophone" "/nix/store/ccc-bash-5.3p3/bin/bash" 3000
    insert_tcc_entry "kTCCServiceMicrophone" "/nix/store/ddd-bash-5.3p3/bin/bash" 4000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    # All 4 entries should be in one group, not two separate version groups
    echo "$output" | grep -q "4 entries"
    echo "$output" | grep -q "3 stale"
    # Only the newest (ddd-bash-5.3p3) should be current
    echo "$output" | grep -q "ddd-bash-5.3p3.*(current)"
    echo "$output" | grep -q "aaa-bash-5.2p37.*(stale)"
    echo "$output" | grep -q "bbb-bash-5.2p37.*(stale)"
    echo "$output" | grep -q "ccc-bash-5.3p3.*(stale)"
}

@test "pn-osx-tcc-check cleanup instructions: duplicates found includes System Preferences and remove stale entries" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/aaa-sleepwatcher/bin/sleepwatcher" 1000
    insert_tcc_entry "kTCCServiceListenEvent" "/nix/store/bbb-sleepwatcher/bin/sleepwatcher" 2000

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "System Preferences"
    echo "$output" | grep -q "remove stale entries manually"
}

@test "pn-osx-tcc-check disabled entries excluded: all-disabled duplicates produce no output" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/aaa-bash-5.2p37/bin/bash" 1000 0
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/bbb-bash-5.3p3/bin/bash" 2000 0

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "No TCC duplicates found"
}

@test "pn-osx-tcc-check disabled entries excluded: mixed enabled/disabled with single enabled produces no output" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/aaa-bash-5.2p37/bin/bash" 1000 0
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/bbb-bash-5.3p3/bin/bash" 2000 2

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "No TCC duplicates found"
}

@test "pn-osx-tcc-check disabled entries excluded: only enabled duplicates are reported" {
    create_tcc_db
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/aaa-bash-5.2p37/bin/bash" 1000 0
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/bbb-bash-5.3p3/bin/bash" 2000 2
    insert_tcc_entry "kTCCServiceCamera" "/nix/store/ccc-bash-5.3p3/bin/bash" 3000 2

    run bash "$SCRIPTS_DIR/pn-osx-tcc-check.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "2 entries"
    echo "$output" | grep -q "1 stale"
    # The disabled entry should not appear at all
    ! echo "$output" | grep -q "aaa-bash"
}
