{
  mkBashScript,
  pkgs,
  pg-go-mutate-lib,
}:

mkBashScript {
  name = "pg-go-mutate-sweep";
  src = ./.;
  description = "Resumable unattended mutation sweep over every Go package in the workspace";
  public = true;
  libraries = [ pg-go-mutate-lib ];
  # NEITHER pg-go-mutate NOR bd is a runtimeDep, deliberately. runtimeDeps are
  # appended with --suffix PATH, so listing them could not displace the machine's
  # wrapper -- the hazard is the reverse: it would provide a SILENT FALLBACK to an
  # unwrapped pg-go-mutate or an unmanaged bd when the wrapped one is absent. An
  # unwrapped pg-go-mutate carries neither the engine store-path binding nor the
  # E1 version assertion, and a bd from outside the machine wrapper loses
  # BEADS_DOLT_AUTO_START=0 and can spawn a competing dolt server on port 25252.
  # With no fallback the preflight in pg-go-mutate-sweep.sh fails loudly (exit 4)
  # instead, naming the apply the operator still owes.
  #
  # go is deliberately NOT here either -- it is in testDeps, matching the sibling,
  # which requires an ambient toolchain. In runtimeDeps it would be inert today,
  # but if it ever won it would run pgm_detect_tags under a different toolchain
  # than the analysis, changing which go1.NN-gated files are visible.
  #
  # gawk IS here, correcting the plan: the plan omitted it on the grounds that awk
  # is reached only through pgm_has_tests, which this command never calls. The
  # first half is true and the conclusion does not follow -- pg-go-mutate-sweep.bash
  # calls awk itself, in pgms_unit_status (the per-unit replay lookup, run for
  # every unit in the plan and again inside pgms_bead_due) and in
  # pgms_lock_acquire (reading the holder pid, run on every invocation). Both uses
  # are POSIX-clean, so an ambient /usr/bin/awk satisfies them and the omission
  # would go unnoticed on this machine; the failure it leaves open is silent and
  # expensive rather than loud. With awk absent pgms_unit_status returns empty for
  # every unit, so a resumed sweep reads its whole ledger as unrun and re-analyses
  # a workspace that costs hours, and pgms_lock_acquire reads an empty holder pid
  # and takes the stale-reclaim path against a LIVE holder. Declaring it cannot
  # displace anything (--suffix, and there is no wrapper to defeat), so unlike
  # pg-go-mutate/bd/go there is no countervailing hazard.
  #
  # No engine-pin `config` either, unlike the sibling's PGM_PINNED_GOMU_VERSION:
  # the sweep never invokes the engine directly, and its preflight deliberately
  # delegates the pin check to the first unit's exit 13 (ADR-0026 section 3).
  runtimeDeps = [
    pkgs.jq
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.coreutils
    pkgs.gawk
  ];
  batsJobs = 4;
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
