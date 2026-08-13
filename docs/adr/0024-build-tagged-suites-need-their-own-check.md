# ADR-0024: A build-tagged test suite MUST have its own check; declare missing capabilities per-scenario

**Date:** 2026-08-12
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

ADR [0021](0021-subpackages-check-scoping-mkgotest.md) closed the "placebo gate" class: a
`subPackages`-scoped package build used _as_ a test gate silently runs tests in one subpackage
and skips the rest of the module. Its remedy is a dedicated unscoped `mkGoTest` check per module.

That ADR also sanctioned an escape hatch, verbatim: _"Sandbox-hostile tests (spawning daemons,
binding sockets) must be `build`-tag/`t.Skip`-guarded rather than weakening the whole gate."_
Correct as far as it goes — but it stopped one step short. It never said what happens to the
suite **after** it is tagged out, and a Go build tag that nothing ever sets is
indistinguishable from deleted code. So the escape hatch reproduced the very class 0021 closed,
one level down: the module gate is real, and a whole suite is simply not in it.

Measured on `modules/pn` (bead pg2-nuacd). `internal/workspace/smoke/` — 44 end-to-end scenarios
driving the real `pn` binary, plus a P1 invariant test — is behind `//go:build smoke`. Nothing in
the repo set that tag:
not `go test ./...`, not `pn-go-tests`, not `pn-golangci`, not the pre-commit or pre-push hooks,
not any other `nix flake check` attribute. The suite had not been compiled, let alone executed,
by any gate. Two independent rots were sitting in it:

- The TOFU hook trust gate landed 2026-07-12 (`cd30f10`, ADR
  [0019](0019-per-repo-event-hooks.md) / bead pg2-oymai) and made `build`/`apply` abort in an
  untrusted workspace. Four scenarios (S18, S19, S29, S36) bootstrap a synthetic workspace and
  never call `pn workspace allow`, so all four broke that day. They stayed "green" for a month
  because they never ran. Their fixtures pre-date the gate by a month (`0335d5d`, 2026-06-14).
- S33/S33b asserted that `pn workspace update` pushes to the remote — the exact **inverse** of
  the contract ADR [0023](0023-workspace-push-owns-sibling-propagation.md) shipped. Only a
  sibling change (`fb31cac`) deliberately going looking found it.

Both are the same failure: **a test suite that cannot fail is not a gate.** Worse than no tests,
because the file count implies coverage that does not exist.

The reason the suite was tagged out in the first place turned out not to survive contact with
measurement. Its actual needs are modest — `git` (real clones and commits, but only against
`file://` bare remotes, never the network), `bash`, and `nix` — and its cost is ~17s. `nix` is
**not** optional: `pn workspace lock` evaluates each repo's flake inputs, so withholding
`pkgs.nix` fails ~23 scenarios, not just the one nix-marked scenario. But local nix evaluation
works fine inside a build sandbox. Exactly one scenario (S23, `nix fmt` on a generated flake
carrying a `nixpkgs` input) needs something the sandbox genuinely cannot provide: **outbound
network** to resolve an external flake input.

The suite already had a capability-marker mechanism for this — a `requires-nix` marker file plus
a `nixAvailable()` probe, so a scenario self-skips instead of hard-failing. It was simply
under-specified: it conflated "nix is on PATH" with "nix can fetch", which are independent, and
the sandbox satisfies the first but not the second. Withholding `pkgs.nix` to force the existing
marker to fire was tried and rejected — it turns 23 honest passes into failures to make one
scenario skip.

## Decision

- A test suite excluded from the default build by a **Go build tag MUST be reachable from a
  named `nix flake check` attribute** that sets that tag. Adding the tag is not a decision to
  stop testing something; it is a decision to test it under a different gate, and that gate MUST
  exist. A tag that no check sets is dead code and MUST NOT be introduced or left in place.
- That check **SHOULD** be a separate `goBuilders.mkGoTest` with `testFlags = [ "-tags" "<tag>" ]`
  rather than the tag folded into the module's primary `mkGoTest`. A tagged-out suite is tagged
  out precisely because its environmental surface is larger, so it MUST NOT be able to take the
  primary unit-test gate down with it, and a distinct attribute name makes the gate discoverable
  in `nix flake show`. The overlap cost — the module's untagged suite running twice — is accepted
  as cheap relative to the suite running never. In this repo that check is `pn-smoke-tests`.
- A capability a scenario needs but the sandbox cannot supply **MUST** be declared **per
  scenario** — a marker file next to the scenario, gated by a stubbable package-level probe that
  calls `t.Skip` — and **MUST NOT** be handled by withholding the dependency from the whole
  check, nor by tagging the suite out again. Withholding is collective punishment: it converts
  every scenario that legitimately uses the dependency into a failure.
- Each such marker **MUST** name one capability and only one. `requires-nix` (a `nix` binary on
  PATH) and `requires-network` (outbound network, e.g. to resolve an external flake input) are
  **independent** and MUST stay separate: a build sandbox satisfies the first and not the second,
  so a single conflated marker cannot express it.
