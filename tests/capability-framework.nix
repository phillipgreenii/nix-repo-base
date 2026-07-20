# Eval-level test for the Light capability framework (Plan 5): exercises feature
# gating, isHuman gating (both directions), bundles.development self-subscription,
# bundle child-enable + veto, and the open-account-property typo→error guarantee.
# Pure evalModules; no VM, no build. Consumed by the capability-framework-eval
# flake check.
{ lib }:
let
  fwLib = import ../lib/capabilities.nix { inherit lib; };
  inherit (fwLib) mkCapability mkBundle;
  framework = import ../home/capability-framework/default.nix;

  # Stub the FEATURE option namespace (real feature modules live downstream).
  featureStubs =
    { lib, ... }:
    {
      options.phillipgreenii.programs = lib.mkOption {
        default = { };
        type = lib.types.attrsOf (lib.types.submodule { options.enable = lib.mkEnableOption "stub"; });
      };
    };

  golang = mkCapability {
    name = "golang";
    features = [
      "go"
      "gofumpt"
    ];
    humanFeatures = [ "bat" ];
    subscribesToDevelopment = true;
  };
  ccLeaf = mkCapability {
    name = "claude-code";
    features = [ "claude-code" ];
  };
  beadsLeaf = mkCapability {
    name = "beads";
    features = [ "beads" ];
  };
  agentSupport = mkBundle {
    name = "agent-support";
    capabilities = [
      "claude-code"
      "beads"
    ];
  };
  # A bundle whose child (golang) ALSO subscribes to development. Enabling this
  # bundle while development is OFF must NOT crash: the subscription is additive-
  # only (mkIf development), so only the bundle's `mkDefault true` applies. Guards
  # the two-opposite-mkDefault regression (subscriber-in-a-bundle collision).
  golangBundle = mkBundle {
    name = "golang-bundle";
    capabilities = [ "golang" ];
  };

  eval =
    extra:
    (lib.evalModules {
      modules = [
        framework
        featureStubs
        golang
        ccLeaf
        beadsLeaf
        agentSupport
        golangBundle
      ]
      ++ extra;
    }).config;
  progs = c: c.phillipgreenii.programs;

  cDevHuman = eval [
    {
      phillipgreenii = {
        bundles.development.enable = true;
        account.isHuman = true;
      };
    }
  ];
  cDevAgent = eval [
    {
      phillipgreenii = {
        bundles.development.enable = true;
        account.isHuman = false;
      };
    }
  ];
  cBundle = eval [ { phillipgreenii.bundles.agent-support.enable = true; } ];
  cVeto = eval [
    {
      phillipgreenii = {
        bundles.agent-support.enable = true;
        capabilities.beads.enable = false;
      };
    }
  ];
  typoSucceeds =
    (builtins.tryEval
      (eval [ { phillipgreenii.account.isHumn = true; } ]).phillipgreenii.account.isHuman
    ).success;

  # Subscriber-in-a-bundle with development OFF: must eval cleanly (no two-opposite-
  # mkDefault crash) AND enable the child via the bundle.
  cSubscriberBundleDevOff =
    let
      cfg = eval [
        {
          phillipgreenii = {
            bundles.golang-bundle.enable = true;
            account.isHuman = false;
          };
        }
      ];
    in
    builtins.tryEval cfg.phillipgreenii.programs.go.enable;

  results = {
    dev_human_go = (progs cDevHuman).go.enable == true;
    dev_human_bat = (progs cDevHuman).bat.enable == true;
    dev_agent_go = (progs cDevAgent).go.enable == true;
    dev_agent_bat_absent = ((progs cDevAgent).bat.enable or false) == false;
    bundle_cc = (progs cBundle).claude-code.enable == true;
    bundle_beads = (progs cBundle).beads.enable == true;
    veto_beads_off = ((progs cVeto).beads.enable or false) == false;
    veto_cc_still_on = (progs cVeto).claude-code.enable == true;
    typo_errors = typoSucceeds == false;
    subscriber_bundle_dev_off_no_crash = cSubscriberBundleDevOff.success == true;
    subscriber_bundle_dev_off_enables_child =
      cSubscriberBundleDevOff.success && cSubscriberBundleDevOff.value == true;
  };
in
results // { allPass = lib.all (x: x) (lib.attrValues results); }
