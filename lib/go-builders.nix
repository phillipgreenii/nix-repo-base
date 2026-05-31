# mkGoBuilders — Go-equivalent of mkBashBuilders.
# Provides mkGoBinary for building Go applications via buildGoModule with
# standard postInstall (man page + completions) and a version contract that
# rejects the literal "dev".
{
  pkgs,
  self,
  lib ? pkgs.lib,
}:

rec {
  mkGoBinary =
    {
      name,
      src,
      version,
      vendorHash ? null,
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
    }:
    if version == "dev" then
      throw "mkGoBinary: version=\"dev\" is not allowed. Pass `phillipgreenii-nix-base.lib.mkVersion self` (or equivalent) to ensure a real version string."
    else
      pkgs.buildGoModule {
        pname = name;
        inherit src version vendorHash;
        ldflags = [ "-X main.Version=${version}" ];
        nativeBuildInputs = (lib.optional manPage pkgs.help2man) ++ [ pkgs.makeWrapper ];
        propagatedBuildInputs = runtimeDeps;
        nativeCheckInputs = testDeps;
        postInstall = ''
          ${lib.optionalString manPage ''
            mkdir -p $out/share/man/man1
            help2man --no-info --no-discard-stderr \
              --name="${description}" \
              $out/bin/${name} > $out/share/man/man1/${name}.1 || true
          ''}
          ${lib.optionalString completions.bash ''
            mkdir -p $out/share/bash-completion/completions
            $out/bin/${name} completion bash > $out/share/bash-completion/completions/${name} 2>/dev/null || true
          ''}
          ${lib.optionalString completions.zsh ''
            mkdir -p $out/share/zsh/site-functions
            $out/bin/${name} completion zsh > $out/share/zsh/site-functions/_${name} 2>/dev/null || true
          ''}
          ${lib.optionalString completions.fish ''
            mkdir -p $out/share/fish/vendor_completions.d
            $out/bin/${name} completion fish > $out/share/fish/vendor_completions.d/${name}.fish 2>/dev/null || true
          ''}
          ${extraPostInstall}
        '';
        meta = { inherit description; };
      };
}
