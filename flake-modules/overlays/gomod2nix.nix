# Light-upstream overlay module. Closes over the producer's gomod2nix input.
# Consumers import this module and apply self.overlays.gomod2nix to their
# pkgs. Consumers do NOT need to declare gomod2nix themselves.
producerInputs: _: {
  flake.overlays.gomod2nix = producerInputs.gomod2nix.overlays.default;
}
