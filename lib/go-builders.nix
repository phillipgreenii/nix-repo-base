# mkGoBuilders — Go-equivalent of mkBashBuilders. Provides:
#   - mkGoApp: build a Go app via gomod2nix (buildGoApplication), keyed to its
#     own source digest (ADR 0006) so editing source gives per-edit `--version`
#     attribution without rebuilding sibling packages. Dependencies are vendored
#     as per-module content-addressed FODs read from a committed `gomod2nix.toml`
#     (ADR 0008), so there is no `vendorHash` to bump and no whole-repo FOD to
#     invalidate.
#   - mkGoBinary: opinionated wrapper over mkGoApp that also generates a man
#     page (help2man) and shell completions.
{
  pkgs,
  lib ? pkgs.lib,
  # `self` is accepted for signature parity with mkBashBuilders but unused here.
  ...
}:
assert
  pkgs ? buildGoApplication
  || throw ''
    mkGoBuilders requires pkgs.buildGoApplication (gomod2nix's overlay).
    Either:
      - Import phillipgreenii-nix-base.flakeModules.gomod2nix-overlay and
        apply self.overlays.gomod2nix to your pkgs, OR
      - Declare inputs.gomod2nix directly and apply
        inputs.gomod2nix.overlays.default to your pkgs.
  '';
rec {
  mkGoBinary =
    {
      name,
      src,
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
      # gomod2nix engine (ADR 0008): mkGoBinary is a CLOSED arg set (no `...`)
      # and calls mkGoApp with an explicit attr list, so these must be threaded
      # explicitly or pn cannot reach mkGoApp. `gomod2nixToml` is required by
      # mkGoApp; pn passes it.
      gomod2nixToml,
      modRoot ? null,
      # Optional list of Go package import paths to build (buildGoApplication's
      # `subPackages`). Defaults to null → build every `main` in the module (the
      # historical behavior). Set it to pin the derivation's bin/ to specific
      # entrypoints when the module carries more than one `cmd/*` main.
      subPackages ? null,
    }:
    # Delegate to mkGoApp for the per-source version + gomod2nix build; this
    # opinionated wrapper only layers on the man-page / completion postInstall.
    mkGoApp (
      {
        pname = name;
        inherit
          src
          versionPath
          gomod2nixToml
          modRoot
          ;
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
        # mkGoBinary installs a single exe at `$out/bin/${name}` by construction,
        # so mainProgram = name is unconditionally correct (fixes the getExe
        # mainProgram-warning class at the builder layer — see bead pg2-148y).
        meta = {
          inherit description;
          mainProgram = name;
        };
      }
      // lib.optionalAttrs (subPackages != null) { inherit subPackages; }
    );

  # mkGoApp — build a Go application via gomod2nix (buildGoApplication), keyed to
  # its OWN source rather than the whole flake. Unlike mkGoBinary (opinionated:
  # forces a man page + `<bin> completion` generation), this is a thin,
  # unopinionated wrapper that forwards every other buildGoApplication argument
  # untouched, so callers keep their bespoke postInstall / subPackages /
  # nativeBuildInputs / meta.
  #
  # Two halves (ADR 0006 + ADR 0008):
  #   1. `version` is derived from THIS package's own `src` digest, so it
  #      changes iff the package's files change — giving per-edit `nvd` /
  #      `--version` attribution AND isolation (a sibling package, or a docs
  #      commit, never rebuilds it).
  #   2. Dependencies are vendored as per-module content-addressed FODs read
  #      from a committed `gomod2nix.toml` beside `go.mod`. There is no
  #      `vendorHash` to bump and no monolithic vendor FOD to invalidate; first-
  #      party local `replace => ../sibling` modules are a native path dep read
  #      live from source. To add/bump a dep: `go get` + `go mod tidy` +
  #      `nix run github:nix-community/gomod2nix -- generate`, then commit the toml.
  #
  # Usage (ADR 0008 §"The pattern"). The committed `gomod2nix.toml` always sits
  # beside `go.mod`; the consumer never names it — mkGoApp derives `pwd` and
  # passes `modules = pwd + "/gomod2nix.toml"`.
  #
  #   Pattern A — single module at the package root (the common case):
  #     mkGoApp {
  #       pname = "<name>";
  #       src = lib.cleanSource ./.;   # go.mod + gomod2nix.toml at the root
  #       subPackages = [ "cmd/<name>" ];
  #       # NO modRoot. mkGoApp sets pwd = src.
  #     }
  #
  #   Pattern B — a local `replace => ../sibling` (pa-monitor, ccpool, pr-pool):
  #     mkGoApp {
  #       pname = "<name>";
  #       # Root src at the PARENT so the sibling is inside ONE store tree.
  #       src = lib.fileset.toSource {
  #         root = ./..;
  #         fileset = lib.fileset.unions [ ./. ../<sibling> ];
  #       };
  #       modRoot = "<name>";          # mkGoApp sets pwd = src + "/<name>"
  #       subPackages = [ "cmd/<name>" ];
  #     }
  #
  # The local-replace symlink in Pattern B resolves because `pwd` and the sibling
  # live in the same rooted store copy.
  mkGoApp =
    {
      pname,
      src,
      # Go linker target for the version string. Defaults to lowercase
      # `main.version`; pass "main.Version" for packages that export it
      # capitalised.
      versionPath ? "main.version",
      # Human-facing base; the per-source digest is appended for uniqueness.
      baseVersion ? "0.0.0",
      ldflags ? [ ],
      # Committed gomod2nix lockfile, conventionally ./gomod2nix.toml beside
      # go.mod (REQUIRED). mkGoApp resolves its location from `pwd`, so callers
      # pass the toml only to signal it is committed — its actual path is derived.
      gomod2nixToml,
      modRoot ? null,
      ...
    }@args:
    let
      # 8-char digest of the package's own (filtered) source tree via the shared
      # helper, so this digest changes iff a file included in `src` changes —
      # never for sibling packages. `src` may also be a list of paths (the helper
      # joins them). See ADR 0006.
      version = "${baseVersion}-${(import ./version.nix).mkSrcDigest src}";

      # `pwd` carries module/replace resolution: it is `src` in Pattern A and
      # `src + "/" + modRoot` in Pattern B (where the sibling replace resolves
      # within the same rooted store copy). `modRoot` stays in `forwarded` so
      # buildGoApplication uses it as the build working dir — it is intentionally
      # NOT stripped.
      pwd = if modRoot != null then src + "/" + modRoot else src;
      # Derive meta.mainProgram = baseNameOf (head subPackages) ONLY when exactly
      # one subPackage is built — then the installed exe name is known. For a null
      # or multi-entry subPackages we do not guess (bead pg2-148y). A caller's
      # explicit meta.mainProgram always wins via the merge below.
      derivedMainProgram =
        let
          sp = args.subPackages or null;
        in
        if sp != null && builtins.length sp == 1 then baseNameOf (builtins.head sp) else null;
      # `meta` is reassembled explicitly (default merged BENEATH args.meta so a
      # caller-supplied mainProgram wins), so strip it from `forwarded` to avoid
      # passing it twice into buildGoApplication.
      forwarded = builtins.removeAttrs args [
        "pname"
        "versionPath"
        "baseVersion"
        "ldflags"
        "version"
        "gomod2nixToml"
        "meta"
      ];
    in
    # `gomod2nixToml` is required (the committed lockfile must exist beside
    # go.mod) but its location is derived from `pwd`, so it is asserted rather
    # than threaded by value.
    assert gomod2nixToml != null;
    pkgs.buildGoApplication (
      forwarded
      // {
        inherit pname version pwd;
        inherit (pkgs) go; # pin to our nixpkgs Go, not gomod2nix's
        modules = pwd + "/gomod2nix.toml";
        ldflags = ldflags ++ [ "-X ${versionPath}=${version}" ];
        # Default mainProgram merged BENEATH the caller's meta so an explicit
        # meta.mainProgram always wins (last-wins). No key added when nothing was
        # derived (multi-main / no subPackages) and the caller set no meta.
        meta =
          (lib.optionalAttrs (derivedMainProgram != null) { mainProgram = derivedMainProgram; })
          // (args.meta or { });
      }
    );
}
