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
      # Single source of truth consumed by BOTH the devShell (via
      # `_module.args.preCommitShellHook`) and `install-pre-commit-hooks`, so the
      # corrected `core.hooksPath` and the tolerant pre-push shim are both applied on
      # either entry point.
      #
      # ORDER IS LOAD-BEARING. `correctRelativeHooksPath` must run BEFORE
      # `hardenPrePushHook`, because the latter locates the shim with
      # `git rev-parse --git-path hooks/pre-push`, which HONOURS `core.hooksPath`.
      # While the relative value is still in place that resolution fails in a linked
      # worktree ("Invalid path …/.git/hooks: Not a directory"), so the pre-push
      # hardening would silently skip exactly where it is needed.
      preCommitShellHook = ''
        ${preCommit.shellHook}
        ${correctRelativeHooksPath}
        ${hardenPrePushHook}
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
    in
    {
      _module.args.preCommitShellHook = preCommitShellHook;
      checks = {
        pre-commit = preCommit;
        pre-commit-config-gitignored = preCommitConfigGitignoredCheck;
        pre-commit-hooks-path-worktree-safe = hooksPathWorktreeSafeCheck;
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
