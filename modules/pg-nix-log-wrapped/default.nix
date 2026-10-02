# Build the pg-nix-log-wrapped binary via mkGoBuilders.
#
# A sibling Go module (own go.mod / gomod2nix.toml), NOT a second cmd/ inside
# modules/pn: pn consumes only the binary path (phillipgreenii.pn.telemetry.
# wrapperPath), never a Go import. See docs/adr/0030-pg-nix-log-wrapped.md.
{
  pkgs,
  self,
}:

let
  goBuilders = (import ../../lib/go-builders.nix) { inherit pkgs self; };
in
goBuilders.mkGoBinary {
  name = "pg-nix-log-wrapped";
  src = ./.;
  description = "Runs a nix command unchanged and turns its json-log-path stream into OpenTelemetry spans and metrics";
  gomod2nixToml = ./gomod2nix.toml;

  # It is a thin process wrapper, not a cobra CLI: no `completion` subcommand
  # exists to generate from, and help2man over the one-screen --help adds
  # nothing the tldr page and README do not already say.
  manPage = false;
  completions = {
    bash = false;
    zsh = false;
    fish = false;
  };

  # tldr decision (ADR 0030): SHIP a page. The command is user-facing (agents
  # and humans invoke it directly, and `--check` is the diagnostic for a
  # silently fail-open setup). Registering it with `programs.tldr.customPages`
  # is a home-manager concern and belongs to the module that installs the
  # package (the pn telemetry option, bead pg2-kqrrs.5/.9), not to this build.
  extraPostInstall = ''
    mkdir -p $out/share/tldr/pages.common
    cp ${./pg-nix-log-wrapped.md} $out/share/tldr/pages.common/pg-nix-log-wrapped.md
  '';

  # Deliberately NO runtimeDeps: mkGoBinary would wrapProgram the binary in a
  # shell script, which breaks the wrapper's own-inode PATH check (W-3) and
  # adds a process hop in front of every nix call. The wrapper resolves CMD
  # from the caller's PATH, and its tests that need nix skip when it is absent.
}
