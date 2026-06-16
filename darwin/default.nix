# Aggregate darwin module set for phillipg-nix-repo-base, exported as
# darwinModules.default (mirrors phillipgreenii-nix-agent-support's
# `darwinModules.default = ./darwin`). A machine flake imports this once and
# gets every per-app darwin module; add new modules to the imports list below.
{ ... }:
{
  imports = [
    ./modules/pn
  ];
}
