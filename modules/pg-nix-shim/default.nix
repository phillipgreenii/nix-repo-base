# pg-nix-shim: two symlinks (bin/nix, bin/darwin-rebuild) onto the one
# pg-nix-log-wrapped binary. The wrapper runs shim mode when
# basename(argv[0]) is nix or darwin-rebuild. No Go build, no ldflags, no baked
# values. Design: docs/superpowers/specs/2026-11-04-pg-nix-shim-design.md.
{ runCommand, pg-nix-log-wrapped }:

runCommand "pg-nix-shim"
  {
    meta.description = "PATH shim: nix and darwin-rebuild symlinks onto pg-nix-log-wrapped";
  }
  ''
    mkdir -p $out/bin
    ln -s ${pg-nix-log-wrapped}/bin/pg-nix-log-wrapped $out/bin/nix
    ln -s ${pg-nix-log-wrapped}/bin/pg-nix-log-wrapped $out/bin/darwin-rebuild
  ''
