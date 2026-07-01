{ lib }:
let
  act = import ./activation.nix { };
  section = act.mkActivationSection {
    tag = "demo";
    headline = "doing things";
    body = ''act_ok "did a thing"'';
  };
  noHeadline = act.mkActivationSection {
    tag = "demo";
    body = "";
  };
in
{
  testHeaderWithHeadline = {
    expr = lib.hasInfix "printf '%s\\n' '[demo] doing things'" section;
    expected = true;
  };
  testHeaderNoHeadline = {
    expr = lib.hasInfix "printf '%s\\n' '[demo]'" noHeadline;
    expected = true;
  };
  testHelpersInlined = {
    expr = lib.hasInfix "act_ok()" section && lib.hasInfix "act_fail()" section;
    expected = true;
  };
  testDetailHelperInlined = {
    expr = lib.hasInfix "act_detail()" section;
    expected = true;
  };
  testDetailIndentTwoSpace = {
    # act_detail aligns to the glyph column (2 spaces), not act_info's 4.
    expr = lib.hasInfix ''act_detail() { printf '%s\n' "  $*"; }'' act.activationHelpers;
    expected = true;
  };
  testPrintfSafeForm = {
    expr = lib.hasInfix "printf '%s\\n'" act.activationHelpers;
    expected = true;
  };
  testAsciiMarkersPresent = {
    expr = lib.hasInfix "[OK]   " act.activationHelpers && lib.hasInfix "[WARN] " act.activationHelpers;
    expected = true;
  };
  testColorGuards = {
    # Policy: color defaults ON; NO_COLOR is the only off-switch. The color
    # decision must NOT dereference CLICOLOR_FORCE — it cannot survive
    # nix-darwin's `env -i` system activation, so consulting it there is
    # pointless. We assert the code form "CLICOLOR_FORCE:-" is absent (that
    # substring only occurs in the `${CLICOLOR_FORCE:-}` read, never in prose,
    # so an explanatory comment mentioning the var by name stays allowed).
    expr =
      lib.hasInfix "NO_COLOR" act.activationHelpers
      && !(lib.hasInfix "CLICOLOR_FORCE:-" act.activationHelpers);
    expected = true;
  };
  testHelpersIsString = {
    expr = builtins.isString act.activationHelpers;
    expected = true;
  };
  # activationHelpers is the single source of truth sourced from the standalone
  # .bash file via readFile, not an inline Nix string.
  testHelpersSourcedFromFile = {
    expr = act.activationHelpers == builtins.readFile ./activation/activation-lib.bash;
    expected = true;
  };
}
