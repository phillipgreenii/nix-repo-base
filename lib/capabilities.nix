# Capability-authoring helpers for the "Light" capability model (Plan 5).
#
# Exported on the flake `lib` so capability modules in the consuming flakes stay
# one-liners and cannot forget the load-bearing conventions (feature gating,
# isHuman gating, development subscription, additive-only bundles).
#
# INVARIANTS these helpers enforce by construction:
#   * A capability only ever SETS feature flags at `mkDefault true` — never
#     `mkDefault false`. Human-only features use `mkIf (isHuman)`, so a non-human
#     account gets NO definition rather than a `mkDefault false` (which could
#     conflict with another setter and crash eval). This is the additive-only
#     rule that keeps overlapping bundles safe.
#   * Conditionals READ feature flags; capabilities SET them. These helpers never
#     read `capabilities.*`/`bundles.*` to gate a feature.
{ lib }:
let
  featureEnable = f: { phillipgreenii.programs.${f}.enable = lib.mkDefault true; };
in
rec {
  # A config fragment enabling `feature` (at mkDefault) whenever `cond` holds, and
  # contributing NOTHING otherwise (no mkDefault false). Use for one-off gates
  # inside a hand-written capability/integration module.
  enableFeatureIf = cond: feature: lib.mkIf cond (featureEnable feature);

  # A leaf capability module.
  #   name                    -> declares phillipgreenii.capabilities.<name>.enable
  #   features                -> enabled for ANY account when the capability is on
  #   humanFeatures           -> enabled only for human accounts (isHuman)
  #   subscribesToDevelopment -> self-subscribe to bundles.development (mkDefault)
  mkCapability =
    {
      name,
      features ? [ ],
      humanFeatures ? [ ],
      subscribesToDevelopment ? false,
      description ? "${name} capability",
    }:
    { config, ... }:
    let
      cfg = config.phillipgreenii.capabilities.${name};
      a = config.phillipgreenii.account;
    in
    {
      options.phillipgreenii.capabilities.${name}.enable = lib.mkEnableOption description;
      config = lib.mkMerge (
        [
          (lib.mkIf cfg.enable (lib.mkMerge (map featureEnable features)))
          (lib.mkIf (cfg.enable && a.isHuman) (lib.mkMerge (map featureEnable humanFeatures)))
        ]
        ++ lib.optional subscribesToDevelopment (
          # Additive-only subscription: contribute mkDefault true ONLY while
          # development is on, and NOTHING when it is off. A bare
          # `mkDefault config.…bundles.development.enable` would emit `mkDefault
          # false` when development is off, which COLLIDES with another additive
          # setter (e.g. a leaf that is ALSO in bundles.infra sets `mkDefault
          # true`) — two opposite mkDefaults crash eval. mkIf keeps it purely
          # additive so a subscriber may safely also belong to another bundle.
          lib.mkIf config.phillipgreenii.bundles.development.enable {
            phillipgreenii.capabilities.${name}.enable = lib.mkDefault true;
          }
        )
      );
    };

  # An aggregation bundle module: enabling it turns on child CAPABILITIES at
  # mkDefault true (additive-only). A machine vetoes a child with a bare
  #   phillipgreenii.capabilities.<child>.enable = false;
  mkBundle =
    {
      name,
      capabilities ? [ ],
      description ? "${name} bundle",
    }:
    { config, ... }:
    let
      cfg = config.phillipgreenii.bundles.${name};
    in
    {
      options.phillipgreenii.bundles.${name}.enable = lib.mkEnableOption description;
      config = lib.mkIf cfg.enable (
        lib.mkMerge (
          map (c: { phillipgreenii.capabilities.${c}.enable = lib.mkDefault true; }) capabilities
        )
      );
    };
}
