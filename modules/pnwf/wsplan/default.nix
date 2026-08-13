{
  mkBashScript,
  pkgs,
  pnwf-lib,
  pnwf,
  pn,
}:

mkBashScript {
  name = "wsplan";
  src = ./.;
  description = "Read-only land-plan emitter: detect workspace shape and emit a typed plan";
  public = true;
  libraries = [ pnwf-lib ];
  runtimeDeps = [
    pkgs.git
    pkgs.jq
    pnwf.script
    # REQUIRED: the set path shells `pn workspace info --json` for the set
    # directory, which `pnwf resolve` does not expose. Threaded in from
    # self.packages (see scripts.nix / flake.nix) — nixpkgs has no `pn`
    # attribute, and overlays.default (the only thing that surfaces one) is
    # exported for CONSUMERS and never applied to this flake's own pkgs.
    pn
    # `integrate-branch-support` is DELIBERATELY NOT declared here, and this is
    # the explicit choice the design (§3.1) requires rather than an assumption
    # inherited in silence.
    #
    # pnwf-lib's pnwf_resolve_primary_branch shells it and parses
    # .primary_branch. It lives in the phillipgreenii-nix-agent-support flake,
    # which DEPENDS ON this one (the workspace lock records
    # `phillipgreenii-nix-agent-support -> phillipg-nix-repo-base`), so
    # declaring it would mean adding agent-support as an input here and
    # inverting that edge into a cycle. `pnwf` itself already relies on ambient
    # PATH for exactly this binary, and both commands reach PATH through the
    # same agent-support overlay that provides it — so on any machine where
    # `wsplan` is installed, `integrate-branch-support` is too.
    #
    # The consequence for tests is not left implicit either: the bats check's
    # PATH is only `[ bats bash ] ++ optional (batsJobs > 1) parallel ++
    # testDeps` (lib/bash-builders.nix:370-376), so an undeclared binary is
    # simply ABSENT there and both suites mock it (as the pnwf suite does).
  ];
  # Every test isolates itself (own mktemp TEST_DIR + own MOCK_BIN), so the
  # suite is parallel-safe; matches pnwf's batsJobs.
  batsJobs = 8;
  testDeps = [
    pkgs.git
    pkgs.jq
  ];
}
