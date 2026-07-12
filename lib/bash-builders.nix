# mkBashBuilders — factory for bash script packaging builders
#
# Takes { pkgs, lib, self } and returns { mkBashLibrary, mkBashScript, mkBashModule, gitHash }.
# gitHash is retained in the return set for compatibility but is no longer used by
# mkBashScript — version now comes from the per-source content digest; see ADR 0006.
# mkBashLibrary composes sourceable bash libraries with dependency chaining
# and generates test check derivations.
{
  pkgs,
  lib,
  self,
}:
let
  versionLib = import ./version.nix;
  gitHash = versionLib.mkGitHash (self.rev or self.dirtyRev or null);

  # mkBashLibrary — build a sourceable bash library with dependency chaining
  #
  # Arguments:
  #   name        — derivation / library name
  #   src         — path to the directory containing the .bash source file
  #   description — human-readable description
  #   libraries   — list of mkBashLibrary results whose .lib will be sourced first
  #   testSupport — optional path to a bats test support directory
  #   testDeps    — additional packages needed at test time
  #
  # Returns: { lib, check, description }
  mkBashLibrary =
    {
      name,
      src,
      description,
      libraries ? [ ],
      testSupport ? null,
      testDeps ? [ ],
    }:
    let
      # Read the source .bash file
      sourceFile = src + "/${name}.bash";
      sourceContent = builtins.readFile sourceFile;

      # Build source lines for dependencies
      depSources = lib.concatMapStringsSep "\n" (dep: "source ${dep.lib}") libraries;

      # Compose the library: dependency sources + library source
      composedContent = (lib.optionalString (libraries != [ ]) (depSources + "\n")) + sourceContent;

      # The composed library as a single file in the nix store
      composedLib = pkgs.writeText "${name}.bash" composedContent;

      # Test directory lives at src/tests/
      testDir = src + "/tests";

      # Build the test check derivation
      check =
        pkgs.runCommand "check-${name}"
          {
            nativeBuildInputs = [
              pkgs.bats
              pkgs.shellcheck
            ]
            ++ testDeps;
          }
          ''
            shellcheck -e SC1091 ${src}/${name}.bash
            export LIB_PATH="${composedLib}"
            ${lib.optionalString (testSupport != null) ''
              export TEST_SUPPORT="${testSupport}"
              export BATS_SUPPORT_PATH="$TMPDIR"
              cp ${testSupport}/*.bash $TMPDIR/ 2>/dev/null || true
            ''}
            bats ${testDir}
            touch $out
          '';
    in
    {
      lib = composedLib;
      inherit check description;
    };

  # mkBashScript — build and package a bash command with completions, man page, and tests
  #
  # Arguments:
  #   name           — command name, matches <name>.sh in src
  #   src            — directory containing <name>.sh, completions/, tests/, etc.
  #   description    — human-readable description (used for man page)
  #   public         — if true, command appears on PATH (default: true)
  #   libraries      — list of mkBashLibrary outputs whose .lib will be sourced
  #   runtimeDeps    — packages to add to PATH at runtime
  #   config         — attrset of local (unexported) variables
  #   exportedConfig — attrset of exported variables
  #   testSupport    — optional path to a bats test support directory
  #   testDeps       — additional packages needed at test time
  #
  # Check environment (bead pg2-28wwb): the `check` runs bats with both SCRIPTS_DIR
  # (raw source, legacy — retained for backward compatibility) and SCRIPT_UNDER_TEST
  # (path to the assembled, shipped bin/<name>) exported. New bats tests SHOULD target
  # SCRIPT_UNDER_TEST to cover the injected header/config/source lines and PATH wrapper.
  # Every `check` also runs a mandatory `<name> --version`/`-v` floor smoke against the
  # assembled artifact, so a defect introduced purely by assembly fails the check.
  #
  # Returns: { script, tldr, completion, check, packages, internalPackages }
  mkBashScript =
    {
      name,
      src,
      description,
      public ? true,
      libraries ? [ ],
      runtimeDeps ? [ ],
      config ? { },
      exportedConfig ? { },
      testSupport ? null,
      testDeps ? [ ],
      baseVersion ? "0.0.0",
      manPage ? true,
    }:
    let
      # Check for optional files
      hasTldr = builtins.pathExists (src + "/${name}.md");
      hasBashCompletion = builtins.pathExists (src + "/completions/${name}.bash");
      hasZshCompletion = builtins.pathExists (src + "/completions/_${name}");

      # Check for optional .bash support file
      hasSupportBash = builtins.pathExists (src + "/${name}.bash");
      supportBashFile = src + "/${name}.bash";

      # Build config variable lines with shared store paths for script and test
      mkConfigLine =
        prefix: varName: value:
        if builtins.isString value then
          {
            scriptLine = "${prefix}${varName}=\"${value}\"";
            testExport = "export ${varName}=\"${value}\"";
          }
        else
          let
            jsonFile = pkgs.writeText "${name}-${varName}.json" (builtins.toJSON value);
          in
          {
            scriptLine = "${prefix}${varName}=\"${jsonFile}\"";
            testExport = "export ${varName}=\"${jsonFile}\"";
          };

      configEntries = lib.mapAttrsToList (n: v: mkConfigLine "" n v) config;
      exportedConfigEntries = lib.mapAttrsToList (n: v: mkConfigLine "export " n v) exportedConfig;

      configLines = lib.concatMapStringsSep "\n" (e: e.scriptLine) configEntries;
      exportedConfigLines = lib.concatMapStringsSep "\n" (e: e.scriptLine) exportedConfigEntries;

      # Build the script body as a Nix string (all content after the version handler)
      scriptBody = lib.concatStringsSep "\n" (
        lib.filter (s: s != "") [
          (lib.concatMapStringsSep "\n" (dep: "source ${dep.lib}") libraries)
          (lib.optionalString hasSupportBash "source ${supportBashFile}")
          configLines
          exportedConfigLines
          (builtins.readFile "${src}/${name}.sh")
        ]
      );
      scriptBodyFile = pkgs.writeText "${name}-body" scriptBody;

      # Per-source content digest: this script's src plus each sourced library's
      # composed-lib store path (l.lib transitively embeds nested libs). Repo-rev
      # independent; see ADR 0006.
      srcDigest = versionLib.mkSrcDigest ([ src ] ++ map (l: l.lib) libraries);

      # The main script derivation
      script = pkgs.stdenv.mkDerivation {
        pname = name;
        version = "${baseVersion}-${srcDigest}"; # nvd-visible per-source digest (ADR 0011); runtime --version adds the date in buildPhase

        inherit src;

        dontUnpack = true;

        nativeBuildInputs = [
          pkgs.makeWrapper
          pkgs.shellcheck
          pkgs.help2man
          pkgs.libfaketime
        ];

        # runtimeDeps are NOT in buildInputs — they're purely runtime, added to
        # PATH via wrapProgram below. Including them in buildInputs pulls in their
        # propagated setup hooks (e.g. pytestCheckHook from pkgs.pre-commit) which
        # would then run checkPhase on this bash-script derivation.
        buildInputs = [ pkgs.bash ];

        buildPhase = ''
          runHook preBuild

          # Compute version: YY.MM.DD.SSSSS+<srcDigest> (date is build-time and not a
          # derivation input; srcDigest is eval-time and content-driven). See ADR 0006.
          SECONDS_TODAY=$(( $(date -u +%s) % 86400 ))
          FULL_VERSION=$(printf "%s.%05d+%s" "$(date -u +%y.%m.%d)" "$SECONDS_TODAY" "${srcDigest}")

          # Assemble script: header + body
          {
            echo '#!/usr/bin/env bash'
            echo 'set -euo pipefail'
            echo ""
            echo '# Version handling (injected by mkBashBuilders)'
            echo "if [[ \"\''${1:-}\" == \"-v\" || \"\''${1:-}\" == \"--version\" ]]; then"
            echo "  echo \"${name} $FULL_VERSION\""
            echo "  exit 0"
            echo "fi"
            echo ""
            cat ${scriptBodyFile}
          } > ${name}

          chmod +x ${name}

          # Shellcheck the assembled script
          shellcheck -e SC1091 ${name}

          runHook postBuild
        '';

        installPhase = ''
          runHook preInstall

          mkdir -p $out/bin
          install -m 0755 ${name} $out/bin/${name}

          ${lib.optionalString (runtimeDeps != [ ]) ''
            wrapProgram $out/bin/${name} \
              --prefix PATH : ${lib.makeBinPath runtimeDeps}
          ''}

          ${lib.optionalString hasBashCompletion ''
            mkdir -p $out/share/bash-completion/completions
            cp ${src}/completions/${name}.bash $out/share/bash-completion/completions/${name}
          ''}

          ${lib.optionalString hasZshCompletion ''
            mkdir -p $out/share/zsh/site-functions
            cp ${src}/completions/_${name} $out/share/zsh/site-functions/_${name}
          ''}

          ${lib.optionalString hasTldr ''
            mkdir -p $out/share/tldr/pages.common
            cp ${src}/${name}.md $out/share/tldr/pages.common/${name}.md
          ''}

          ${lib.optionalString manPage ''
            # Man page via help2man, folded into the script derivation so it inherits the
            # script's version (ADR 0011). faketime fixes the date for reproducibility.
            # Best-effort (mirrors mkGoApp's _try): --no-discard-stderr handles a --help
            # that writes to stderr, and a script help2man cannot parse is skipped with a
            # warning rather than failing the whole build.
            mkdir -p $out/share/man/man1
            if faketime '2020-01-01 00:00:00' \
                 help2man --no-info --no-discard-stderr \
                   --name="${description}" \
                   --output=$out/share/man/man1/${name}.1 \
                   $out/bin/${name}; then
              :
            else
              echo "mkBashScript: help2man could not generate a man page for ${name} (no parseable --help); skipping" >&2
              rm -f $out/share/man/man1/${name}.1
            fi
          ''}

          runHook postInstall
        '';

        meta = {
          inherit description;
          platforms = lib.platforms.darwin ++ lib.platforms.linux;
          mainProgram = name;
        };
      };

      # tldr attrs (if .md exists)
      tldr =
        if hasTldr then
          {
            ${name} = builtins.readFile (src + "/${name}.md");
          }
        else
          { };

      # Completion paths
      completion = {
        bash = if hasBashCompletion then "${script}/share/bash-completion/completions/${name}" else null;
        zsh = if hasZshCompletion then "${script}/share/zsh/site-functions/_${name}" else null;
      };

      # Test check derivation
      testDir = src + "/tests";
      check =
        pkgs.runCommand "check-${name}"
          {
            nativeBuildInputs = [
              pkgs.bats
              pkgs.bash
            ]
            ++ testDeps;
          }
          ''
            # Floor smoke (bead pg2-28wwb): the shipped, ASSEMBLED + wrapped artifact must
            # run end-to-end — shebang + injected `set -euo pipefail` + version handler +
            # runtimeDeps PATH wrapper — which the bats suites (raw source via SCRIPTS_DIR)
            # never exercise. Placed before the `cp` so it is independent of the tests/ dir
            # and never masked by a copy error. Written defensively so it does not rely on
            # runCommand's set -e. ${name}/${script} are Nix interpolations; $smoke_out is a
            # shell var (no braces) passed through literally.
            smoke_out="$("${script}/bin/${name}" --version)" || {
              echo "FAIL: assembled '${name} --version' exited non-zero" >&2; exit 1; }
            case "$smoke_out" in
              "${name} "*) ;;
              *) echo "FAIL: assembled '${name} --version' did not emit '${name} <version>': $smoke_out" >&2; exit 1 ;;
            esac
            "${script}/bin/${name}" -v >/dev/null || {
              echo "FAIL: assembled '${name} -v' exited non-zero" >&2; exit 1; }

            # Copy tests to $TMPDIR for writability
            cp -r ${testDir}/* $TMPDIR/
            chmod -R u+w $TMPDIR/

            export SCRIPTS_DIR="${src}"
            # Assembled-artifact handle so bats authors MAY drive the shipped script
            # (covering injected header/config/source lines + runtimeDeps PATH wrapper)
            # instead of the raw .sh via SCRIPTS_DIR. Legacy SCRIPTS_DIR retained for
            # backward compatibility. (bead pg2-28wwb)
            export SCRIPT_UNDER_TEST="${script}/bin/${name}"
            ${lib.optionalString (libraries != [ ]) ''
              export LIB_PATH="${lib.concatMapStringsSep ":" (dep: "${dep.lib}") libraries}"
            ''}
            ${lib.optionalString (testSupport != null) ''
              export TEST_SUPPORT="${testSupport}"
              export BATS_SUPPORT_PATH="$TMPDIR"
              # Copy test support files alongside tests so bats `load` resolves them
              cp ${testSupport}/*.bash $TMPDIR/ 2>/dev/null || true
            ''}
            ${lib.concatMapStringsSep "\n" (e: e.testExport) exportedConfigEntries}
            ${lib.concatMapStringsSep "\n" (e: e.testExport) configEntries}

            bats $TMPDIR/
            touch $out
          '';
    in
    {
      inherit
        script
        tldr
        completion
        check
        ;
      packages = if public then [ script ] else [ ];
      internalPackages = [ script ];
    };
  # mkBashModule — aggregate mkBashScript and mkBashLibrary outputs into a single record
  #
  # Arguments:
  #   name      — module name (informational, included in returned attrset)
  #   scripts   — list of mkBashScript results
  #   libraries — list of mkBashLibrary results
  #
  # Returns: { name, packages, checks, tldr, libraries, scripts }
  mkBashModule =
    {
      name,
      scripts ? [ ],
      libraries ? [ ],
    }:
    {
      # All public script packages (only from scripts with public = true)
      packages = lib.concatMap (s: s.packages) scripts;

      # All test checks, keyed by name
      checks = lib.listToAttrs (
        (map (s: {
          name = "check-${s.script.pname}";
          value = s.check;
        }) scripts)
        ++ (map (l: {
          name = "check-${l.lib.name}";
          value = l.check;
        }) libraries)
      );

      # Merged tldr attrs from all scripts
      tldr = lib.foldl' (acc: s: acc // s.tldr) { } scripts;

      # Passthrough for cross-repo wiring
      inherit name libraries scripts;
    };
in
{
  inherit
    mkBashLibrary
    mkBashScript
    mkBashModule
    gitHash
    ;
}
