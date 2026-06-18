# Heavy-upstream overlay module. Consumers must declare inputs.llm-agents.
{ inputs, ... }:
{
  flake.overlays.llm-agents = _final: prev: {
    llm-agentsPkgs = inputs.llm-agents.packages.${prev.stdenv.hostPlatform.system};
  };

  config.phillipgreenii.alignment.requires = [ "llm-agents" ];
}
