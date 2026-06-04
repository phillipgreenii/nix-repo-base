# mkGoBuilders — Go-equivalent of mkBashBuilders.
# Provides mkGoBinary for building Go applications via buildGoModule with
# standard postInstall (man page + completions) and a version contract that
# rejects any "dev" sentinel value.
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
    if version == "dev" || lib.strings.hasSuffix "-dev" version then
      throw "mkGoBinary: version=\"${version}\" indicates a build outside a git checkout. Pass `phillipgreenii-nix-base.lib.mkVersion self` from inside a clean or dirty git tree to ensure a real version string."
    else
      pkgs.buildGoModule {
        pname = name;
        inherit src version vendorHash;
        ldflags = [ "-X main.Version=${version}" ];
        nativeBuildInputs = (lib.optional manPage pkgs.help2man) ++ [ pkgs.makeWrapper ];
        nativeCheckInputs = testDeps;
        postInstall = ''
          _try() {
            local label="$1"; shift
            local errfile
            errfile=$(mktemp)
            if ! "$@" 2> "$errfile"; then
              echo "WARN: $label failed:" >&2
              cat "$errfile" >&2
              rm -f "$errfile"
              return 1
            fi
            rm -f "$errfile"
            return 0
          }
          ${lib.optionalString manPage ''
            mkdir -p $out/share/man/man1
            _try "${name} help2man" \
              help2man --no-info --no-discard-stderr \
                --name="${description}" \
                --output=$out/share/man/man1/${name}.1 \
                $out/bin/${name} \
              || rm -f $out/share/man/man1/${name}.1
          ''}
          ${lib.optionalString completions.bash ''
            mkdir -p $out/share/bash-completion/completions
            _try "${name} completion bash" \
              sh -c "$out/bin/${name} completion bash > $out/share/bash-completion/completions/${name}" \
              || rm -f $out/share/bash-completion/completions/${name}
          ''}
          ${lib.optionalString completions.zsh ''
            mkdir -p $out/share/zsh/site-functions
            _try "${name} completion zsh" \
              sh -c "$out/bin/${name} completion zsh > $out/share/zsh/site-functions/_${name}" \
              || rm -f $out/share/zsh/site-functions/_${name}
          ''}
          ${lib.optionalString completions.fish ''
            mkdir -p $out/share/fish/vendor_completions.d
            _try "${name} completion fish" \
              sh -c "$out/bin/${name} completion fish > $out/share/fish/vendor_completions.d/${name}.fish" \
              || rm -f $out/share/fish/vendor_completions.d/${name}.fish
          ''}
          ${lib.optionalString (runtimeDeps != [ ]) ''
            wrapProgram $out/bin/${name} --suffix PATH : ${lib.makeBinPath runtimeDeps}
          ''}
          ${extraPostInstall}
        '';
        meta = { inherit description; };
      };
}
