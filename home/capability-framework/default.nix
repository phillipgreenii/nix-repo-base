# Capability framework (home-manager) — the shared foundation for the "Light"
# capability model used across the phillipgreenii nix-* flakes (Plan 5).
#
# This module ONLY declares the framework option namespaces; it installs nothing
# and enables nothing. Capability DEFINITIONS live in the consuming flakes
# (nix-personal, nix-agent-support); this repo owns only the framework so both
# consumers share one vocabulary and cross-repo subscription is type-safe.
#
# Two axes:
#   * phillipgreenii.account.*  — declared, type-safe account properties. `isHuman`
#     is the ONLY behavioural axis (gates human-only tooling). Agent-vs-system is
#     expressed by WHICH capabilities an account enables, never a within-capability
#     gate. Other capabilities MAY declare additional `account.<prop>` options
#     (they merge cleanly); there is deliberately NO freeform catch-all, so a typo
#     like `account.isHumn = true` fails evaluation.
#   * phillipgreenii.bundles.<name>.enable — aggregation flags. Distinct from
#     `capabilities.<name>.enable` (a leaf) so the option path reveals the layer.
#     `bundles.development` is the open dev-machine aggregate: capabilities in any
#     repo self-subscribe via `mkDefault`, so a machine opts in once and receives
#     the union of every input's dev tooling, each child still vetoable.
{ lib, ... }:
{
  options.phillipgreenii = {
    account.isHuman = lib.mkOption {
      type = lib.types.bool;
      default = false;
      example = true;
      description = ''
        Whether this account is driven interactively by a human. Gates human-only
        tooling (TUIs, themes, editor niceties) inside capabilities. This is the
        ONLY behavioural account property in the capability model — agent vs system
        accounts differ only in which capabilities they enable, never in how a
        capability behaves. Defaults false (a non-human/automated account); the
        accounts layer sets it true per human account.
      '';
    };

    bundles.development.enable = lib.mkEnableOption ''
      this account as a repo-development target. Capabilities across all inputs
      self-subscribe (`capabilities.<x>.enable = lib.mkDefault
      config.phillipgreenii.bundles.development.enable`), so enabling this pulls in
      the union of every input's development tooling. A new tool flows automatically
      once its capability subscribes; any individual child stays vetoable with a
      bare `capabilities.<x>.enable = false`
    '';
  };
}
