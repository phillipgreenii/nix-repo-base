# Package building helpers
# Provides mkManPage, mkBashBuilders, mkGoBuilders
_: {
  # Generate man page for a command using help2man
  # Usage: mkManPage {
  #   pkgs = pkgs;
  #   name = "gh-zpr";
  #   command = "${package}/bin/gh-zpr";
  #   version = "1.0.0";
  #   description = "GitHub Pull Request Review Extension";
  #   includeFile = ./gh-zpr.h2m;  # optional
  # }
  mkManPage =
    {
      pkgs,
      name,
      command,
      version,
      description,
      includeFile ? null,
    }:
    pkgs.runCommand "${name}-man"
      {
        nativeBuildInputs = [ pkgs.help2man ];
      }
      ''
        mkdir -p $out/share/man/man1
        help2man --no-info \
          --name="${description}" \
          --version-string="${version}" \
          ${if includeFile != null then "--include=${includeFile}" else ""} \
          ${command} > $out/share/man/man1/${name}.1
      '';

  # Factory for bash script packaging builders (mkBashLibrary, mkBashScript, mkBashModule)
  # Usage: bashBuilders = mkBashBuilders { inherit pkgs lib self; };
  mkBashBuilders = import ../lib/bash-builders.nix;

  # Factory for Go binary packaging (mkGoBinary)
  # Usage: goBuilders = mkGoBuilders { inherit pkgs self; };
  mkGoBuilders = import ../lib/go-builders.nix;

  # Factory for Claude Code marketplace packaging
  # (mkClaudePlugin, mkClaudeMarketplace, mkDirectoryMarketplaceSettings).
  # Usage: mp = mkClaudeMarketplaceBuilders { inherit pkgs lib; };
  # See docs/claude-marketplaces.md and ADR-0010.
  mkClaudeMarketplaceBuilders = import ../lib/claude-marketplace.nix;
}
