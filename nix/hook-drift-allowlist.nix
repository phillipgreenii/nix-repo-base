# repo-base's own allowlist for the hook drift guard (pg-hooks-drift-guard,
# `phillipgreenii.pre-commit.driftGuard`; plan Task 22, ADR 0032). Every entry is
# explicit: a path glob (matched against the repo-relative path; `*` also crosses
# `/`), the categories it allows (githooks-dir, githooks-ref, relative-hooks-path,
# config-link, removed-machinery), and the reason. The patterns themselves are
# never loosened. Keep the reasons free of tabs and newlines.
[
  # ----- frozen history: describes the machinery as it was -----
  {
    path = "docs/superpowers/*";
    categories = [ "*" ];
    reason = "frozen point-in-time plan, spec and verification snapshots; they describe the shim and the link code as they were";
  }
  {
    path = "docs/design/pg2-migib-*.md";
    categories = [ "*" ];
    reason = "frozen design record of the root-cause fix that removed the link and the config bypass";
  }
  {
    path = "2026-06-12-*-deepdive.md";
    categories = [ "*" ];
    reason = "frozen audit snapshot from before the hook bundle";
  }
  {
    path = "docs/adr/0016-*";
    categories = [ "*" ];
    reason = "ADR 0016 (gitignore of the generated config): its text is the record of the rule that is kept until the follow-up retires it";
  }
  {
    path = "docs/adr/0023-*";
    categories = [ "config-link" ];
    reason = "ADR 0023 context section describes the per-machine config symlink that motivated it";
  }
  {
    path = "docs/adr/0029-*";
    categories = [ "*" ];
    reason = "ADR 0029 (commit-time shim experiment), superseded by ADR 0032; the text is the record of what was removed";
  }
  {
    path = "docs/adr/0032-*";
    categories = [ "*" ];
    reason = "ADR 0032 names the removed machinery it deleted, by identifier";
  }

  # ----- kept on purpose (ADR 0032, R2): the old-symlink gitignore rule and check -----
  {
    path = ".gitignore";
    categories = [ "config-link" ];
    reason = "R2: the exact-line ignore rule for the old generated config symlink is kept until the follow-up retires it";
  }
  {
    path = "flake-modules/pre-commit.nix";
    categories = [
      "config-link"
      "githooks-ref"
    ];
    reason = "R2: the gitignore check for the old generated symlink, and its error text, are kept until the follow-up retires them; the driftGuard option description names the conditions it detects";
  }
  {
    path = "CLAUDE.md";
    categories = [ "config-link" ];
    reason = "current-truth rule text: an old config symlink reads as missing and is never read, linked or regenerated";
  }
  {
    path = "docs/hooks.md";
    categories = [
      "config-link"
      "githooks-ref"
    ];
    reason = "current-truth reference: it documents the drift guard, so it names the removed hook directory, and states the old config symlink is not a bundle and must never be linked, copied or regenerated";
  }

  # ----- the guard itself: its patterns and its planted-violation tests -----
  {
    path = "modules/pg-hooks/pg-hooks-drift-guard/*";
    categories = [ "*" ];
    reason = "the drift guard names every pattern it detects, and its bats tests plant each violation on purpose";
  }
  {
    path = "modules/pn/internal/workspace/doctor_checks_hookdrift*.go";
    categories = [ "*" ];
    reason = "the pn doctor drift rows name the conditions they detect, and their tests plant each one on purpose";
  }

  # ----- fixtures that prove behavior on a bad setup -----
  {
    path = "modules/pg-hooks/lib/tests/test-pg-hooks-lib.bats";
    categories = [
      "githooks-ref"
      "relative-hooks-path"
    ];
    reason = "fixture: a local relative core.hooksPath is the input that proves the unreachable message";
  }
  {
    path = "modules/pg-hooks/pg-hooks/tests/test-pg-hooks.bats";
    categories = [
      "githooks-ref"
      "relative-hooks-path"
    ];
    reason = "fixture: a local relative core.hooksPath is the input that proves status unreachable (exit 15)";
  }
  {
    path = "modules/pn/internal/workspace/hookbundle_test.go";
    categories = [ "githooks-ref" ];
    reason = "fixture: a local relative core.hooksPath is the input that proves the unreachable state";
  }
  {
    path = "modules/pn/internal/workspace/doctor_checks_hookbundle_test.go";
    categories = [
      "githooks-ref"
      "relative-hooks-path"
    ];
    reason = "fixture: a local relative core.hooksPath is the input that proves the unreachable doctor finding";
  }
  {
    path = "modules/pn/internal/workspace/smoke/smoke_hook_bundle.go";
    categories = [ "githooks-ref" ];
    reason = "the smoke scenario asserts that nothing is written into a working tree, naming the removed directory it must not find";
  }
  {
    path = "modules/pn/internal/workspace/hookbundle_gate_test.go";
    categories = [ "config-link" ];
    reason = "test input: an old config symlink proves the install gate ignores it (R4)";
  }
  {
    path = "modules/pn/internal/workspace/nix_hooks_test.go";
    categories = [ "config-link" ];
    reason = "test input: an old config symlink proves the install gate ignores it (R4)";
  }
  {
    path = "modules/pn/internal/workspace/update_worktree_test.go";
    categories = [
      "config-link"
      "removed-machinery"
    ];
    reason = "test asserts the update worktree gets no config link; its comment names the removed bypass";
  }
  {
    path = "modules/pn/internal/workspace/propagate_test.go";
    categories = [ "removed-machinery" ];
    reason = "test asserts the removed config bypass no longer exists";
  }
  {
    path = "lib/pg-hooks-bundle-tests.nix";
    categories = [ "removed-machinery" ];
    reason = "eval test asserting the removed shim option is gone";
  }

  # ----- comments that deliberately record removed or kept behavior -----
  {
    path = "modules/pn/internal/workspace/propagate.go";
    categories = [ "removed-machinery" ];
    reason = "comment recording that the config bypass was removed with the root-cause fix, so it is not reintroduced";
  }
  {
    path = "modules/pn/internal/workspace/doctor_checks_hookbundle.go";
    categories = [ "config-link" ];
    reason = "comment: a clone holding only an old config symlink is reported as having no bundle (R4)";
  }
  {
    path = "modules/pn/internal/workspace/doctor_checks_precommithook.go";
    categories = [ "config-link" ];
    reason = "comment: a clone holding only an old config symlink is reported as having no bundle (R4)";
  }
  {
    path = "modules/pn/internal/workspace/hookbundle.go";
    categories = [ "config-link" ];
    reason = "comment: an old config symlink also reads missing";
  }
  {
    path = "modules/pn/internal/workspace/nix_hooks.go";
    categories = [ "config-link" ];
    reason = "comment: an old config symlink reads as missing (R4)";
  }
]
