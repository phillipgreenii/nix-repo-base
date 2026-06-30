# Pure string-builders for consistent system.activationScripts output.
# Spec: docs/superpowers/specs/2026-06-29-activation-script-output-consistency-design.md
_:
let
  # POSIX single-quote escaping (no lib dependency).
  esc = s: "'" + (builtins.replaceStrings [ "'" ] [ "'\\''" ] s) + "'";

  # Bash defining act_* plus color/glyph detection. Idempotent: safe to emit
  # multiple times in one shell (last definition wins). Also injected verbatim
  # into child scripts that run in their own process (see beads-dolt).
  #
  # Single source of truth: the bash lives in ./activation/activation-lib.bash
  # so the same act_* source feeds both this inline string (system
  # activationScripts) and a consumer-built mkBashLibrary (home.activation /
  # standalone sourceable scripts). readFile keeps them byte-identical.
  activationHelpers = builtins.readFile ./activation/activation-lib.bash;

  mkActivationSection =
    {
      tag,
      headline ? null,
      body,
    }:
    let
      header = if headline == null then "[${tag}]" else "[${tag}] ${headline}";
    in
    ''
      ${activationHelpers}
      printf '%s\n' ${esc header}
      ${body}
      printf '\n'
    '';
in
{
  inherit activationHelpers mkActivationSection;
}
