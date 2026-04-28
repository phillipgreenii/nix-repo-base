{ pkgs, ... }:
{
  projectRootFile = "flake.nix";
  programs.nixfmt.enable = true;
  programs.nixfmt.package = pkgs.nixfmt-rfc-style;
  programs.prettier.enable = true;
  programs.prettier.includes = [
    "*.md"
    "*.yaml"
    "*.yml"
    "*.json"
  ];
  programs.shellcheck.enable = true;
  programs.shfmt.enable = true;
  programs.shfmt.indent_size = 2;
}
