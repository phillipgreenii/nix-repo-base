# No producer-input closure needed — checks helpers use only consumer pkgs.
# `phillipgreenii.src` defaults to the consumer's flake root (inputs.self);
# consumers only need to set it if they want to scope formatting/linting to a
# subdirectory. The consumer-input-alignment check ALWAYS reads the consumer's
# top-level flake.lock (via inputs.self), independent of phillipgreenii.src.
{
  lib,
  config,
  inputs,
  ...
}:
let
  topLevelCfg = config.phillipgreenii;
in
{
  options.phillipgreenii = {
    src = lib.mkOption {
      type = lib.types.path;
      default = inputs.self.outPath;
      defaultText = lib.literalExpression "inputs.self";
      description = ''
        Source root used by the auto-contributed formatting + linting checks.
        Defaults to the consumer's flake root; override only to scope these
        checks to a subdirectory.
      '';
    };
    alignment.requires = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Names of consumer-declared flake inputs that imported overlay modules
        require. The consumer-input-alignment check reads this list and verifies
        the consumer's flake.lock has each input declared exactly once
        (no <name>_N duplicates from missing follows).
      '';
    };
  };

  config.perSystem =
    {
      pkgs,
      ...
    }:
    let
      mkHelpers = p: {
        formatting =
          root:
          p.runCommand "check-formatting"
            {
              nativeBuildInputs = [ p.nixfmt ];
            }
            ''
              cd ${root}
              nixfmt --check .
              touch $out
            '';

        linting =
          root:
          p.runCommand "check-linting"
            {
              nativeBuildInputs = [ p.statix ];
            }
            ''
              statix check ${root}
              touch $out
            '';

        shellcheck =
          {
            scripts,
            exclude ? [ ],
            allowWarnings ? false,
          }:
          let
            excludeArgs = if exclude != [ ] then "-e ${lib.concatStringsSep "," exclude}" else "";
            # allowWarnings raises the reporting FLOOR to `error` so warnings no
            # longer fail the check while genuine errors still do. It previously
            # appended `|| true`, which swallowed EVERY shellcheck failure —
            # errors included — despite the "allowWarnings" name (bead pg2-ncyg5).
            severity = if allowWarnings then "error" else "warning";
          in
          p.runCommand "check-shellcheck"
            {
              nativeBuildInputs = [ p.shellcheck ];
            }
            ''
              ${lib.concatMapStringsSep "\n" (
                # --severity=warning keeps this check consistent with the treefmt
                # shellcheck formatter and the pre-commit shellcheck hook (tc-neh26).
                script: "${p.shellcheck}/bin/shellcheck --severity=${severity} ${excludeArgs} ${script}"
              ) scripts}
              touch $out
            '';

        testBashScripts =
          {
            package,
            tests,
            extraInputs ? [ ],
          }:
          p.runCommand "test-bash-scripts"
            {
              nativeBuildInputs = [
                p.bats
                p.git
                p.which
                package
              ]
              ++ extraInputs;
            }
            ''
              export PATH="${package}/bin:$PATH"
              bats ${tests}
              touch $out
            '';

        testPythonProject =
          {
            package,
            src,
            name,
            checkLibDir ? null,
          }:
          p.runCommand "test-${name}"
            {
              nativeBuildInputs = [
                p.bash
                p.uv
                package
              ];
              inherit src;
            }
            ''
              export HOME=$TMPDIR
              export UV_CACHE_DIR=$TMPDIR/uv-cache
              mkdir -p $UV_CACHE_DIR
              ${lib.optionalString (checkLibDir != null) ''export CHECK_LIB_DIR="${checkLibDir}"''}
              cp -r $src ${name}
              cd ${name}
              chmod +w -R .
              bash check-all.sh --no-fix --quick --suppress-coverage-check
              touch $out
            '';

        testUpdateLocksLib =
          {
            testsDir ? ../lib/tests,
            scriptsDir ? ../lib/scripts,
          }:
          p.runCommand "test-update-locks-lib"
            {
              nativeBuildInputs = [
                p.bats
                p.bash
                p.coreutils
                p.git
              ];
            }
            ''
              export PATH="${
                lib.makeBinPath [
                  p.coreutils
                  p.bash
                  p.git
                ]
              }:$PATH"
              export UL_LIB_SCRIPTS_DIR="${scriptsDir}"
              export HOME="$TMPDIR/test-home"
              mkdir -p "$HOME"
              bats ${testsDir}
              touch $out
            '';
      };
      helpers = mkHelpers pkgs;
    in
    {
      _module.args.checksHelpers = helpers;

      checks = {
        formatting = helpers.formatting topLevelCfg.src;
        linting = helpers.linting topLevelCfg.src;
        consumer-input-alignment =
          let
            requiresJSON = builtins.toJSON topLevelCfg.alignment.requires;
          in
          pkgs.runCommand "consumer-input-alignment"
            {
              requires = requiresJSON;
              # ALWAYS read the consumer's top-level flake.lock via inputs.self —
              # NOT phillipgreenii.src, which may be scoped to a subdirectory for
              # formatting/linting purposes. flake-parts binds inputs.self to the
              # importing flake (consumer at consumer's eval; nix-repo-base at
              # nix-repo-base's eval), so this always reads the right lock.
              consumerLock = builtins.toString (inputs.self + "/flake.lock");
              nativeBuildInputs = [ pkgs.jq ];
            }
            ''
              set -euo pipefail
              count=$(echo "$requires" | ${pkgs.jq}/bin/jq -r 'length')
              if [ "$count" = "0" ]; then
                echo "alignment: no required inputs (no overlay modules imported)"
                touch $out
                exit 0
              fi
              failed=0
              for name in $(echo "$requires" | ${pkgs.jq}/bin/jq -r '.[]'); do
                if ! ${pkgs.jq}/bin/jq -e --arg n "$name" '.nodes.root.inputs | has($n)' "$consumerLock" >/dev/null; then
                  echo "ERROR: required input '$name' is not declared at top level of flake.lock" >&2
                  failed=1
                  continue
                fi
                dupes=$(${pkgs.jq}/bin/jq -r --arg n "$name" '.nodes | keys[] | select(test("^" + $n + "_[0-9]+$"))' "$consumerLock")
                if [ -n "$dupes" ]; then
                  echo "ERROR: input '$name' has duplicate nodes in flake.lock: $dupes" >&2
                  echo "       Missing 'follows' on a downstream flake. Add e.g.:" >&2
                  echo "       inputs.<downstream>.inputs.$name.follows = \"$name\";" >&2
                  failed=1
                fi
              done
              [ "$failed" = 0 ] || exit 1
              touch $out
            '';

        # Auto-contributed security drift guard (bead pg2-o784p). Reads the
        # consumer's OWN update-locks.sh via inputs.self (NOT phillipgreenii.src,
        # which may be scoped to a subdirectory — mirrors consumer-input-alignment
        # above). Fails if the script still calls the nix-repo-base resolver at
        # unpinned HEAD, which lets token-bearing CI execute arbitrary default-branch
        # code. Pure eval-time read (no IFD: inputs.self is an already-realised store
        # path, exactly like pre-commit.nix reads .gitignore). Every repo that imports
        # flakeModules.checks inherits this with zero per-repo wiring, so the pin
        # fix cannot silently fail to propagate to a new consumer.
        update-locks-pinned =
          let
            ulPath = inputs.self + "/update-locks.sh";
            inherit (import ../lib/ul-pin.nix { inherit lib; }) isUnpinnedUpdateLocks;
          in
          if !builtins.pathExists ulPath then
            # No update-locks.sh to guard — trivially compliant.
            pkgs.runCommand "update-locks-pinned" { } "touch $out"
          else if isUnpinnedUpdateLocks (builtins.readFile ulPath) then
            throw "phillipgreenii.checks.update-locks-pinned: ${toString ulPath} calls the nix-repo-base resolver at UNPINNED HEAD (nix run github:phillipgreenii/nix-repo-base#determine-ul-lib-dir). In token-bearing CI this builds and runs whatever is at nix-repo-base's default branch — an arbitrary code-execution hole (bead pg2-o784p). Pin it to the locked rev: resolve NRB_REV via 'nix flake metadata' and nix run the rev-pinned ref. Reference implementation: phillipgreenii-nix-overlay/update-locks.sh."
          else
            pkgs.runCommand "update-locks-pinned" { } "touch $out";
      };
    };
}
