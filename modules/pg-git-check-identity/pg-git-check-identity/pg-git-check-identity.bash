# shellcheck shell=bash

# RFC 2606 reserves example.com/.org/.net for documentation/placeholder use;
# nothing legitimate is ever sent from there. Deliberately excludes .invalid
# (also RFC 2606-reserved): phillipgreenii-nix-personal's own non-human
# account identity scheme derives `<username>@non-human.invalid`
# (nixos/accounts/default.nix in that repo), so flagging .invalid would
# reject legitimate agent-account commits.
#
# `(^|\.)` anchors the left side so a real domain that merely ENDS in the
# substring "example.com" (e.g. "grexample.com", "notexample.com") does not
# false-positive -- only "example.com" itself or a subdomain of it
# ("mail.example.com") matches.
#
# The second alternative catches the `gittest`/`gitfixture` isolated-repo
# fixture's identity scheme (gitclient design pg2-svfbb section 5 guarantee
# 3: `<Suite>@gitfixture.invalid`) with the same `(^|\.)` left guard, so
# "sub.gitfixture.invalid" matches but "notgitfixture.invalid" does not.
# LOCK-STEP: this pattern and the fixture's identity scheme are defined
# together (pg2-svfbb section 5 guarantee 3) and MUST be updated together.
#
# The third alternative catches the bash-side hermetic bats git-fixture
# harness's identity scheme (design pg2-gucfd D3; implementation epic
# pg2-ljn47 / lib/scripts/git-fixture-harness.bash: `<suite>@bashfixture.invalid`),
# with the same `(^|\.)` left guard, so "sub.bashfixture.invalid" matches but
# "notbashfixture.invalid" does not. LOCK-STEP: this pattern and
# GFH_IDENTITY_DOMAIN in git-fixture-harness.bash are defined together and
# MUST be updated together.
PGCI_FAKE_DOMAIN_RE='(^|\.)example\.(com|org|net)$|(^|\.)gitfixture\.invalid$|(^|\.)bashfixture\.invalid$'

# Placeholder/test-fixture names, not a real person -- e.g. a test suite that
# overrode git config and never restored it (the incident this exists for).
#
# The second alternative catches the `gittest`/`gitfixture` isolated-repo
# fixture's identity scheme (pg2-svfbb section 5 guarantee 3:
# `gitfixture <Suite>`) -- prefix-anchored and space-or-end anchored rather
# than a `\b` word boundary, since `\b` is a GNU grep extension and this
# script's `grep` resolves from ambient PATH (only `git` is a pinned
# runtimeDep), not a pinned GNU grep. LOCK-STEP: see the domain regex above.
PGCI_FAKE_NAME_RE='^(t|test|tester|testuser|test ?user)$|^gitfixture( |$)'

# Extracts the "Name" portion of a git ident string, e.g.
# `Jane Doe <jane@example.com> 1700000000 -0500` -> `Jane Doe`.
pgci_extract_name() {
  printf '%s' "$1" | sed -E 's/ <.*//'
}

# Extracts the email portion (without angle brackets).
pgci_extract_email() {
  printf '%s' "$1" | sed -E 's/^.*<(.*)>.*/\1/'
}

# Extracts the domain portion of an email address.
pgci_extract_domain() {
  printf '%s' "$1" | sed -E 's/^.*@//'
}

pgci_is_fake_domain() {
  printf '%s' "$1" | grep -qiE "$PGCI_FAKE_DOMAIN_RE"
}

pgci_is_fake_name() {
  printf '%s' "$1" | grep -qiE "$PGCI_FAKE_NAME_RE"
}

# Checks one git identity (the author or committer of the commit about to be
# made). $1 is "author" or "committer" (used only for the diagnostic); $2 is
# a git ident string as returned by `git var GIT_AUTHOR_IDENT` /
# `GIT_COMMITTER_IDENT`. Prints a diagnostic to stderr and returns 1 if the
# identity looks like a test/placeholder account; returns 0 otherwise.
pgci_check_identity() {
  local role="$1"
  local ident="$2"
  local name email domain role_upper name_var email_var

  name="$(pgci_extract_name "$ident")"
  email="$(pgci_extract_email "$ident")"
  domain="$(pgci_extract_domain "$email")"

  # `git var` resolves GIT_AUTHOR_*/GIT_COMMITTER_* env vars ahead of
  # user.name/user.email config, so an override via either source produces
  # the same rejected ident here. Name both possible causes in the
  # diagnostic -- pointing only at `git config --show-origin` misleads
  # someone whose *config* is correct but whose environment is overridden,
  # since that command would show them their real (unaffected) config.
  role_upper="$(printf '%s' "$role" | tr '[:lower:]' '[:upper:]')"
  name_var="GIT_${role_upper}_NAME"
  email_var="GIT_${role_upper}_EMAIL"

  if pgci_is_fake_domain "$domain"; then
    echo "pg-git-check-identity: $role email '$email' looks like a placeholder address (matches /$PGCI_FAKE_DOMAIN_RE/i) -- refusing to commit. Check for a test/fixture that overrode git config (see 'git config --show-origin user.email') or set $email_var (see 'env | grep $role_upper')." >&2
    return 1
  fi

  if pgci_is_fake_name "$name"; then
    echo "pg-git-check-identity: $role name '$name' looks like a placeholder identity (matches /$PGCI_FAKE_NAME_RE/i) -- refusing to commit. Check for a test/fixture that overrode git config (see 'git config --show-origin user.name') or set $name_var (see 'env | grep $role_upper')." >&2
    return 1
  fi

  return 0
}
