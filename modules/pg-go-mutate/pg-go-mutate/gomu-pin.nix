# THE single source of the gomu engine version pg-go-mutate is pinned to
# (spec "Engine selection", E1). Exactly two importers, and no other PRODUCTION
# file MUST hold a second copy of this string:
#
#   * ./default.nix — bakes it into the shipped script as
#     PGM_PINNED_GOMU_VERSION via mkBashScript's `config`, which is what makes
#     the UNWRAPPED package (`packages.pg-go-mutate` / `overlays.default`)
#     assert E1 on its own, with no home-manager module in the picture.
#   * ../../../home/pg-go-mutate/default.nix — asserts at eval time that the
#     engine it binds (`pkgs.phillipgreenii.gomu`, whose version the overlay
#     derives from the fetched tag) reports this same string, so the two halves
#     cannot drift apart silently.
#
# WHICH install path OBSERVES this pin — and so how a verification step MUST be
# written — is the outcome-shaped contract in repo-base CLAUDE.md, "Mutation
# testing": the UNWRAPPED package is where a mismatched engine MUST abort naming
# this string, while the WRAPPED home-manager binary MUST resist substitution
# (by PATH and by PG_GO_MUTATE_GOMU alike) and complete instead of aborting. A
# check that expects the abort from the wrapped binary is asking for the one
# outcome the wrapper exists to prevent (bead pg2-3x7xm).
#
# The TEST SUITES are deliberately not a third copy, and the two differ:
#
#   * the script suite (../pg-go-mutate/tests/test-pg-go-mutate.bats) reads the
#     baked value out of its check environment — mkBashScript exports every
#     `config` entry into it (lib/bash-builders.nix) — and every stub engine
#     reports THAT, so a bump here moves all of them with it. Before that, the
#     baked pin made the E1 check fire in end-to-end cases that are not about
#     the pin at all, so a legitimate bump broke six unrelated tests.
#   * the LIBRARY suite (../../lib/tests/test-pg-go-mutate-lib.bats) does still
#     contain the literal 0.2.1, and that is NOT a copy of this pin. mkBashLibrary
#     takes no `config`, so that check env structurally cannot carry the pin;
#     those cases set the stub's reported version AND the expected one
#     themselves, as self-consistent fixture pairs a bump here cannot break.
#
# This is NOT the rejected "pin gomu in repo-base" alternative (spec W6): W6
# forbids repo-base gaining a third-party FETCH, and this file adds none. It
# records an EXPECTATION about the engine a consumer supplies; the package
# recipe stays in phillipgreenii-nix-overlay, which repo-base cannot read
# (the overlay flake consumes repo-base, so an input edge back would be a cycle).
#
# It lives inside the script's `src` directory deliberately: mkBashScript's
# per-source digest (ADR 0006/0011) is computed over `src`, so a bump here
# changes the derivation's nvd-visible version. Held one directory up it would
# change the shipped artifact while reporting an unchanged version — the exact
# defect ADR 0011 was written to remove.
#
# To bump: change this string — WITHOUT the tag's leading "v", which the
# assertions below reject — and regenerate the overlay's `sources.gomu` to the
# matching tag in the same coordinated change (spec W16). The home-manager
# assertion fails eval until both agree.
let
  # THE pinned version. Recorded WITHOUT the tag's leading "v" — the assertions
  # below reject one, and explain why.
  pin = "0.2.1";
  # `lib` is not in scope: this file is imported with no arguments, by design, so
  # that both importers can read it without threading anything in. lib.assertMsg
  # is therefore spelled out with builtins.
  assertMsg = pred: msg: pred || builtins.throw "gomu-pin.nix: ${msg}";
in
# The pin's SHAPE is validated, not assumed, and validating it HERE fails eval
# for both importers at once. Neither importer can catch these on its own, and
# both failure modes are silent at eval and fatal at runtime:
#
#   * a LEADING "v" passes the home-manager assertion — which normalizes the
#     prefix off BOTH sides — and then aborts every run on BOTH install paths,
#     wrapped and unwrapped alike. The runtime comparison strips "v" only from
#     the version the ENGINE REPORTS (pgm_require_engine in
#     ../../lib/pg-go-mutate-lib.bash), never from this expectation, so no
#     reported field can ever equal "v0.3.0". The trap is baited: the tag this
#     tracks is recorded as "v0.2.1" in the overlay's `_sources`, and the bump
#     note above says to match the tag.
#   * an EMPTY pin is worse than a wrong one. `${VAR-default}` yields the empty
#     string, which the runtime check reads as "nothing is pinned" and returns
#     early — silently reopening the unwrapped-package gap this file exists to
#     close, with nothing left to catch it.
assert assertMsg (builtins.isString pin)
  "the pin MUST be a string; got a ${builtins.typeOf pin}. It is rendered verbatim into the shipped script as PGM_PINNED_GOMU_VERSION and compared as text at runtime.";
assert assertMsg (pin != "")
  "the pin MUST NOT be empty. An empty PGM_PINNED_GOMU_VERSION makes the runtime E1 check skip itself, which silently reopens the unwrapped-package gap this file closes (spec E2).";
assert assertMsg (builtins.substring 0 1 pin != "v")
  ''the pin MUST NOT carry a leading "v" (got "${pin}"). The runtime E1 check strips "v" from the version the ENGINE reports, never from this expectation, so a v-prefixed pin passes the home-manager assertion and then aborts every run. Record 0.2.1, not v0.2.1 — the overlay's `sources.gomu` keeps the v-prefixed TAG.'';
pin