- A marker file **MUST** record why the scenario needs the capability and **what would remove the
  need** — the un-skip condition. A skip with no exit condition is a slower version of the
  build-tag defect this ADR exists to close.
- The `requires-network` probe **MUST** be deterministic, not a live dial. Presence of
  `NIX_BUILD_TOP` is the sanctioned signal: nix sets it only inside a builder, and a build
  sandbox has no network by construction (only fixed-output derivations do). A real DNS/TCP probe
  MUST NOT be used — it adds latency and a flaky failure mode to every run to answer a question
  already known with certainty.
- A scenario **MUST NOT** reach the real machine. Activation-shaped verbs (`build`, `apply`) MUST
  run the synthetic `build_command` / `apply_command` from the scenario's own `pn-workspace.toml`;
  no scenario may invoke `darwin-rebuild`, `sudo`, or a real activation.
- When a tagged-out suite is first wired to a gate, its accumulated failures MUST be diagnosed as
  **test defect** (fixture rotted against a shipped contract → fix the fixture) or **product
  defect** (the assertion is right and the product is broken → fix the product, or quarantine
  that one scenario with a recorded un-skip condition and a filed issue). A failing assertion
  MUST NOT be rewritten to match observed behavior merely to make the new gate green — that
  launders a product defect into a passing test and is the precise failure this ADR prevents.

## Consequences

### Positive

- The smoke suite is now in a gate: `nix flake check` builds `pn-smoke-tests`, which compiles the
  suite with `-tags smoke` and runs all 44 scenarios (43 in-sandbox; S23 skips). A wrong assertion
  now fails the gate — verified by injecting one deliberately and observing the derivation fail.
- Compile-level rot (a renamed helper, a changed signature) can no longer hide in the tagged
  suite, because the check compiles it on every `nix flake check`.
- The escape hatch 0021 sanctioned keeps working, but now terminates in a gate rather than in
  silence, so "tag it out" is no longer a way to delete coverage without deleting files.
- Capability markers make the residual exclusion **explicit and per-scenario**: exactly one
  scenario (S23) skips in the sandbox, visibly and with a recorded un-skip condition, instead of
  the whole suite skipping invisibly.

### Negative / Neutral

- `pn-smoke-tests` re-runs the module's untagged suite (~11s) because `mkGoTest` deliberately
  runs `go test ./...` unscoped (ADR 0021) and cannot be narrowed to one package without
  reintroducing the scoping footgun that ADR forbids. Accepted: ~11s of duplicate compute against
  a suite that previously never ran.
- S23 does not run under `nix flake check`; it runs only outside the sandbox. This is a real
  residual coverage gap, bounded to one scenario, declared in-tree, and removable by making the
  fixture resolve `nixpkgs` offline.
- The `NIX_BUILD_TOP` probe treats every nix builder as networkless. A fixed-output derivation
  does have network, so a scenario running inside one would skip unnecessarily. No such use
  exists; if one appears, the probe — a package-level var — is stubbable.

## Alternatives Considered

- **Fold `-tags smoke` into the existing `pn-go-tests`.** Rejected: one flaky end-to-end scenario
  would then redden the primary unit-test gate, and the smoke gate would have no name of its own
  to see or to run. Cheaper by ~11s; not worth the coupling.
- **Leave the suite manual and document the invocation.** Rejected. This was the fallback the bead
  allowed if the dependencies could not be supplied, and measurement showed they can be. A
  documented manual step is still a step nobody takes: the month-long rot above happened while
  `go test -tags smoke ./internal/workspace/smoke/` was, in principle, runnable by anyone.
- **`go vet -tags smoke ./...` as the only gate.** Rejected as insufficient on its own — it
  catches compile-level rot but not a wrong assertion, which is the defect actually observed. It
  is retained as a cheap additional floor, not as the answer.
- **Withhold `pkgs.nix` so the existing `requires-nix` marker fires for S23.** Rejected
  empirically: it fails ~23 scenarios whose `workspace lock` needs nix to evaluate local flakes.
  This is what motivated splitting `requires-network` out as its own capability.
- **A real DNS/TCP probe for network availability.** Rejected: slower and flaky, to determine
  something `NIX_BUILD_TOP` already answers exactly.
- **Delete the smoke suite.** Rejected: once actually run it earns its keep immediately — it is
  what surfaced the trust-gate breakage and the inverted push assertions.

## Related Decisions

- ADR [0021](0021-subpackages-check-scoping-mkgotest.md) — **amended by this ADR.** 0021 requires
  a real per-module test gate and sanctions build-tag guarding for sandbox-hostile tests; this
  ADR adds that the tagged-out suite MUST itself be gated, and that missing capabilities are
  declared per scenario rather than by withholding a dependency.
- ADR [0019](0019-per-repo-event-hooks.md) — the TOFU hook trust gate whose arrival broke four
  scenarios undetected; the fixtures now call `pn workspace allow`.
- ADR [0023](0023-workspace-push-owns-sibling-propagation.md) — the `update`-never-pushes contract
  the suite had been asserting the inverse of.
- ADR [0008](0008-adopt-gomod2nix-for-go-packages.md) — the gomod2nix engine `mkGoTest` builds on.
