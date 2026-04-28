# Reusable check patterns for flake checks
# This module provides common patterns to reduce duplication in flake.nix
{
  pkgs,
  ...
}:
{
  # Check Nix formatting with nixfmt-rfc-style
  formatting =
    root:
    pkgs.runCommand "check-formatting"
      {
        nativeBuildInputs = [ pkgs.nixfmt-rfc-style ];
      }
      ''
        cd ${root}
        nixfmt --check .
        touch $out
      '';

  # Check Nix linting with statix
  linting =
    root:
    pkgs.runCommand "check-linting"
      {
        nativeBuildInputs = [ pkgs.statix ];
      }
      ''
        statix check ${root}
        touch $out
      '';

  # Check shell scripts with shellcheck
  # Unified version supporting both exclude and allowWarnings patterns
  # Usage: shellcheck { scripts = [ ./path/to/script.sh ... ]; exclude = [ "SC2086" ]; allowWarnings = true; }
  shellcheck =
    {
      scripts,
      exclude ? [ ],
      allowWarnings ? false,
    }:
    let
      excludeArgs = if exclude != [ ] then "-e ${pkgs.lib.concatStringsSep "," exclude}" else "";
      # If allowWarnings is true, add "|| true" to prevent failures on warnings
      errorHandling = if allowWarnings then " || true" else "";
    in
    pkgs.runCommand "check-shellcheck"
      {
        nativeBuildInputs = [ pkgs.shellcheck ];
      }
      ''
        # Shellcheck all provided scripts
        ${pkgs.lib.concatMapStringsSep "\n" (
          script: "${pkgs.shellcheck}/bin/shellcheck ${excludeArgs} ${script}${errorHandling}"
        ) scripts}
        touch $out
      '';

  # Test bash scripts using BATS
  # Usage: testBashScripts { package = self.packages.${system}.my-package; tests = ./tests; extraInputs = [ pkgs.gh ]; }
  testBashScripts =
    {
      package,
      tests,
      extraInputs ? [ ],
    }:
    pkgs.runCommand "test-bash-scripts"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.git
          pkgs.which
          package
        ]
        ++ extraInputs;
      }
      ''
        export PATH="${package}/bin:$PATH"
        # All tests must pass
        bats ${tests}
        touch $out
      '';

  # Test Python project with check-all.sh
  # This handles the source copy and coverage suppression needed for Nix sandbox
  # Usage: testPythonProject { package = ...; src = ./packages/my-app; name = "my-app"; }
  # Optional: checkLibDir = ./lib/python-checks; (for shared check-lib.sh)
  testPythonProject =
    {
      package,
      src,
      name,
      checkLibDir ? null,
    }:
    pkgs.runCommand "test-${name}"
      {
        nativeBuildInputs = [
          pkgs.bash
          pkgs.uv
          package
        ];
        inherit src;
      }
      ''
        # Set up writable cache directory for uv
        export HOME=$TMPDIR
        export UV_CACHE_DIR=$TMPDIR/uv-cache
        mkdir -p $UV_CACHE_DIR

        ${pkgs.lib.optionalString (checkLibDir != null) ''
          export CHECK_LIB_DIR="${checkLibDir}"
        ''}

        # Copy source to writable directory
        cp -r $src ${name}
        cd ${name}
        chmod +w -R .

        # Run check-all.sh - all tests must pass
        bash check-all.sh --no-fix --quick --suppress-coverage-check
        touch $out
      '';

  testUpdateLocksLib =
    {
      testsDir ? ../lib/tests,
      scriptsDir ? ../lib/scripts,
    }:
    pkgs.runCommand "test-update-locks-lib"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.bash
          pkgs.coreutils
          pkgs.git
        ];
      }
      ''
        export PATH="${
          pkgs.lib.makeBinPath [
            pkgs.coreutils
            pkgs.bash
            pkgs.git
          ]
        }:$PATH"
        export UL_LIB_SCRIPTS_DIR="${scriptsDir}"
        export HOME="$TMPDIR/test-home"
        mkdir -p "$HOME"
        bats ${testsDir}
        touch $out
      '';
}
