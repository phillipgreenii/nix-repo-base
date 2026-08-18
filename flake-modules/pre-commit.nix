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
  };

  config.perSystem =
    {
      config,
      pkgs,
      system,
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
            entry = "${pkgs.python3Packages.pre-commit-hooks}/bin/trailing-whitespace-fixer";
          };
          end-of-file-fixer = {
            enable = true;
            entry = "${pkgs.python3Packages.pre-commit-hooks}/bin/end-of-file-fixer";
          };
          check-case-conflicts.enable = true;
          # NOTE: Go linting is intentionally NOT a pre-commit hook. golangci-lint
          # must load the full package graph, which cannot be done offline in the
          # no-network `nix flake check` sandbox that runs checks.pre-commit
          # (bead pg2-6wly). It is instead a dedicated, sandbox-safe check per Go
          # module (checks.<module>-golangci) via gomod2nix's vendored dep env —
          # see lib/go-builders.nix `mkGoLint`. Repos wanting local commit/push-time
          # Go lint feedback can add their own hook via `extraHooks` (e.g. at
          # stages = [ "pre-push" ] to keep it out of the sandboxed check).
        }
        // resolvedExtraHooks;
      };

      # ADR 0016: the git-hooks.nix-generated `.pre-commit-config.yaml` is a
      # symlink into `/nix/store` and MUST NOT be committed — a committed
      # store path is GC-eligible and rots into a dangling symlink, breaking
      # the hook. Enforce that every consumer gitignores it. Pure eval-time
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

      # pg2-vqyw3: harden the generated pre-push hook against a MISSING config.
      # git-hooks.nix's installer (the `preCommit.shellHook` above) hardcodes
      # `prek install -c .pre-commit-config.yaml -t pre-push` with NO
      # `--allow-missing-config`, so the shim it writes runs
      # `prek hook-impl … --config=.pre-commit-config.yaml` and ABORTS every push
      # with "config file not found" whenever that config is absent — which is
      # ALWAYS true in a fresh checkout/worktree, because the config is the
      # gitignored, nix-generated /nix/store symlink (ADR 0016) that no commit
      # carries. That breaks pushes from pn's temp worktrees (pg2-x42j3).
      #
      # The framework exposes no option to thread install flags, so AFTER its
      # installer runs we RE-INSTALL just the pre-push shim with
      # `--allow-missing-config`, which bakes `--skip-on-missing-config` into it.
      # That flag ONLY no-ops the config-ABSENT branch; with a config present the
      # shim still runs every hook exactly as before (enforcement intact) —
      # unlike `--no-verify`. Defense-in-depth complementing pg2-m75sq.
      #
      # Regeneration-safe: it WRAPS the framework's opaque shellHook instead of
      # editing it, keys off the observable shim file (not the framework's
      # command text), and is idempotent — the grep guard skips the re-install
      # (and its filesystem churn under lorri/direnv) once the shim is already
      # tolerant, and the whole block no-ops when hook install is disabled or
      # pre-push is not a configured stage (no shim exists to harden).
      hardenPrePushHook = ''
        if ${lib.getExe' pkgs.git "git"} rev-parse --git-dir >/dev/null 2>&1; then
          _pgii_prepush="$(${lib.getExe' pkgs.git "git"} rev-parse --path-format=absolute --git-path hooks/pre-push 2>/dev/null || true)"
          if [ -n "$_pgii_prepush" ] && [ -e "$_pgii_prepush" ] \
            && ! grep -q -- '--skip-on-missing-config' "$_pgii_prepush"; then
            ${lib.getExe pkgs.prek} install -c .pre-commit-config.yaml --allow-missing-config -t pre-push -f
          fi
          unset _pgii_prepush
        fi
      '';
      # pg2-ohng1: undo the framework installer's RELATIVE `core.hooksPath`, which
      # silently UNGATES every linked worktree of the repo.
      #
      # git-hooks.nix's installer (the `preCommit.shellHook` above) ENDS by writing
      # `core.hooksPath`, and deliberately relativizes it first — at the locked rev
      # (cachix/git-hooks.nix 43b3c1ab) that is `modules/pre-commit.nix:540-546`:
      #
      #   GIT_WC=`git rev-parse --show-toplevel`                               # :483
      #   common_dir=$(git rev-parse --path-format=absolute --git-common-dir)   # :541
      #   common_dir=<"$GIT_WC/" prefix stripped>                # :544 -> RELATIVE
      #   git config --local core.hooksPath "$common_dir/hooks"                 # :546
      #
      # so the value written depends on the installer's CWD: from the CANONICAL clone
      # `--git-common-dir` is `<repo>/.git`, the prefix strips, and it writes the
      # relative `.git/hooks`; from a LINKED WORKTREE the common dir is the canonical
      # clone's, the prefix does not match, and the value stays absolute. Per ADR 0019
      # the workspace's per-repo event hooks run `install-pre-commit-hooks` with
      # cwd == the CANONICAL repo (post-clone / -rebase / -update / -upgrade), so the
      # relative form is written again and again — and with `extensions.worktreeConfig`
      # unset, `--local` is the ONE `.git/config` that the canonical clone and every
      # linked worktree of it SHARE.
      #
      # A relative value is a SILENT gate failure in a linked worktree: git resolves
      # it against that worktree's top level, where `.git` is a FILE (a gitdir
      # pointer), so `.git/hooks` is not a directory — git then skips EVERY hook with
      # no warning and exit 0, and a commit appears to pass a gate that never ran.
      # (Seen twice in this workspace, on support-apps: pg2-bdq2m, pg2-qxi4e.)
      #
      # The correction is to UNSET the key, not to rewrite it absolute: with it unset
      # git resolves the default hooks dir against the COMMON dir, so the canonical
      # clone AND every linked worktree resolve `git rev-parse --git-path hooks` to
      # `<canonical>/.git/hooks` — worktree-correct by construction, with no
      # machine-specific absolute path baked into a shared, untracked config.
      # `lib/scripts/update-locks-lib.bash:399-406` already documents exactly that
      # resolution, and is this repo's only in-tree READER of `core.hooksPath`; it
      # reads it as `git rev-parse --path-format=absolute --git-path hooks`, which is
      # correct for the unset case (bead pg2-rltuo landed that). Upstream itself
      # unsets the key just before installing the hooks (its line 511) — it merely
      # re-adds the relativized value afterwards.
      #
      # Regeneration-safe and churn-free, same shape as `hardenPrePushHook`: it WRAPS
      # the framework's opaque shellHook instead of editing it, keys off the
      # OBSERVABLE config value rather than the framework's command text, and honours
      # upstream's "compare before it writes" rule (its line 484). It corrects ONLY
      # the defect — a NON-ABSOLUTE value — so an absolute value is left alone,
      # whether deliberate or written by an installer run from a worktree. That is
      # also what makes it idempotent: every state it can leave behind (unset, or
      # absolute) is a state it will not write to again, so a repeat run performs no
      # config write at all. The `rev-parse --git-dir` guard preserves upstream's
      # not-a-git-repo skip (its line 480). The post-unset re-check covers the one
      # case where unsetting does NOT yield the default — a higher-scope
      # (`--global` / `--system` / include) `core.hooksPath` taking over — by writing
      # the absolute common hooks dir back as a local override. Scoped to `--local`
      # throughout; never `--global`, never `--system`.
      correctRelativeHooksPath = ''
        _pgii_git="${lib.getExe' pkgs.git "git"}"
        if "$_pgii_git" rev-parse --git-dir >/dev/null 2>&1; then
          _pgii_hooks_path="$("$_pgii_git" config --local --get core.hooksPath 2>/dev/null || true)"
          case "$_pgii_hooks_path" in
            "" | /*) ;;
            *)
              _pgii_common_hooks="$("$_pgii_git" rev-parse --path-format=absolute --git-common-dir)/hooks"
              "$_pgii_git" config --local --unset-all core.hooksPath || true
              if [ "$("$_pgii_git" rev-parse --path-format=absolute --git-path hooks)" \
                != "$_pgii_common_hooks" ]; then
                "$_pgii_git" config --local core.hooksPath "$_pgii_common_hooks"
              fi
              unset _pgii_common_hooks
              ;;
          esac
          unset _pgii_hooks_path
        fi
        unset _pgii_git
      '';
      # pg2-7g7yl: eliminate the LAST per-worktree dependency in hook
      # invocation — the shim's `--config=` argument.
      #
      # git-hooks.nix's installer (`installationScript`, the same function that
      # is `preCommit.shellHook` above; cachix/git-hooks.nix 43b3c1ab
      # modules/pre-commit.nix:524,527,537) always calls
      # `prek install -c ${cfg.configPath}` where `cfg.configPath` is the plain
      # relative literal `.pre-commit-config.yaml` — confirmed against this
      # repo's OWN installed shim (`.git/hooks/pre-commit`):
      # `exec "$PREK" hook-impl … --config=".pre-commit-config.yaml" -- "$@"`.
      # `prek` bakes whatever string it is given into the shim VERBATIM; it does
      # not resolve it. That relative argument is then re-resolved by
      # `prek hook-impl` against the INVOKING WORKTREE's cwd at commit/push
      # time — NOT the canonical clone's — even though (post pg2-ohng1) the
      # SHIM ITSELF is now found correctly from any worktree via the shared,
      # corrected `core.hooksPath`. A fresh `git worktree add` worktree has no
      # `.pre-commit-config.yaml` of its own (gitignored/untracked, ADR 0016),
      # so `git commit` there aborts: "config file not found". Loud-and-closed
      # (safe, unlike pg2-ohng1's silent skip) but still a usability blocker —
      # exactly what this bead exists to remove, via the documented manual
      # workaround (symlink the canonical clone's config target into the
      # worktree) that this fix makes permanently unnecessary.
      #
      # THE FIX: rewrite each installed shim's `--config="…"` argument to the
      # config's RESOLVED ABSOLUTE STORE PATH. `preCommit.shellHook` writes
      # `.pre-commit-config.yaml` at the repo's TOP LEVEL as a symlink whose
      # target IS that exact absolute path (git-hooks.nix line 506:
      # `ln -fs ${cfg.configFile} "$GIT_WC/${cfg.configPath}"`), so a plain,
      # single-hop `readlink` of it (no `-f`, no chained resolution needed)
      # gives exactly the value every shim's `--config=` argument should carry.
      # Once every shim carries that absolute value, hook invocation depends on
      # NOTHING worktree-local — no per-worktree symlink, ever, for any current
      # or future worktree — while still failing loudly (not silently) should
      # the store path itself ever be missing.
      #
      # GENERAL, not special-cased to one stage: rather than hardcoding a list
      # of stage names (which would drift the moment a consumer's `extraHooks`
      # configures a new one, e.g. `commit-msg`), this scans every file in the
      # resolved hooks directory and rewrites any that actually invoke
      # `prek hook-impl … --config="…"` — i.e. every shim `prek install` ever
      # wrote, for whichever stages are actually configured, pre-commit and
      # pre-push alike, with zero hardcoded stage list. It never touches the
      # `*.sample` hooks `git init` ships (they contain neither marker) or a
      # hand-authored hook (which would not match the pattern either).
      #
      # TEXT-REWRITE, NOT A `prek install` RE-RUN — deliberately, and this is
      # WHY IT MUST RUN LAST: `hardenPrePushHook` (above) re-installs the
      # pre-push shim via its OWN hardcoded `prek install -c
      # .pre-commit-config.yaml --allow-missing-config -t pre-push -f` call.
      # If this step tried to fix the config path the SAME way — re-running
      # `prek install` per stage — it would face a genuine ordering conflict:
      # run before `hardenPrePushHook` and that step's hardcoded RELATIVE `-c`
      # would immediately undo the absolutizing for pre-push; run after it
      # using a plain `prek install` and it would DROP the
      # `--skip-on-missing-config` flag `hardenPrePushHook` just baked in
      # (that call carries no `--allow-missing-config` of its own), silently
      # reintroducing the pg2-vqyw3 regression. Rewriting the ONE substring in
      # place, after everything else has already written its shim, avoids ever
      # needing to know or reproduce what flags a prior step baked in — every
      # other byte of the shim is left exactly as the prior step wrote it.
      #
      # ENFORCEMENT IS UNCHANGED — the acceptance criterion that separates this
      # from the `--allow-missing-config` anti-pattern: this only rewrites an
      # ARGUMENT VALUE that already points at a real, present config. It never
      # adds `--allow-missing-config`/`--skip-on-missing-config` to the general
      # (pre-commit) path, and never touches which hooks run or their exit
      # behaviour. A genuine violation is still REJECTED, with real hook
      # output, from any worktree — the fix makes the config resolvable, not
      # the gate optional.
      #
      # IDEMPOTENT / CHURN-FREE, same discipline as `correctRelativeHooksPath`
      # and its "compare before it writes" rule (upstream's own line 484-487):
      # each shim is only rewritten if its CURRENT `--config="…"` value differs
      # from the freshly-`readlink`ed absolute path, so a repeat run over an
      # already-correct shim performs no write (mtime and content untouched).
      # The rewrite itself is write-to-temp-then-`mv`, never `sed -i` (whose
      # in-place flag differs between BSD sed on macOS and GNU sed on Linux),
      # so it is portable and atomic on both.
      absolutizeHookConfigPath = ''
        _pgii_git="${lib.getExe' pkgs.git "git"}"
        if "$_pgii_git" rev-parse --git-dir >/dev/null 2>&1; then
          _pgii_wc="$("$_pgii_git" rev-parse --show-toplevel)"
          _pgii_config_abs="$(readlink "$_pgii_wc/.pre-commit-config.yaml" 2>/dev/null || true)"
          _pgii_hooks_dir="$("$_pgii_git" rev-parse --path-format=absolute --git-path hooks 2>/dev/null || true)"
          if [ -n "$_pgii_config_abs" ] && [ -d "$_pgii_hooks_dir" ]; then
            for _pgii_shim in "$_pgii_hooks_dir"/*; do
              [ -f "$_pgii_shim" ] || continue
              grep -q -- 'hook-impl' "$_pgii_shim" 2>/dev/null || continue
              grep -q -- '--config="' "$_pgii_shim" 2>/dev/null || continue
              grep -q -- "--config=\"$_pgii_config_abs\"" "$_pgii_shim" 2>/dev/null && continue
              _pgii_tmp="$_pgii_shim.pgii-tmp"
              sed -E 's#--config="[^"]*"#--config="'"$_pgii_config_abs"'"#' \
                "$_pgii_shim" >"$_pgii_tmp"
              chmod 0755 "$_pgii_tmp"
              mv "$_pgii_tmp" "$_pgii_shim"
            done
            unset _pgii_shim _pgii_tmp
          fi
          unset _pgii_wc _pgii_config_abs _pgii_hooks_dir
        fi
        unset _pgii_git
      '';
      # Single source of truth consumed by BOTH the devShell (via
      # `_module.args.preCommitShellHook`) and `install-pre-commit-hooks`, so the
      # corrected `core.hooksPath`, the tolerant pre-push shim, and the
      # absolutized config path are all applied on either entry point.
      #
      # ORDER IS LOAD-BEARING. `correctRelativeHooksPath` must run BEFORE
      # `hardenPrePushHook`, because the latter locates the shim with
      # `git rev-parse --git-path hooks/pre-push`, which HONOURS `core.hooksPath`.
      # While the relative value is still in place that resolution fails in a linked
      # worktree ("Invalid path …/.git/hooks: Not a directory"), so the pre-push
      # hardening would silently skip exactly where it is needed.
      #
      # `absolutizeHookConfigPath` must run LAST, after `hardenPrePushHook`: it
      # also needs the corrected `core.hooksPath` to locate shims (same reason
      # as `hardenPrePushHook`), AND it must see the shim(s) in their FINAL
      # state so it only ever touches the one `--config=` substring rather than
      # racing a later step's own shim rewrite (see its own comment above for
      # why re-running `prek install` instead, in a different order, would
      # conflict with `hardenPrePushHook`'s hardcoded relative `-c` argument).
      preCommitShellHook = ''
        ${preCommit.shellHook}
        ${correctRelativeHooksPath}
        ${hardenPrePushHook}
        ${absolutizeHookConfigPath}
      '';

      # Regression guard for pg2-ohng1, asserting the OBSERVABLE OUTCOME rather
      # than the config text: after `correctRelativeHooksPath` runs, does a git
      # hook actually FIRE on a commit made from a LINKED WORKTREE? That is the
      # property the defect silently removed, and the only one worth pinning —
      # keying the assertion on a particular `core.hooksPath` string would rot the
      # moment upstream changes what it writes.
      #
      # Self-contained and sandbox-safe: it builds a throwaway repo plus one linked
      # worktree in $TMPDIR from `pkgs.git` alone (no network, no /nix/store
      # writes, no reference to the consumer's own repo), so it runs unchanged in
      # every consumer that imports this module. It asserts the DEFECT first (a
      # CONTROL commit that must SUCCEED ungated), which is what keeps the test
      # from passing vacuously if the relativizing write ever disappears upstream.
      hooksPathWorktreeSafeCheck =
        pkgs.runCommand "pre-commit-hooks-path-worktree-safe"
          {
            nativeBuildInputs = [ pkgs.git ];
          }
          ''
            set -u
            export HOME="$TMPDIR"
            export GIT_CONFIG_GLOBAL="$TMPDIR/gitconfig"
            export GIT_CONFIG_SYSTEM=/dev/null
            export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t
            export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
            : >"$GIT_CONFIG_GLOBAL"

            canon="$TMPDIR/canon"
            git init -q -b main "$canon"
            cd "$canon"
            echo seed >seed.txt
            git add seed.txt
            git commit -qm seed
            seed_sha=$(git rev-parse HEAD)

            # An always-rejecting pre-commit hook: the presence or absence of its
            # marker line is the whole signal. Installed AFTER the seed commit so
            # the seed is not itself gated. Written with printf rather than a
            # heredoc because a heredoc body inside a Nix indented string depends
            # on the string's common-indent stripping to land at column 0.
            printf '%s\n' '#!/bin/sh' 'echo HOOK-FIRED' 'exit 1' \
              >"$canon/.git/hooks/pre-commit"
            chmod +x "$canon/.git/hooks/pre-commit"
            git worktree add -q "$TMPDIR/wt" -b wt

            # Reproduce cachix/git-hooks.nix modules/pre-commit.nix:483,541,544,546
            # with cwd == the CANONICAL clone: the prefix strips, so the value it
            # writes is RELATIVE. This is the premise; if it stops holding, the
            # rest of this check would pass for the wrong reason.
            GIT_WC=$(git rev-parse --show-toplevel)
            common_dir=$(git rev-parse --path-format=absolute --git-common-dir)
            common_dir=''${common_dir#"$GIT_WC"/}
            git config --local core.hooksPath "$common_dir/hooks"
            case $(git config --local --get core.hooksPath) in
              "" | /*)
                echo "PREMISE FAILED: upstream's write is no longer relative" >&2
                exit 1
                ;;
            esac

            # CONTROL: with the defect in place the hook must NOT fire in the
            # worktree, so a commit succeeds ungated. A failure here means the
            # defect is gone and this check is no longer measuring anything.
            cd "$TMPDIR/wt"
            echo control >control.txt
            git add control.txt
            # NOTE: never name a local `out` in a runCommand body — that shadows
            # the derivation's own $out and the final `touch` silently misses.
            if ! commit_out=$(git commit -m control 2>&1); then
              echo "CONTROL FAILED: the relative value already blocked the commit" >&2
              printf '%s\n' "$commit_out" >&2
              exit 1
            fi
            case $commit_out in
              *HOOK-FIRED*)
                echo "CONTROL FAILED: the hook fired despite the relative value" >&2
                exit 1
                ;;
            esac
            git reset -q --hard "$seed_sha"

            # THE FIX, byte-identical to what the shellHook and
            # install-pre-commit-hooks run.
            ${correctRelativeHooksPath}

            # Tolerate a FAILING rev-parse rather than letting `set -e` abort: with
            # the defect in place git exits 128 with "Invalid path …/.git/hooks: Not
            # a directory", and reporting that as this check's own diagnosis is far
            # more useful to a future reader than a bare git fatal.
            want="$canon/.git/hooks"
            for d in "$canon" "$TMPDIR/wt"; do
              got=$(git -C "$d" rev-parse --path-format=absolute --git-path hooks 2>&1) ||
                got="<rev-parse failed: $got>"
              if [ "$got" != "$want" ]; then
                echo "FAIL: $d resolves hooks to '$got', want '$want'" >&2
                exit 1
              fi
            done

            # THE ASSERTION: the same violating commit is now REJECTED from the
            # worktree, with hook output, and HEAD does not move.
            echo violation >violation.txt
            git add violation.txt
            if commit_out=$(git commit -m violation 2>&1); then
              echo "FAIL: worktree commit succeeded; the hook did not fire" >&2
              exit 1
            fi
            case $commit_out in
              *HOOK-FIRED*) ;;
              *)
                echo "FAIL: no hook output from the worktree commit" >&2
                printf '%s\n' "$commit_out" >&2
                exit 1
                ;;
            esac
            if [ "$(git rev-parse HEAD)" != "$seed_sha" ]; then
              echo "FAIL: HEAD moved despite the rejected commit" >&2
              exit 1
            fi

            # The canonical clone must stay gated too.
            cd "$canon"
            echo canon-violation >canon-violation.txt
            git add canon-violation.txt
            if git commit -m canon-violation >/dev/null 2>&1; then
              echo "FAIL: canonical commit succeeded; the hook did not fire" >&2
              exit 1
            fi
            git reset -q --hard "$seed_sha"

            # IDEMPOTENT + CHURN-FREE: a repeat run must neither fail nor rewrite
            # .git/config (the shellHook runs on every devShell entry).
            before=$(sha256sum "$canon/.git/config" | cut -d' ' -f1)
            ${correctRelativeHooksPath}
            ${correctRelativeHooksPath}
            after=$(sha256sum "$canon/.git/config" | cut -d' ' -f1)
            if [ "$before" != "$after" ]; then
              echo "FAIL: re-running the correction rewrote .git/config" >&2
              exit 1
            fi

            # A deliberate ABSOLUTE value is NOT the defect and must be preserved.
            git config --local core.hooksPath "$TMPDIR/custom-hooks"
            ${correctRelativeHooksPath}
            if [ "$(git config --local --get core.hooksPath)" != "$TMPDIR/custom-hooks" ]; then
              echo "FAIL: an absolute core.hooksPath was clobbered" >&2
              exit 1
            fi

            # Outside a git repo the correction must no-op silently (upstream's
            # not-a-git-repo skip).
            mkdir -p "$TMPDIR/notarepo"
            cd "$TMPDIR/notarepo"
            export GIT_CEILING_DIRECTORIES="$TMPDIR"
            ${correctRelativeHooksPath}

            touch "$out"
          '';

      # Regression guard for pg2-7g7yl, same shape as
      # `hooksPathWorktreeSafeCheck` immediately above (assert the DEFECT first
      # as a control, then THE FIX): does a genuine hook violation get
      # REJECTED, with real output, from a linked worktree that has NEVER had
      # `install-pre-commit-hooks` run inside it — zero per-worktree setup?
      #
      # Exercises the REAL `prek` binary end to end (not a hand-rolled shim),
      # because the defect and the fix both live in the exact argument `prek
      # install` bakes into the shim it writes — a hand-authored stand-in
      # would not prove anything about that. Self-contained and sandbox-safe:
      # `pkgs.git` and `pkgs.prek` only, no network, no reference to the
      # consumer's own repo, so it runs unchanged in every consumer that
      # imports this module.
      absolutizeHookConfigPathCheck =
        pkgs.runCommand "pre-commit-hooks-config-path-absolute"
          {
            nativeBuildInputs = [
              pkgs.git
              pkgs.prek
            ];
          }
          ''
            set -u
            export HOME="$TMPDIR"
            export GIT_CONFIG_GLOBAL="$TMPDIR/gitconfig"
            export GIT_CONFIG_SYSTEM=/dev/null
            export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t
            export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
            : >"$GIT_CONFIG_GLOBAL"

            canon="$TMPDIR/canon"
            git init -q -b main "$canon"
            cd "$canon"
            echo seed >seed.txt
            git add seed.txt
            git commit -qm seed
            seed_sha=$(git rev-parse HEAD)

            # A minimal, REAL prek config: one always-run hook that fails, so
            # its firing (HOOK-FIRED) is the whole signal — same technique as
            # `hooksPathWorktreeSafeCheck`'s hand-rolled hook, but this time
            # authored as a genuine prek/pre-commit-format config, since this
            # check's subject IS the `--config=` argument prek itself bakes
            # into the shim.
            storeconfig="$TMPDIR/store-config.yaml"
            cat >"$storeconfig" <<'PGII_EOF'
            repos:
              - repo: local
                hooks:
                  - id: reject
                    name: reject
                    entry: sh -c 'echo HOOK-FIRED; exit 1'
                    language: system
                    always_run: true
                    pass_filenames: false
            PGII_EOF
            ln -s "$storeconfig" .pre-commit-config.yaml

            # Reproduce exactly what git-hooks.nix's installer does for the
            # pre-commit stage (its line 524/537): install with the RELATIVE
            # configPath. This is the premise; if prek ever stops baking a
            # relative `--config=` argument itself, the rest of this check
            # would pass for the wrong reason.
            prek install -c .pre-commit-config.yaml -t pre-commit
            if ! grep -q -- '--config=".pre-commit-config.yaml"' .git/hooks/pre-commit; then
              echo "PREMISE FAILED: prek no longer bakes a relative --config= argument" >&2
              exit 1
            fi

            git worktree add -q "$TMPDIR/wt" -b wt

            # CONTROL: from the linked worktree, with NO config symlink of its
            # own and NO per-worktree setup, the commit must fail
            # loud-and-closed (config not found) — this is the pg2-7g7yl
            # defect this check exists to remove. A failure here means the
            # defect is already gone and this check is no longer measuring
            # anything.
            cd "$TMPDIR/wt"
            echo control >control.txt
            git add control.txt
            if commit_out=$(git commit -m control 2>&1); then
              echo "CONTROL FAILED: commit succeeded despite the missing worktree config" >&2
              exit 1
            fi
            case $commit_out in
              *"config file not found"* | *"No such file or directory"* | *"not found"*) ;;
              *)
                echo "CONTROL FAILED: unexpected failure mode (not a missing-config error)" >&2
                printf '%s\n' "$commit_out" >&2
                exit 1
                ;;
            esac
            git reset -q --hard "$seed_sha"
            git clean -qfd

            # THE FIX, byte-identical to what the shellHook and
            # install-pre-commit-hooks run.
            cd "$canon"
            ${correctRelativeHooksPath}
            ${absolutizeHookConfigPath}
            if ! grep -q -- "--config=\"$storeconfig\"" .git/hooks/pre-commit; then
              echo "FAIL: shim was not rewritten to the absolute config path" >&2
              cat .git/hooks/pre-commit >&2
              exit 1
            fi

            # THE ASSERTION: the SAME violating commit, from the SAME linked
            # worktree, with ZERO worktree-local setup, is now REJECTED with
            # real hook output, and HEAD does not move.
            cd "$TMPDIR/wt"
            echo violation >violation.txt
            git add violation.txt
            if commit_out=$(git commit -m violation 2>&1); then
              echo "FAIL: worktree commit succeeded; the hook did not fire" >&2
              exit 1
            fi
            case $commit_out in
              *HOOK-FIRED*) ;;
              *)
                echo "FAIL: no hook output from the worktree commit" >&2
                printf '%s\n' "$commit_out" >&2
                exit 1
                ;;
            esac
            if [ "$(git rev-parse HEAD)" != "$seed_sha" ]; then
              echo "FAIL: HEAD moved despite the rejected commit" >&2
              exit 1
            fi

            # A CLEAN commit (no violation) must still succeed from the same
            # worktree — the fix must reject only genuine violations, not
            # everything.
            cat >"$storeconfig" <<'PGII_EOF'
            repos:
              - repo: local
                hooks:
                  - id: allow
                    name: allow
                    entry: sh -c 'exit 0'
                    language: system
                    always_run: true
                    pass_filenames: false
            PGII_EOF
            echo clean >clean.txt
            git add clean.txt
            if ! git commit -qm clean; then
              echo "FAIL: a clean commit was rejected from the worktree" >&2
              exit 1
            fi
            git reset -q --hard "$seed_sha"
            git clean -qfd

            # IDEMPOTENT + CHURN-FREE: a repeat run must neither fail nor
            # rewrite an already-correct shim.
            cd "$canon"
            before=$(sha256sum .git/hooks/pre-commit | cut -d' ' -f1)
            ${absolutizeHookConfigPath}
            ${absolutizeHookConfigPath}
            after=$(sha256sum .git/hooks/pre-commit | cut -d' ' -f1)
            if [ "$before" != "$after" ]; then
              echo "FAIL: re-running the absolutizer rewrote an already-correct shim" >&2
              exit 1
            fi

            touch "$out"
          '';
    in
    {
      _module.args.preCommitShellHook = preCommitShellHook;
      checks = {
        pre-commit = preCommit;
        pre-commit-config-gitignored = preCommitConfigGitignoredCheck;
        pre-commit-hooks-path-worktree-safe = hooksPathWorktreeSafeCheck;
        pre-commit-hooks-config-path-absolute = absolutizeHookConfigPathCheck;
      };
      packages.install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
        ${preCommitShellHook}
        echo "Pre-commit hooks installed successfully!"
        echo "Run 'pre-commit run --all-files' to test them."
      '';

      # Autofix helper for the `statix` pre-commit hook. Runs `statix fix` over
      # the CURRENT working directory (or the paths given as args) — NOT ${./.},
      # which resolves to a read-only /nix/store copy statix can never write to.
      # Auto-contributed to every consumer that imports this flakeModule, so the
      # six hand-rolled per-repo copies (five of them broken with the ${./.} bug)
      # are deleted in favour of this single source of truth (bead pg2-7vhvn).
      packages.fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
        exec ${pkgs.lib.getExe pkgs.statix} fix "''${@:-.}"
      '';
    };
}
