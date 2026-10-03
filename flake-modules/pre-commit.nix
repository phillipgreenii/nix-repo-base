# Light-upstream module: closes over the producer's git-hooks input.
# IMPORTS flake-modules/treefmt.nix because the pre-commit treefmt hook
# needs the formatter wrapper. Consumers who import pre-commit get treefmt
# automatically; they do NOT need to import treefmt separately.
producerInputs:
{
  lib,
  config,
  inputs,
  ...
}:
let
  topLevelCfg = config.phillipgreenii.pre-commit;

  # One fixer (spec 5.1/5.2). Deliberately a strict submodule: a `stages` key (a
  # fixer attached to a prek stage) is an unknown option and fails evaluation.
  fixerType = lib.types.submodule {
    options = {
      name = lib.mkOption {
        type = lib.types.str;
        description = "Unique fixer name; also the anchor name for `after`/`before`.";
      };
      command = lib.mkOption {
        type = lib.types.str;
        description = "Command (store paths allowed) the staged file paths are appended to.";
      };
      mode = lib.mkOption {
        type = lib.types.enum [
          "files"
          "per-file"
        ];
        default = "files";
        description = "`files`: one invocation per batch of files; `per-file`: one per file.";
      };
      includes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Globs a staged file must match (empty: every file).";
      };
      excludes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Globs a staged file must not match.";
      };
      after = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Insert directly after the fixer with this name.";
      };
      before = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Insert directly before the fixer with this name.";
      };
    };
  };

  # One drift-guard allowlist entry (pg-hooks-drift-guard): every field is
  # mandatory, so an exception always says where, what and why.
  driftCategories = [
    "githooks-dir"
    "githooks-ref"
    "relative-hooks-path"
    "config-link"
    "removed-machinery"
  ];
  driftAllowType = lib.types.submodule {
    options = {
      path = lib.mkOption {
        type = lib.types.str;
        description = ''
          Glob matched against the repo-relative path (`case` pattern: `*` also
          crosses `/`, so `docs/frozen/*` covers a whole tree).
        '';
      };
      categories = lib.mkOption {
        type = lib.types.nonEmptyListOf (lib.types.enum ([ "*" ] ++ driftCategories));
        description = "Categories the entry allows in the matching files; `*` allows all.";
      };
      reason = lib.mkOption {
        type = lib.types.strMatching "[^\t\n]+";
        description = "Why the hit is intentional (one line, no tab).";
      };
    };
  };
