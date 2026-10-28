# Heavy-upstream overlay module. Consumers must declare inputs.flox.
#
# Two attributes, one DEFAULT and one explicit opt-in FALLBACK (bead pg2-y1z3fk):
#
#   pkgs.floxPkgs            DEFAULT. inputs.flox.packages.${system}, i.e. flox's own
#                            `packages` output (use `pkgs.floxPkgs.flox`). Its closure
#                            is exactly what flox's CI pushes to cache.flox.dev, so
#                            consumers substitute it instead of compiling GHC, cargo,
#                            nix-main and openjpeg whenever flox moves.
#   pkgs.floxPkgsNoChecks    OPT-IN FALLBACK. flox's overlay chain rebuilt on flox's own
#                            nixpkgs pin with `nix` check phases disabled (below). A
#                            full nixpkgs-shaped set (use `pkgs.floxPkgsNoChecks.flox`).
#                            Lazy: costs nothing unless a consumer dereferences it.
#
# Trade-off (why the default changed). Until 2026-10-28 `floxPkgs` WAS the rebuilt
# chain. Disabling checks on `nix` changes nix's derivation, so flox, flox-interpreter,
# shellcheck, pandoc-cli and ghc-9.10.3 (plus cargo, nix-main-2.31.6, openjpeg)
# all got new store paths that no cache has: every flox bump compiled GHC from source
# (pg2-ukd7y: 745 and 423 derivations, one 8-hour build). The default now trades that
# build cost for dependence on cache.flox.dev serving the nix revision flox pins.
#
# Why the override exists (and what the default gives up). On 2026-08-24 the
# CVE-patched nix 2.31.5 that flox pins (flox's pkgs/nix/default.nix overrides
# nixComponents_2_31 with a patched `src`/`version`) was NOT on cache.flox.dev, so
# every consumer built it from source with doCheck's default (true), pulling nixpkgs'
# nix-functional-tests suite in as a hard build dependency. That suite is
# path-length sensitive: several tests (build-remote-trustless-*, shell) URL-encode
# the sandbox build directory into one lock-file name and hit ENAMETOOLONG whenever
# that path is long, a nix-daemon build-dir-naming detail unrelated to whether the
# patched Nix is correct (confirmed via `nix log` on nix-functional-tests-2.31.5.drv,
# tc-support deploy on monorepod, 2026-08-24). The default has NO protection against
# that: if cache.flox.dev ever stops serving the nix revision flox pins, flox's
# closure builds that nix from source WITH its functional tests and can fail the same
# way.
#
# Detecting the uncached case. Before bumping the flox input, probe flox's own output
# for every platform you consume, for example
#   nix path-info --store https://cache.flox.dev \
#     "$(nix eval --raw github:flox/flox#packages.<system>.default.outPath)"
# or dry-run the system build and look for nix-main / ghc / cargo in the to-be-built
# list. A miss means the default will compile from source.
#
# Fallback when the probe misses. Point the consumer at the opt-in attribute (one
# line, e.g. `pkgs.floxPkgsNoChecks.flox` in the home module that installs flox).
# That keeps the doCheck = false chain: the rebuild is slow (the heavy toolchain is
# compiled) but cannot fail on ENAMETOOLONG. Switch back to `floxPkgs` once
# cache.flox.dev serves the nix revision again. The wiring of both attributes is
# covered by `checks.<system>.flox-overlay-eval` (eval-only, stubbed flox input).
{ inputs, ... }:
{
  flake.overlays.flox =
    _final: prev:
    let
      system = prev.stdenv.hostPlatform.system;

      # FALLBACK chain. We rebuild flox's own overlay chain ourselves (deps -> our
      # doCheck override -> flox -> development, mirroring flox's own
      # overlays.default) on top of flox's OWN nixpkgs pin (inputs.flox.inputs.nixpkgs)
      # so we can disable checks on flox's `nix` component before overlays.flox wires
      # it into flox/flox-cli. Using flox's own nixpkgs pin (not a follows override)
      # keeps every other package eligible for cache.flox.dev substitution -- see
      # "Consumer input alignment" in CLAUDE.md for why flox's nixpkgs MUST NOT be
      # follows-overridden.
      floxNixpkgs = inputs.flox.inputs.nixpkgs.legacyPackages.${system};

      floxPkgsNoChecks = floxNixpkgs.extend (
        prev.lib.composeManyExtensions [
          inputs.flox.overlays.deps
          # `nix` here is nixpkgs' `nix-everything` aggregate (pkgs/tools/package-management/
          # nix/modular/packaging/everything.nix): doCheck/checkInputs are literal attrs on
          # ITS OWN mkDerivation call, not derived from its component arguments, so only
          # .overrideAttrs on the aggregate itself removes nix-functional-tests from
          # checkInputs. .overrideAllMesonComponents (nixpkgs' other override method here)
          # only rewrites the individual sub-component derivations it is built from and
          # leaves the aggregate's own `doCheck = true;`/`checkInputs = [ ... ];` literals
          # untouched -- verified empirically: it still produced nix-functional-tests as a
          # build input. Confirmed fix with `.overrideAttrs`: recursively dumping flox's
          # full derivation closure (`nix derivation show -r` on the resulting flox .drv)
          # shows zero remaining references to nix-functional-tests.
          (_finalFlox: prevFlox: {
            nix = prevFlox.nix.overrideAttrs (_old: {
              doCheck = false;
              doInstallCheck = false;
            });
          })
          inputs.flox.overlays.flox
          inputs.flox.overlays.development
        ]
      );
    in
    {
      # DEFAULT: flox's own packages output, so its closure matches cache.flox.dev.
      floxPkgs = inputs.flox.packages.${system};
      # OPT-IN FALLBACK: see the header.
      inherit floxPkgsNoChecks;
    };

  phillipgreenii.alignment.requires = [ "flox" ];
}
