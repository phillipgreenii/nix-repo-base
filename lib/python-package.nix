# Helper for creating Python packages, lock-driven via uv2nix (ADR 0022, bead pg2-r4cfy).
#
# Two-stage: the OUTER function captures the uv2nix ecosystem inputs (curried in
# from flake.nix); the INNER function keeps the historical
# `{ pkgs, lib, mkSrcDigest }` factory signature UNCHANGED so every
# `self.lib.mkPythonBuilders { pkgs; lib; mkSrcDigest; }` call site is untouched
# (including agent-support, which instantiates the factory but ships no app).
#
# The shipped closure is the resolved-and-tested `uv.lock` (not a name-match
# against nixpkgs). uv2nix reads uv.lock DIRECTLY — there is no generate step and
# no second lock artifact, so update-locks.sh needs no new step.
{
  uv2nix,
  pyproject-nix,
  pyproject-build-systems,
}:
{
  pkgs,
  lib,
  mkSrcDigest,
}:
{
  # Create a lock-driven Python application.
  # Usage: mkPythonPackage {
  #   name = "gh-prreview";
  #   src = ./.;                                   # must contain pyproject.toml + a git-tracked uv.lock
  #   runtimeDeps = [ pkgs.gh pkgs.git ];
  #   versionPlaceholder = "0.0.0";                # version string in pyproject.toml to replace
  #   versionInitFile = "src/gh_prreview/__init__.py";  # optional __init__.py with __version__.
  #                                                      # Omit only if the app never reads __version__:
  #                                                      # when null, importlib.metadata is stamped but
  #                                                      # __version__ stays at the placeholder.
  #   hasCompletions = true;
  #   hasTldr = true;
  # }
  mkPythonPackage =
    {
      name,
      src,
      runtimeDeps ? [ ],
      versionPlaceholder ? "0.0.0",
      baseVersion ? "0.0.0",
      versionInitFile ? null,
      hasCompletions ? true,
      hasTldr ? true,
      extraPostInstall ? "",
      # --- Accepted NO-OPS (ADR 0022): retained so base can land BEFORE the
      # support-apps consumer cleanup without an unknown-arg eval error (the arg
      # set has no `...`). uv2nix resolves everything from uv.lock, so these do
      # nothing now; removal is a separate follow-up bead once consumer usage is
      # gone. DO NOT drop these params in this repo before the consumers stop
      # passing them, or agent-support/support-apps fail to evaluate.
      customDeps ? { },
      pypiToNixNameMappings ? { },
      allowMissingDeps ? false,
      extraNativeBuildInputs ? [ ],
    }:
    let
      python = pkgs.python3;

      # Per-source content digest (ADR 0006). Computed at EVAL from the original
      # src (which includes uv.lock + pyproject.toml), so it stays pure/cacheable
      # and never folds in the build-time date below.
      srcDigest = mkSrcDigest src;

      pyproject = builtins.fromTOML (builtins.readFile "${src}/pyproject.toml");
      # PEP 503-normalized name. uv2nix keys the pythonSet by the normalized name,
      # so the version-stamp overlay MUST index by it — indexing by a raw
      # underscore/dot/uppercase project name would silently define a bogus attr
      # and never stamp (shipping the 0.0.0 placeholder). See the py_lock_pin
      # fixture, whose name is deliberately non-normalized to guard this.
      pname = pyproject-nix.lib.pypa.normalizePackageName pyproject.project.name;

      # (1) Load the committed uv.lock — THIS is the fix. The lock, not a name
      #     lookup in python.pkgs, determines the resolved closure.
      workspace = uv2nix.lib.workspace.loadWorkspace { workspaceRoot = src; };

      # (2) Overlay resolving every dependency at its uv.lock-pinned version.
      overlay = workspace.mkPyprojectOverlay { sourcePreference = "wheel"; };

      # (3) Relocated ADR-0011 runtime version stamp (spike-proven, bead
      #     pg2-r4cfy). The old builder substituted the PEP 440 build version into
      #     pyproject.toml/__init__.py in a buildPythonApplication preBuild; under
      #     mkVirtualEnv that hook is gone, so the substitution moves onto the ROOT
      #     project's own wheel build here. Digest is eval-time; the date is
      #     build-time. importlib.metadata normalizes leading zeros (26.07->26.7);
      #     __version__ keeps the literal — matching the previous builder exactly.
      versionOverlay = _final: prev: {
        ${pname} = prev.${pname}.overrideAttrs (old: {
          preBuild = (old.preBuild or "") + ''
            SECONDS_TODAY=$(( $(date +%s) % 86400 ))
            BUILD_VERSION=$(printf "%s.%05d+%s" "$(date +%y.%m.%d)" "$SECONDS_TODAY" "${srcDigest}")
            substituteInPlace pyproject.toml \
              --replace-fail 'version = "${versionPlaceholder}"' "version = \"$BUILD_VERSION\""
            ${lib.optionalString (versionInitFile != null) ''
              substituteInPlace ${versionInitFile} \
                --replace-fail '__version__ = "${versionPlaceholder}"' "__version__ = \"$BUILD_VERSION\""
            ''}
          '';
        });
      };

      # (4) Compose the python set: base build-systems + lock overlay + version stamp.
      pythonSet = (pkgs.callPackage pyproject-nix.build.packages { inherit python; }).overrideScope (
        lib.composeManyExtensions [
          pyproject-build-systems.overlays.default
          overlay
          versionOverlay
        ]
      );

      # (5) The virtualenv IS the resolved lock closure. Completeness is by
      #     construction (no name-match drop possible), so the old
      #     dontCheckRuntimeDeps knob is moot; a runtime import smoke proves it.
      venv = pythonSet.mkVirtualEnv "${pname}-env" workspace.deps.default;
    in
    # (6) Wrapper derivation. The nvd-visible per-source digest `version` (ADR
    #     0011) is stamped HERE, on the wrapper. src is unpacked (cwd = src) so
    #     the completions/man/tldr postInstall and consumer extraPostInstall keep
    #     using the same relative paths as the previous builder. runtimeDeps are
    #     wired onto PATH over the venv console script.
    # Deprecation nudge for the accepted no-op args (ADR 0022). Referencing them
    # here also keeps them "used" for deadnix without changing the arg contract.
    # Fires only when a consumer still passes a non-default value (support-apps
    # pre-cleanup); base's own fixtures pass none, so base stays quiet.
    lib.warnIf
      (
        customDeps != { }
        || pypiToNixNameMappings != { }
        || allowMissingDeps
        || extraNativeBuildInputs != [ ]
      )
      "mkPythonPackage(${name}): customDeps/pypiToNixNameMappings/allowMissingDeps/extraNativeBuildInputs are accepted no-ops under uv2nix (ADR 0022) and will be removed — drop them from this package's default.nix."
      (
        pkgs.stdenvNoCC.mkDerivation {
          inherit pname src;
          version = "${baseVersion}-${srcDigest}";

          dontConfigure = true;
          dontBuild = true;

          nativeBuildInputs = [
            pkgs.makeWrapper
            pkgs.help2man
          ];

          installPhase = ''
            runHook preInstall
            mkdir -p $out/bin
            makeWrapper ${venv}/bin/${name} $out/bin/${name} ${
              lib.optionalString (runtimeDeps != [ ]) "--prefix PATH : ${lib.makeBinPath runtimeDeps}"
            }
            runHook postInstall
          '';

          # Install completion scripts and generate a man page — unchanged from the
          # previous builder; cwd is the unpacked src.
          postInstall = ''
            mkdir -p $out/share/man/man1
            ${lib.optionalString hasCompletions ''
              mkdir -p $out/share/bash-completion/completions
              mkdir -p $out/share/zsh/site-functions
            ''}
            ${lib.optionalString hasTldr ''
              mkdir -p $out/share/tldr/pages.common
            ''}

            ${lib.optionalString hasCompletions ''
              if [ -f completions/${name}.bash ]; then
                cp completions/${name}.bash $out/share/bash-completion/completions/${name}
              fi
              if [ -f completions/_${name} ]; then
                cp completions/_${name} $out/share/zsh/site-functions/_${name}
              fi
            ''}

            export SOURCE_DATE_EPOCH=$(date +%s)
            ${pkgs.help2man}/bin/help2man --no-info \
              --name="${pyproject.project.description}" \
              $out/bin/${name} > $out/share/man/man1/${name}.1

            ${lib.optionalString hasTldr ''
              if [ -f ${name}.md ]; then
                cp ${name}.md $out/share/tldr/pages.common/
              fi
            ''}

            ${extraPostInstall}
          '';

          meta = with lib; {
            inherit (pyproject.project) description;
            platforms = platforms.darwin ++ platforms.linux;
            mainProgram = name;
          };
        }
      );
}
