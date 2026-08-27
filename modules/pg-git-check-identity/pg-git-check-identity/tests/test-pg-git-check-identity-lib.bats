#!/usr/bin/env bats
# bats file_tags=type:unit
# shellcheck disable=SC1090,SC2030,SC2031

SCRIPTS_DIR="${SCRIPTS_DIR:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)}"

setup() {
  # shellcheck source=/dev/null
  source "$SCRIPTS_DIR/pg-git-check-identity.bash"
}

# -- pgci_extract_name / pgci_extract_email / pgci_extract_domain -------------

@test "pgci_extract_name: strips the trailing <email> timestamp tz" {
  run pgci_extract_name "Jane Doe <jane@example.com> 1700000000 -0500"
  [ "$status" -eq 0 ]
  [ "$output" = "Jane Doe" ]
}

@test "pgci_extract_email: pulls the address out of angle brackets" {
  run pgci_extract_email "Jane Doe <jane@example.com> 1700000000 -0500"
  [ "$status" -eq 0 ]
  [ "$output" = "jane@example.com" ]
}

@test "pgci_extract_domain: pulls the domain out of an email" {
  run pgci_extract_domain "jane@example.com"
  [ "$status" -eq 0 ]
  [ "$output" = "example.com" ]
}

# -- pgci_is_fake_domain -------------------------------------------------------

@test "pgci_is_fake_domain: matches example.com/.org/.net case-insensitively" {
  run pgci_is_fake_domain "example.com"
  [ "$status" -eq 0 ]
  run pgci_is_fake_domain "EXAMPLE.ORG"
  [ "$status" -eq 0 ]
  run pgci_is_fake_domain "example.net"
  [ "$status" -eq 0 ]
}

@test "pgci_is_fake_domain: does not match a real-looking domain" {
  run pgci_is_fake_domain "ziprecruiter.com"
  [ "$status" -ne 0 ]
}

@test "pgci_is_fake_domain: does not match .invalid (the non-human account convention)" {
  run pgci_is_fake_domain "non-human.invalid"
  [ "$status" -ne 0 ]
}

@test "pgci_is_fake_domain: matches a subdomain of example.com/.org/.net" {
  run pgci_is_fake_domain "mail.example.com"
  [ "$status" -eq 0 ]
}

@test "pgci_is_fake_domain: does not false-positive on a domain merely ending in the substring" {
  run pgci_is_fake_domain "grexample.com"
  [ "$status" -ne 0 ]
  run pgci_is_fake_domain "notexample.com"
  [ "$status" -ne 0 ]
  run pgci_is_fake_domain "myexample.org"
  [ "$status" -ne 0 ]
  run pgci_is_fake_domain "bestexample.net"
  [ "$status" -ne 0 ]
}

@test "pgci_is_fake_domain: an empty domain does not match" {
  run pgci_is_fake_domain ""
  [ "$status" -ne 0 ]
}

# -- pgci_is_fake_name ----------------------------------------------------------

@test "pgci_is_fake_name: matches common test/placeholder names case-insensitively" {
  run pgci_is_fake_name "test"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "Test"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "TEST"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "t"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "T"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "tester"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "testuser"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "test user"
  [ "$status" -eq 0 ]
  run pgci_is_fake_name "Test User"
  [ "$status" -eq 0 ]
}

@test "pgci_is_fake_name: does not match a real-looking name" {
  run pgci_is_fake_name "Phillip Green"
  [ "$status" -ne 0 ]
}

@test "pgci_is_fake_name: does not false-positive on a name merely containing 'test'" {
  run pgci_is_fake_name "Contest Winner"
  [ "$status" -ne 0 ]
}

@test "pgci_is_fake_name: an empty name does not match" {
  run pgci_is_fake_name ""
  [ "$status" -ne 0 ]
}

# -- pgci_check_identity --------------------------------------------------------

@test "pgci_check_identity: passes a real-looking identity" {
  run pgci_check_identity author "Phillip Green <phillipg@ziprecruiter.com> 1700000000 -0500"
  [ "$status" -eq 0 ]
}

@test "pgci_check_identity: rejects a placeholder email domain and names the role" {
  run pgci_check_identity author "Some Name <name@example.com> 1700000000 -0500"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "author" ]]
  # shellcheck disable=SC2076  # intentional literal match; a regex "." would also match
  [[ "$output" =~ "example.com" ]]
}

@test "pgci_check_identity: rejects a placeholder name and names the role" {
  run pgci_check_identity committer "Test User <tu@realcorp.com> 1700000000 -0500"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "committer" ]]
  [[ "$output" =~ "Test User" ]]
}

@test "pgci_check_identity: passes the repo's own non-human account convention" {
  run pgci_check_identity author "tcagent <tcagent@non-human.invalid> 1700000000 -0500"
  [ "$status" -eq 0 ]
}

@test "pgci_check_identity: an empty ident passes -- detecting an UNRESOLVABLE identity is the .sh caller's job (via set -e on a failed 'git var'), not this pure function's" {
  run pgci_check_identity author ""
  [ "$status" -eq 0 ]
}

@test "pgci_check_identity: the rejection message names the GIT_AUTHOR_* env var for an author, not just config" {
  run pgci_check_identity author "Real Name <real@example.com> 1700000000 -0500"
  [[ "$output" =~ "GIT_AUTHOR_EMAIL" ]]
}

@test "pgci_check_identity: the rejection message names the GIT_COMMITTER_* env var for a committer, not just config" {
  run pgci_check_identity committer "Test User <tu@realcorp.com> 1700000000 -0500"
  [[ "$output" =~ "GIT_COMMITTER_NAME" ]]
}
