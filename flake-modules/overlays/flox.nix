# Heavy-upstream overlay module. Consumers must declare inputs.flox.
{ inputs, ... }:
{
  flake.overlays.flox =
    _final: prev:
    let
      system = prev.stdenv.hostPlatform.system;

      # inputs.flox.packages.${system} (flox/flox's own `packages` output) bakes
      # in a hard dependency on nixpkgs' nix-functional-tests suite: flox's
      # pkgs/nix/default.nix overrides nixComponents_2_31 with a CVE-patched
      # `src`/`version` (2.31.5) to fix 3 published Nix security advisories, and
      # that revision isn't published to cache.flox.dev, so every consumer
      # builds it from source with doCheck's default (true) pulling in the full
      # functional-test suite as a hard build dependency. That suite is
      # path-length sensitive -- several tests (build-remote-trustless-*,
      # shell) URL-encode the sandbox build directory into a single lock-file
      # name and hit ENAMETOOLONG whenever that path is long, which is a
      # nix-daemon build-dir-naming detail unrelated to whether the patched
      # Nix itself is correct. Confirmed via `nix log` on the failing
      # nix-functional-tests-2.31.5.drv (tc-support deploy on monorepod,
      # 2026-08-24).
      #
      # We rebuild flox's own overlay chain ourselves (deps -> our doCheck
      # override -> flox -> development, mirroring flox's own overlays.default)
      # on top of flox's OWN nixpkgs pin (inputs.flox.inputs.nixpkgs) so we can
      # disable checks on flox's `nix` component before overlays.flox wires it
      # into flox/flox-cli. Using flox's own nixpkgs pin (not a follows
      # override) keeps every other package eligible for cache.flox.dev
      # substitution -- see "Consumer input alignment" in CLAUDE.md for why
      # flox's nixpkgs MUST NOT be follows-overridden.
      floxNixpkgs = inputs.flox.inputs.nixpkgs.legacyPackages.${system};

      floxPkgsBase = floxNixpkgs.extend (
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
      floxPkgs = floxPkgsBase;
    };

  phillipgreenii.alignment.requires = [ "flox" ];
}
