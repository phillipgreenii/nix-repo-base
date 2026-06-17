# mkGoBuilders — Go-equivalent of mkBashBuilders. Provides:
#   - mkGoApp: build a Go app whose version is keyed to its own source digest
#     and whose vendor FOD name is pinned, so editing source never re-vendors
#     and unrelated packages/commits never rebuild it (see nrb-n9f).
#   - mkGoBinary: opinionated wrapper over mkGoApp that also generates a man
#     page (help2man) and shell completions.
{
  pkgs,
  lib ? pkgs.lib,
  # `self` is accepted for signature parity with mkBashBuilders but unused here.
  ...
}:

rec {
  mkGoBinary =
    {
      name,
      src,
      vendorHash ? null,
      description ? "",
      runtimeDeps ? [ ],
      completions ? {
        bash = true;
        zsh = true;
        fish = true;
      },
      manPage ? true,
      testDeps ? [ ],
      extraPostInstall ? "",
      # Go linker target for the version string (mkGoApp injects it).
      versionPath ? "main.Version",
    }:
    # Delegate to mkGoApp for the per-source version + pinned vendor FOD; this
    # opinionated wrapper only layers on the man-page / completion postInstall.
    mkGoApp {
      pname = name;
      inherit src vendorHash versionPath;
      nativeBuildInputs = (lib.optional manPage pkgs.help2man) ++ [ pkgs.makeWrapper ];
      nativeCheckInputs = testDeps;
      postInstall = ''
        _try() {
          local label="$1"; shift
          local errfile
          errfile=$(mktemp)
          if ! "$@" 2> "$errfile"; then
            echo "WARN: $label failed:" >&2
            cat "$errfile" >&2
            rm -f "$errfile"
            return 1
          fi
          rm -f "$errfile"
          return 0
        }
        ${lib.optionalString manPage ''
          mkdir -p $out/share/man/man1
          _try "${name} help2man" \
            help2man --no-info --no-discard-stderr \
              --name="${description}" \
              --output=$out/share/man/man1/${name}.1 \
              $out/bin/${name} \
            || rm -f $out/share/man/man1/${name}.1
        ''}
        ${lib.optionalString completions.bash ''
          mkdir -p $out/share/bash-completion/completions
          _try "${name} completion bash" \
            sh -c "$out/bin/${name} completion bash > $out/share/bash-completion/completions/${name}" \
            || rm -f $out/share/bash-completion/completions/${name}
        ''}
        ${lib.optionalString completions.zsh ''
          mkdir -p $out/share/zsh/site-functions
          _try "${name} completion zsh" \
            sh -c "$out/bin/${name} completion zsh > $out/share/zsh/site-functions/_${name}" \
            || rm -f $out/share/zsh/site-functions/_${name}
        ''}
        ${lib.optionalString completions.fish ''
          mkdir -p $out/share/fish/vendor_completions.d
          _try "${name} completion fish" \
            sh -c "$out/bin/${name} completion fish > $out/share/fish/vendor_completions.d/${name}.fish" \
            || rm -f $out/share/fish/vendor_completions.d/${name}.fish
        ''}
        ${lib.optionalString (runtimeDeps != [ ]) ''
          wrapProgram $out/bin/${name} --suffix PATH : ${lib.makeBinPath runtimeDeps}
        ''}
        ${extraPostInstall}
      '';
      meta = { inherit description; };
    };

  # mkGoApp — build a Go application whose identity is keyed to its OWN source
  # rather than the whole flake. Unlike mkGoBinary (opinionated: forces a man
  # page + `<bin> completion` generation), this is a thin, unopinionated wrapper
  # that forwards every other buildGoModule argument untouched, so callers keep
  # their bespoke postInstall / subPackages / nativeBuildInputs / meta.
  #
  # Why it exists (nrb-n9f, refining nrb-c7a): the old convention threaded
  # `version = mkVersion self` into buildGoModule. That hashed the WHOLE repo,
  # so any edit or commit anywhere changed every package's version — which
  # renamed each `-go-modules` FOD and forced a full re-vendor + rebuild of all
  # Go packages on every apply (a ~26-minute apply that never cached). This
  # wrapper fixes both halves:
  #
  #   1. `version` is derived from THIS package's own `src` digest, so it
  #      changes iff the package's files change — giving per-edit `nvd` /
  #      `--version` attribution AND isolation (a sibling package, or a docs
  #      commit, never rebuilds it).
  #   2. the vendored modules (`-go-modules`) are pinned to a stable,
  #      version-independent name via buildGoModule's supported
  #      `passthru.overrideModAttrs` hook, so editing source never re-vendors.
  #      The output stays content-addressed by `vendorHash`; vendoring re-runs
  #      only when `vendorHash` changes — i.e. when dependencies change.
  mkGoApp =
    {
      pname,
      src,
      vendorHash ? null,
      # Go linker target for the version string. Defaults to lowercase
      # `main.version`; pass "main.Version" for packages that export it
      # capitalised.
      versionPath ? "main.version",
      # Human-facing base; the per-source digest is appended for uniqueness.
      baseVersion ? "0.0.0",
      ldflags ? [ ],
      # First-party modules pulled in via a local `replace => <relPath>` directive.
      # buildGoModule's `go mod vendor` copies these into the vendorHash-pinned
      # `-go-modules` FOD, which FREEZES them: the FOD is content-addressed by
      # (constant name, vendorHash), so it is never rebuilt on a source edit and the
      # build silently compiles a stale copy (or, after a GC, fails with a hash
      # mismatch) — every local edit otherwise forces a manual `vendorHash` bump.
      # See bead pg2-gjzz for the full write-up. To keep local code "live" we:
      #   1. strip each module from the FOD (so its hash tracks ONLY third-party
      #      deps and never drifts on a local edit), and
      #   2. overlay the current source back into `vendor/` at build time.
      # Each entry: { goImportPath = "github.com/org/mod"; relPath = "../mod"; }
      # where relPath is relative to modRoot (the build's working directory).
      localReplaceModules ? [ ],
      ...
    }@args:
    let
      # 8-char digest of the package's own (filtered) source tree via the shared
      # helper, so this digest changes iff a file included in `src` changes —
      # never for sibling packages. `src` may also be a list of paths (the helper
      # joins them); single-path behaviour is identical to the old inline form.
      # See ADR 0006.
      version = "${baseVersion}-${(import ./version.nix).mkSrcDigest src}";
      forwarded = builtins.removeAttrs args [
        "versionPath"
        "baseVersion"
        "ldflags"
        "version"
        "vendorHash"
        "localReplaceModules"
      ];
      # (1) Drop the local module's contents from the FOD, AFTER `go mod vendor`
      #     has run (cwd = modRoot, vendor/ still local) and BEFORE it is copied to
      #     $out. vendor/modules.txt keeps listing the module; only its files go,
      #     so the FOD hash no longer depends on first-party source.
      stripLocalFromModules = lib.concatMapStringsSep "\n" (m: ''
        rm -rf "vendor/${m.goImportPath}"
      '') localReplaceModules;
      # (2) After buildGoModule's configurePhase copies the FOD into ./vendor,
      #     overlay the live source so the build always sees current local code.
      #     The FOD is copied read-only from the store, so make vendor/ writable
      #     first (else creating the overlaid dir fails with EACCES). The per-module
      #     chmod + go.mod/go.sum strip keeps the copy a valid vendored dep (vendored
      #     modules must not carry their own module files).
      overlayLocalModules = ''
        chmod -R u+w vendor
      ''
      + lib.concatMapStringsSep "\n" (m: ''
        rm -rf "vendor/${m.goImportPath}"
        mkdir -p "$(dirname "vendor/${m.goImportPath}")"
        cp -r --reflink=auto "${m.relPath}" "vendor/${m.goImportPath}"
        chmod -R u+w "vendor/${m.goImportPath}"
        rm -f "vendor/${m.goImportPath}/go.mod" "vendor/${m.goImportPath}/go.sum"
      '') localReplaceModules;
    in
    pkgs.buildGoModule (
      forwarded
      // {
        inherit version vendorHash;
        ldflags = ldflags ++ [ "-X ${versionPath}=${version}" ];
        passthru = (args.passthru or { }) // {
          overrideModAttrs =
            _:
            {
              name = "${pname}-go-modules";
            }
            // lib.optionalAttrs (localReplaceModules != [ ]) {
              postBuild = stripLocalFromModules;
            };
        };
      }
      // lib.optionalAttrs (localReplaceModules != [ ]) {
        postConfigure = (args.postConfigure or "") + "\n" + overlayLocalModules;
      }
    );
}