in
{
  imports = [ (import ./treefmt.nix producerInputs) ];

  options.phillipgreenii.pre-commit = {
    src = lib.mkOption {
      type = lib.types.path;
      default = inputs.self.outPath;
      defaultText = lib.literalExpression "inputs.self";
      description = ''
        Source path passed to git-hooks for hook registration. Defaults to the
        consumer's flake root; rarely needs overriding.
      '';
    };
    extraHooks = lib.mkOption {
      type = lib.types.either (lib.types.attrsOf lib.types.anything) (
        lib.types.functionTo (lib.types.attrsOf lib.types.anything)
      );
      default = { };
      description = ''
        Additional hooks merged into the standard set. Accepts either an
        attrset of hooks, or a function `pkgs -> attrset` that is applied with
        the per-system `pkgs` inside this module's `perSystem`. The function
        form lets hook `entry` store paths (e.g. host-native `go` /
        `golangci-lint`) follow the building/committing system instead of a
        single statically pinned system — so the committing machine can build
        the hook tooling for its own platform. See phillipgreenii-nix-agent-support
        for a function-form example.
      '';
    };
    excludes = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "^_sources/" ];
      description = ''
        File patterns (git-hooks/pre-commit regexes) excluded from ALL hooks
        (deadnix, end-of-file-fixer, trailing-whitespace, shellcheck, etc.).

        Defaults to nvfetcher's generated `_sources/` tree: those files are
        tool-generated and regenerated, so formatting/linting them is both wrong
        and unstable. The producer itself has no `_sources/`, so the default is a
        harmless no-op here while giving every nvfetcher-using consumer correct
        behaviour with zero per-repo config. Consumers can extend this list for
        other generated/vendored paths; definitions concatenate.
      '';
    };
    fixers = lib.mkOption {
      type = lib.types.listOf fixerType;
      default = [ ];
      example = lib.literalExpression ''
        [
          {
            name = "sort-claude-permissions";
            command = "''${pkgs.sort-claude-permissions}/bin/sort-claude-permissions";
            includes = [ ".claude/settings.json" ];
            after = "statix";
          }
        ]
      '';
      description = ''
        Fixers that `pg-hooks fix` (alias `pre-commit-fix`) runs over the staged files,
        in addition to the shared defaults (treefmt, statix once per `*.nix` file,
        treefmt again, trailing-whitespace, end-of-file-fixer). A fixer without
        `after`/`before` is appended after the defaults; with an anchor it is inserted
        directly after/before the named fixer (anchors MAY name another added fixer).
        Fixers are NOT prek hooks: they never run from a git hook (HK-2), a fixer MUST
        NOT carry a stage, and no prek hook's `entry` may invoke `pg-hooks fix` or
        `pre-commit-fix` (evaluation fails, only when `bundle.enable`). `mode = "per-file"`
        runs the command once per file (e.g. `statix fix`, which takes one target).
        Resolved into the bundle's `fixers.json` (a flat ordered list) at evaluation time.
      '';
    };
    stampPaths = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Repo-relative paths, in addition to `flake.lock` and `flake.nix`, whose tracked
        content is hashed (`git ls-files -s -- <paths> | git hash-object --stdin`) into
        the hook bundle's staleness stamp. Needed only where hook definitions live in
        the repo itself rather than arriving through `flake.lock` (repo-base).
        Recorded in the bundle's `meta.json`.
      '';
    };
    bundle.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Render the per-clone hook bundle (`packages.<system>.pg-hooks-bundle`: prek,
        prek config, fixers, stage list, runner and library) and make
        `install-pre-commit-hooks` root it under `<git-common-dir>/pg-hooks/` (spec
        docs/superpowers/specs/2026-10-01-per-clone-hook-bundle-design.md, sections
        4.2 and 5.1; ADR 0032). Defaults to `true`.

        Setting it to `false` is an opt-out: NO hooks are installed. The
        `pg-hooks-bundle` package is not exposed, the devShell installs nothing, and
        `install-pre-commit-hooks` prints a notice and exits 0. The derivation stays
        available as `legacyPackages.<system>.pgHooksBundle` either way.
      '';
    };
  };

  options.phillipgreenii.pre-commit.driftGuard = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Contribute the `hook-drift-guard` flake check (plan Task 22, design spec
        section 8 "drift guard"): it fails when the repo's source contains a
        `.githooks` directory, text naming `.githooks`, a relative `core.hooksPath`,
        link code or text for the old generated `.pre-commit-config.yaml`, or an
        identifier of the deleted shim and link code, anywhere outside
        `driftGuard.allowlist`. Opt-in (default `false`) so relocking onto this
        module never turns a consumer's checks red; a consumer enables it once its
        allowlist is written.
      '';
    };
    allowlist = lib.mkOption {
      type = lib.types.listOf driftAllowType;
      default = [ ];
      example = lib.literalExpression ''
        [
          {
            path = "docs/adr/0016-*";
            categories = [ "config-link" ];
            reason = "ADR text records the old symlink";
          }
        ]
      '';
      description = ''
        Explicit exceptions to the drift guard, each a path glob, the categories it
        allows (`githooks-dir`, `githooks-ref`, `relative-hooks-path`, `config-link`,
        `removed-machinery`, or `*`) and a reason. The guard's patterns are never
        loosened; an exception is always data here. Definitions concatenate.
      '';
    };
  };

  config.perSystem =
    {
      config,
      pkgs,
      system,
      inputs,
      ...
    }:
    let
      # Resolve the function-or-attrset extraHooks against the per-system pkgs so
      # a function-form definition (pkgs -> hooks) picks up the building system's
      # tooling. An attrset-form definition passes through unchanged.
      resolvedExtraHooks =
        if lib.isFunction topLevelCfg.extraHooks then
          topLevelCfg.extraHooks pkgs
        else
          topLevelCfg.extraHooks;

      # pg-git-check-identity (modules/pg-git-check-identity): rejects a
      # commit whose author or committer identity looks like a test/
      # placeholder account (e.g. "Test User <x@example.com>") -- the exact
      # incident that motivated adding it to the BASE hook set rather than
      # relying solely on phillipgreenii-nix-personal's global, per-user
      # `programs.git.hooks.pre-commit`: that global hook sets git's
      # *global* `core.hooksPath`, which git's own local > global > system
      # precedence means is SILENTLY SHADOWED in every repo that has ever
      # run `prek install`/entered this module's own devShell -- i.e. every
      # repo that imports this exact flakeModule, including this one. Built
      # SELF-CONTAINED here (its own bashBuilders instantiation, not a
      # reference to `pkgs.pg-git-check-identity`) deliberately: a
      # consumer's own perSystem `pkgs` is not guaranteed to have this
      # repo's `overlays.default` applied (this repo's OWN perSystem `pkgs`
      # doesn't either -- see flake.nix's `pgTestRunnerScripts` et al., the
      # same reasoning), so the hook's `entry` must not depend on it.
      # `inputs.self` here is whichever flake is CONSUMING this module (not
      # necessarily this repo) -- harmless, since mkBashScript's `self` arg
      # is retained only for a version stamp mkBashScript itself no longer
      # uses (see lib/bash-builders.nix's own header comment).
      pgGitCheckIdentityBashBuilders = (import ../nix/packages.nix { }).mkBashBuilders {
        inherit pkgs;
        inherit (pkgs) lib;
        inherit (inputs) self;
      };
      pgGitCheckIdentityScripts = import ../modules/pg-git-check-identity/scripts.nix {
        inherit pkgs;
        bashBuilders = pgGitCheckIdentityBashBuilders;
      };
      # The base fixer hooks' entries, shared with the bundle's default fixers
      # (fixers.json) so `pg-hooks fix` and the prek check hooks run the same tools.
      trailingWhitespaceEntry = "${pkgs.python3Packages.pre-commit-hooks}/bin/trailing-whitespace-fixer";
      endOfFileFixerEntry = "${pkgs.python3Packages.pre-commit-hooks}/bin/end-of-file-fixer";
      preCommit = producerInputs.git-hooks.lib.${system}.run {
        # `excludes` becomes a top-level pre-commit `exclude` regex applied to
        # every hook (git-hooks modules/pre-commit.nix). Single source of truth
        # for generated-path exclusion — see the option doc above.
        inherit (topLevelCfg) src excludes;
        package = pkgs.prek;
        tools.dotnet-sdk = pkgs.runCommand "dotnet-stub" { } "mkdir $out";
        hooks = {
          treefmt = {
            enable = true;
            package = config.treefmt.build.wrapper;
          };
          statix = {
            enable = true;
            name = "statix";
          };
          deadnix = {
            enable = true;
            name = "deadnix";
          };
          # Severity matches the treefmt shellcheck formatter and
          # checksHelpers.shellcheck (all three = warning) so a single, consistent
          # policy governs shellcheck everywhere. error was too lenient (let
          # info/style findings pass the hook but fail `nix flake check`); style
          # was too strict (info-level false positives: bats subshell SC2030/2031,
          # source-following SC1091, indirectly-invoked SC2329). See tc-neh26.
          shellcheck = {
            enable = true;
            name = "shellcheck";
            args = [ "--severity=warning" ];
          };
          check-merge-conflicts.enable = true;
          trailing-whitespace = {
            enable = true;
            entry = trailingWhitespaceEntry;
          };
          end-of-file-fixer = {
            enable = true;
            entry = endOfFileFixerEntry;
          };
          check-case-conflicts.enable = true;
          # Rejects a commit whose author or committer identity looks like a
          # test/placeholder account (see pgGitCheckIdentityScripts above for
          # the full rationale). Skips itself inside the nix build sandbox
          # (checks.pre-commit) -- mirroring phillipgreenii-nix-personal's
          # own `run-unit-tests` extraHooks entry -- since there is no real
          # committer identity to protect in that throwaway sandbox repo,
          # and this avoids depending on whatever synthetic identity
          # git-hooks.nix's own sandbox setup happens to use.
          check-git-identity = {
            enable = true;
            name = "check-git-identity";
            entry = "${pkgs.writeShellScript "check-git-identity" ''
              set -euo pipefail
              if [ -n "''${IN_NIX_BUILD:-}" ] || [ -n "''${NIX_BUILD_TOP:-}" ]; then
                echo "check-git-identity: inside the nix build sandbox (checks.pre-commit) -- skipping." >&2
                exit 0
              fi
              exec ${pgGitCheckIdentityScripts.pg-git-check-identity.script}/bin/pg-git-check-identity
            ''}";
            pass_filenames = false;
          };
          # NOTE: Go linting is intentionally NOT a pre-commit hook. golangci-lint
          # must load the full package graph, which cannot be done offline in the
          # no-network `nix flake check` sandbox that runs checks.pre-commit
          # (bead pg2-6wly). It is instead a dedicated, sandbox-safe check per Go
          # module (checks.<module>-golangci) via gomod2nix's vendored dep env —
          # see lib/go-builders.nix `mkGoLint`.
          #
          # AMENDED HK-2 (design spec
          # docs/superpowers/specs/2026-08-24-pg-test-runner-design.md, section 5,
          # superseding the git-hook speed evaluation doc's blanket "no git hook
          # may perform thorough verification" for the label-`unit` tier): a git
          # hook MAY run label-`unit` tests directly via `pg-test-runner`. A git
          # hook MUST NOT invoke nix (build, develop, run, or flake evaluation) or
          # run any non-unit test kind -- REGARDLESS of which stage it runs at.
          # Staging a `nix build`/`nix run` hook at `stages = [ "pre-push" ]`
          # merely moves it outside the sandboxed `checks.pre-commit` run; it does
          # NOT stop it from being a nix-invoking git hook, and that loophole is
          # exactly what this amendment closes. (repo-base's own former
          # `golangci-lint-prepush` `extraHooks` entry did precisely this -- a
          # pre-push hook that shelled out to `nix build` -- and was removed for
          # it; bead pg2-lxz3o.) A repo wanting local commit-time Go feedback
          # short of the full `nix flake check` should add a `pg-test-runner`-based
          # `extraHooks` entry instead (see repo-base's own `run-unit-tests` hook,
          # top-level in this repo's flake.nix, for the shape) -- never one that
          # invokes nix.
          #
          # HK-2 STANDS (ADR 0032): the commit-time shim experiment that tested a
          # nix-invoking git hook (ADR 0029, bead pg2-z19ad) was abandoned, and its
          # shim option removed. No `extraHooks` entry may invoke nix.
        }
        // resolvedExtraHooks;
      };

      # ADR 0016 (generation mechanism superseded by ADR 0032; the gitignore rule
      # is KEPT for now because old clones and worktrees still hold the symlink and
      # the follow-up bead that retires this check runs only after every clone has
      # dropped it): a `.pre-commit-config.yaml` symlink into `/nix/store` MUST NOT
      # be committed — a committed store path is GC-eligible and rots into a
      # dangling symlink. Enforce that every consumer gitignores it. Pure eval-time
      # read of the flake source's `.gitignore` (no IFD: `src` is an
      # already-realised store path); an exact full-line match avoids matching
      # the explanatory comment line.
      gitignorePath = topLevelCfg.src + "/.gitignore";
      gitignoreLines =
        if builtins.pathExists gitignorePath then
          lib.splitString "\n" (builtins.readFile gitignorePath)
        else
          null;
      ignoresPreCommitConfig =
        gitignoreLines != null
        && lib.any (l: lib.removeSuffix "\r" l == ".pre-commit-config.yaml") gitignoreLines;
      preCommitConfigGitignoredCheck =
        if gitignoreLines == null then
          throw "phillipgreenii.pre-commit: ${toString topLevelCfg.src}/.gitignore is missing; it MUST exist and ignore the generated .pre-commit-config.yaml store-symlink (ADR 0016 in phillipg-nix-repo-base)."
        else if !ignoresPreCommitConfig then
          throw "phillipgreenii.pre-commit: .gitignore MUST contain a line '.pre-commit-config.yaml'. The git-hooks.nix config is a generated /nix/store symlink and must not be committed (ADR 0016 in phillipg-nix-repo-base)."
        else
          pkgs.runCommand "pre-commit-config-gitignored" { } "touch $out";

      # ----- Hook drift guard (plan Task 22, bead pg2-pla9d.25) -----
      # Opt-in: see `driftGuard.enable`. The allowlist is rendered to the guard's
      # tab-separated format and kept OUT of the scanned tree (it is passed by path),
      # so the reasons it records never trip the patterns themselves.
      driftCfg = topLevelCfg.driftGuard;
      driftAllowlistTsv = lib.concatMapStrings (
        e: "${e.path}\t${lib.concatStringsSep "," e.categories}\t${e.reason}\n"
      ) driftCfg.allowlist;
      hookDriftGuardCheck =
        pkgs.runCommand "hook-drift-guard"
          {
            nativeBuildInputs = [ pgHooksScripts.pg-hooks-drift-guard.script ];
            inherit driftAllowlistTsv;
            passAsFile = [ "driftAllowlistTsv" ];
          }
          ''
            pg-hooks-drift-guard --root ${topLevelCfg.src} --allowlist "$driftAllowlistTsvPath"
            touch $out
          '';

      # ----- Per-clone hook bundle (spec 4.2, 5.1, 5.2; bead pg2-pla9d.7) -----

      bundleCfg = topLevelCfg.bundle;

      # pg-hooks scripts, built SELF-CONTAINED here exactly like
      # pgGitCheckIdentityScripts above: a consumer's perSystem `pkgs` lacks this
      # repo's overlay, so the bundle must not reach for `pkgs.pg-hooks-run`.
      pgHooksBashBuilders = (import ../nix/packages.nix { }).mkBashBuilders {
        inherit pkgs;
        inherit (pkgs) lib;
        inherit (inputs) self;
      };
      pgHooksScripts = import ../modules/pg-hooks/scripts.nix {
        inherit pkgs;
        bashBuilders = pgHooksBashBuilders;
      };

      enabledHooks = lib.filterAttrs (_: hook: hook.enable) preCommit.config.hooks;

      # stages.json: `default_stages` UNION every enabled hook's `stages` (the set
      # of stages the rendered config uses), each stage mapped to the hooks that run
      # at it. Entry shape
      # `{ id, reason }` is what modules/pg-hooks/pg-hooks reads for `list` and
      # `explain`; `reason` is the hook's `description` (first line) or its id.
      stageNames = lib.sort lib.lessThan (
        lib.unique (
          preCommit.config.default_stages ++ lib.concatMap (hook: hook.stages) (lib.attrValues enabledHooks)
        )
      );
      hookReason =
        id: hook:
        let
          firstLine = builtins.head (lib.splitString "\n" hook.description);
        in
        if firstLine != "" then firstLine else id;
      stagesData = lib.genAttrs stageNames (
        stage:
        lib.mapAttrsToList (id: hook: {
          inherit id;
          reason = hookReason id hook;
        }) (lib.filterAttrs (_: hook: builtins.elem stage hook.stages) enabledHooks)
      );

      # fixers.json: the shared default order, then the repo's `fixers` resolved
      # against it by `after`/`before` anchors into ONE flat ordered list. A fixer
      # without an anchor is appended after the defaults; several fixers on the same
      # anchor keep their declared order; anchors may name another added fixer.
      defaultFixers =
        map
          (
            fixer:
            {
              mode = "files";
              includes = [ ];
              excludes = [ ];
              after = null;
              before = null;
            }
            // fixer
          )
          [
            {
              name = "treefmt";
              command = "${config.treefmt.build.wrapper}/bin/treefmt";
            }
            {
              # `statix fix` takes ONE target, hence per-file.
              name = "statix";
              command = "${pkgs.statix}/bin/statix fix";
              mode = "per-file";
              includes = [ "*.nix" ];
            }
            {
              name = "treefmt-final";
              command = "${config.treefmt.build.wrapper}/bin/treefmt";
            }
            {
              name = "trailing-whitespace";
              command = trailingWhitespaceEntry;
            }
            {
              name = "end-of-file-fixer";
              command = endOfFileFixerEntry;
            }
          ];
      addedFixers = topLevelCfg.fixers;
      allFixers = defaultFixers ++ addedFixers;
      afterOf = name: lib.filter (fixer: fixer.after == name) addedFixers;
      beforeOf = name: lib.filter (fixer: fixer.before == name) addedFixers;
      expandFixer =
        fixer:
        lib.concatMap expandFixer (beforeOf fixer.name)
        ++ [ fixer ]
        ++ lib.concatMap expandFixer (afterOf fixer.name);
      resolvedFixers = lib.concatMap expandFixer (
        defaultFixers ++ lib.filter (fixer: fixer.after == null && fixer.before == null) addedFixers
      );
      fixersData = map (fixer: {
        inherit (fixer)
          name
          command
          mode
          includes
          excludes
          ;
      }) resolvedFixers;

      metaData = {
        # Empty: the runner and CLI fall back to the canonical clone's basename.
        repo = "";
        inherit (topLevelCfg) stampPaths;
        stages = stageNames;
      };

      fixerNames = map (fixer: fixer.name) allFixers;
      invokesFix =
        entry: builtins.length (builtins.split "pg-hooks[[:space:]]+fix|pre-commit-fix" entry) > 1;
      # Evaluation-time assertions (spec 5.1). The fixer and hook checks apply only
      # with `bundle.enable`, so a repo that opted out evaluates without them. An unknown
      # stage name is rejected by git-hooks.nix's own `stages` enum type, which the
      # stage-list computation above forces; a fixer carrying a `stages` key is
      # rejected by the strict `fixerType` submodule.
      bundleFailures = lib.optionals bundleCfg.enable (
        lib.optional (lib.length (lib.unique fixerNames) != lib.length fixerNames)
          "fixer names MUST be unique (anchors address them by name); got: ${lib.concatStringsSep ", " fixerNames}."
        ++ lib.concatMap (
          fixer:
          lib.optional (
            fixer.after != null && fixer.before != null
          ) "fixer '${fixer.name}' sets both after and before; set exactly one."
          ++
            lib.concatMap
              (
                anchor:
                lib.optional (
                  anchor != null && !(lib.elem anchor fixerNames)
                ) "fixer '${fixer.name}' anchors on unknown fixer '${anchor}'."
              )
              [
                fixer.after
                fixer.before
              ]
        ) addedFixers
        ++ lib.optional (
          lib.length resolvedFixers != lib.length allFixers
        ) "fixer anchors do not resolve (a cycle, or an anchor chain that never reaches a default fixer)."
        ++ lib.mapAttrsToList (
          id: _:
          "prek hook '${id}' invokes pg-hooks fix / pre-commit-fix; fixers are never run from a hook (use `fixers`)."
        ) (lib.filterAttrs (_: hook: invokesFix hook.entry) enabledHooks)
      );
      guard =
        value:
        if bundleFailures == [ ] then
          value
        else
          throw "phillipgreenii.pre-commit:\n  - ${lib.concatStringsSep "\n  - " bundleFailures}";

      pgHooksBundle =
        pkgs.runCommand "pg-hooks-bundle"
          {
            nativeBuildInputs = [ pkgs.jq ];
            # Evaluation-time view of the same data, for lib/pg-hooks-bundle-tests.nix.
            passthru.bundleData = {
              fixers = fixersData;
              stages = stagesData;
              meta = metaData;
            };
            stagesJson = builtins.toJSON stagesData;
            fixersJson = builtins.toJSON fixersData;
            metaJson = builtins.toJSON metaData;
            passAsFile = [
              "stagesJson"
              "fixersJson"
              "metaJson"
            ];
          }
          ''
            mkdir -p $out/bin $out/lib
            ln -s ${pkgs.prek}/bin/prek $out/bin/prek
            ln -s ${pgHooksScripts.pg-hooks-run.script}/bin/pg-hooks-run $out/bin/pg-hooks-run
            cp ${pgHooksScripts.libDir}/pg-hooks-lib.bash $out/lib/pg-hooks-lib.bash
            # git-hooks.nix writes a leading "# DO NOT MODIFY" comment block; strip it
            # so the file is plain JSON. The store paths in its hook entries stay in
            # the text, which is what roots their closure.
            sed '/^#/d' ${preCommit.config.configFile} >$out/prek-config.json
            jq empty $out/prek-config.json
            jq . "$stagesJsonPath" >$out/stages.json
            jq . "$fixersJsonPath" >$out/fixers.json
            jq . "$metaJsonPath" >$out/meta.json
          '';
    in
    {
      # Kept for `flake-modules/devshell.nix` (and any consumer that destructures
      # it): the devShell MUST NOT install anything (spec 4.4; the installer is the
      # only writer), so the hook is always empty.
      _module.args.preCommitShellHook = guard "";
      # Exposed whether or not bundle.enable is set, so a check can build the real
      # bundle regardless of the opt-out (Task 5's real-tools check).
      legacyPackages.pgHooksBundle = guard pgHooksBundle;
      checks = {
        pre-commit = guard preCommit;
        pre-commit-config-gitignored = preCommitConfigGitignoredCheck;
      }
      // lib.optionalAttrs driftCfg.enable {
        hook-drift-guard = hookDriftGuardCheck;
      };
      packages = {
        install-pre-commit-hooks = guard (
          pkgs.writeShellScriptBin "install-pre-commit-hooks" (
            if bundleCfg.enable then
              # Spec 4.4: the script text embeds the bundle's store path, so
              # `nix run [--override-input ...] .#install-pre-commit-hooks`
              # realises the bundle with the same overrides; the installer only
              # roots it (nix-store --add-root) and never calls `nix build`. No
              # core.hooksPath write.
              ''
                exec ${pgHooksScripts.pg-hooks-install.script}/bin/pg-hooks-install --bundle ${pgHooksBundle} "$@"
              ''
            else
              # Opt-out (`bundle.enable = false`, ADR 0032): no hooks are installed.
              ''
                echo "install-pre-commit-hooks: phillipgreenii.pre-commit.bundle.enable is false in this flake; no hooks installed." >&2
                exit 0
              ''
          )
        );

        # Autofix helper for the `statix` pre-commit hook. Runs `statix fix` over
        # the CURRENT working directory (or the paths given as args) — NOT ${./.},
        # which resolves to a read-only /nix/store copy statix can never write to.
        # Auto-contributed to every consumer that imports this flakeModule, so the
        # six hand-rolled per-repo copies (five of them broken with the ${./.} bug)
        # are deleted in favour of this single source of truth (bead pg2-7vhvn).
        fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
          exec ${pkgs.lib.getExe pkgs.statix} fix "''${@:-.}"
        '';
      }
      // lib.optionalAttrs bundleCfg.enable {
        pg-hooks-bundle = guard pgHooksBundle;
      };
    };
}
