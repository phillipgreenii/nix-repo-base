---
name: capability-model
description: >-
  Use when adding, placing, gating, or reorganising a TOOL or program in the
  phillipgreenii nix-* flakes (nix-repo-base, nix-personal, nix-agent-support,
  nix-overlay, homelab) — anything expressed through `phillipgreenii.programs.*`,
  `phillipgreenii.capabilities.*`, `phillipgreenii.bundles.*`, or
  `phillipgreenii.account.isHuman`. Fires on: "add tool/config X" and deciding
  whether it belongs in a home-manager capability vs `environment.systemPackages`
  vs a `services.*` closure vs `darwinModules`; authoring or extending a feature
  module, a `mkCapability` leaf, or a `mkBundle`; wiring `bundles.development` /
  the development subscription; gating human-only tooling via `account.isHuman`;
  the additive-only bundle invariant (why `mkDefault false` is forbidden and two
  opposite mkDefaults crash eval); reconciling a drifted/behind consumer
  (especially the darwin machine) after a renamed option or a removed feature; and
  the CI/build-runner least-privilege placement (runner gets its own HM profile,
  not root `environment.systemPackages`). Also fires when you see `mkCapability`,
  `mkBundle`, `enableFeatureIf`, `capability-framework`, `subscribesToDevelopment`,
  or `humanFeatures`. Do NOT use for generic nix work unrelated to the capability
  model, or for `twistcone.*` system SERVICE modules (jellyfin, k3s, docker
  daemon) — those are a separate axis this model does not touch.
---

# The phillipgreenii Light Capability Model

Rules for AI agents adding, placing, or reorganising tooling across the
phillipgreenii `nix-*` flakes. The model is **HM-only, additive, and
type-safe**: capabilities are thin home-manager setters over feature modules;
`account.isHuman` is the only behavioural axis; bundles aggregate additively.

Authority for the design: Plan 5
(`nix-personal/docs/superpowers/plans/2026-07-15-plan-5-light-capability-model.md`)
and Plan 6 (the convergence + darwin + this skill, bead `tc-oqzb9.5`). The
framework itself lives in `nix-repo-base`
(`home/capability-framework/default.nix` declares the namespaces;
`lib/capabilities.nix` exports the authoring helpers).

---

## 1. The two-layer model (feature → capability → bundle)

There are exactly three kinds of thing. Keep them distinct.

- A **FEATURE module** is the **single source of truth** for one program. It lives
  in the repo that owns the tooling, under `home/programs/<name>/`, and it:
  declares its own `phillipgreenii.programs.<name>.enable`, installs the package,
  and sets that program's options/config. Downstream/integration modules **READ**
  feature flags; they never declare them.
- A **CAPABILITY** is a **thin `mkDefault` setter** produced by `mkCapability`. It
  turns on one or more feature flags (and, for human accounts, extra
  `humanFeatures`). It owns no installs and no config of its own — only wiring.
- A **BUNDLE** is a `mkBundle` over capabilities: enabling it sets each child
  `capabilities.<c>.enable = mkDefault true`.

**MUST:** a conditional or integration fragment MUST gate on a **FEATURE** flag
(`config.phillipgreenii.programs.<x>.enable`, or an upstream `config.programs.<x>.enable`),
**never** on `capabilities.*` or `bundles.*`. The capability/bundle layer is
write-only from the framework's side; reading it to gate behaviour reintroduces
the coupling the Light model exists to remove.

```nix
# CORRECT — integration fragment reads the FEATURE flag:
config = lib.mkIf
  (config.phillipgreenii.programs.claude-code.enable && config.programs.neovim.enable)
  { programs.neovim.plugins = [ pkgs.unstable.vimPlugins.claudecode-nvim ]; };

# WRONG — never gate on the capability/bundle layer:
config = lib.mkIf config.phillipgreenii.capabilities.claude-code.enable { ... };
```

## 2. `account.isHuman` is the only behavioural axis

`phillipgreenii.account.isHuman : bool` (default `false`) is the **only**
behavioural account property. It gates human-only tooling (TUIs, themes, editor
niceties) **inside** capabilities, via a capability's `humanFeatures` list.

- **Agent vs system vs human** is expressed by **which capabilities an account
  enables** (plus the finer `humanFeatures` split) — **never** by a
  within-capability "if agent do X else Y" gate.
- There is deliberately **no freeform catch-all** account property. A capability
  MAY declare additional `account.<prop>` options (they merge cleanly), but a typo
  like `account.isHumn = true` MUST fail evaluation rather than silently no-op.

## 3. The `bundles.*` namespace and the additive-only invariant

Bundles and subscriptions set `mkDefault true` **only**. This is load-bearing:

- **MUST NOT** ever set a capability/feature to `mkDefault false`. Two opposite
  `mkDefault`s on the same option **crash evaluation** ("mkDefault … and …
  mismatch"). `mkCapability`/`mkBundle` enforce this by construction — a
  human-only feature on a non-human account contributes **nothing** (via `mkIf`),
  not a `mkDefault false`. Hand-written fragments MUST follow the same rule
  (`enableFeatureIf cond feature` gives you `mkIf cond (… mkDefault true)` and
  nothing on the else branch).
- A machine **vetoes** a bundle-provided child with a **bare** assignment:
  `phillipgreenii.capabilities.<child>.enable = false;` (an ordinary priority
  wins over `mkDefault true`). Never veto with `mkForce false` unless you have a
  genuine double-`mkDefault` to break.
- `bundles.development` is the **open inverted subscription**. A capability opts
  IN with `subscribesToDevelopment = true`, which emits
  `capabilities.<x>.enable = lib.mkDefault config.phillipgreenii.bundles.development.enable`.
  A machine enables `bundles.development.enable = true` once and receives the
  union of every input's dev tooling; each child stays individually vetoable.

## 4. Where things live (single ownership)

- The **framework** (the `account.*` / `bundles.*` namespaces + the
  `mkCapability`/`mkBundle`/`enableFeatureIf` helpers) lives in **nix-repo-base**
  and nowhere else. Consumers import `homeModules.capability-framework` and use
  `lib.{mkCapability,mkBundle,enableFeatureIf}`.
- Each **capability is defined in the repo that owns the tooling** it enables —
  agent tooling in `nix-agent-support`, personal/dev tooling in `nix-personal`.
- A **consumer account imports BOTH** `homeModules.default` (the pure feature
  aggregate — every `home/programs/*` module, import-once) **and**
  `homeModules.capabilities` (the leaves + framework). The feature aggregate MUST
  be inert — it declares feature options and installs-on-enable, but does **not**
  force-enable anything, so importing it onto every account (including non-human)
  is safe.

> Historical footgun (Plan 6, critique C-1): nix-personal's old
> `home/default.nix` force-enabled ~30 programs at `mkDefault true` and imported
> stylix. Importing THAT into every HM user would push those tools + stylix onto
> tcagent and defeat the model. The converge made `home/default.nix` **pure** (the
> force-enable block deleted); if you find a "feature aggregate" that force-enables
> tools, it is NOT safe to import broadly — fix it to be pure first.

## 5. How to ADD or EXTEND a tool (worked example)

To add a new tool `foo`, owned by (say) nix-personal, wanted on dev machines:

1. **Feature module** — create `home/programs/foo/default.nix`. It declares
   `phillipgreenii.programs.foo.enable` and installs/configures `foo` under
   `lib.mkIf config.phillipgreenii.programs.foo.enable`. Add it to the
   import-once aggregate (`home/default.nix`'s `imports`).
2. **Capability leaf** — add a `mkCapability` entry in the repo's
   `home/capabilities/default.nix`:

   ```nix
   { name = "foo"; features = [ "foo" ]; subscribesToDevelopment = true; }
   ```

   - Put anything human-only in `humanFeatures = [ … ]` instead of `features`.
   - Set `subscribesToDevelopment = true` only if `foo` should ride the generic
     dev-machine set; leave it off for opt-in-only tooling (e.g. agent-support's
     leaves deliberately do NOT subscribe).

3. **Bundle (optional)** — if `foo` belongs to a named group, add its capability
   name to the relevant `mkBundle` `capabilities` list.
4. **Dependency rule** — a tool pulled in only because another needs it rides with
   that capability; do NOT blanket-install it. Example (Plan 6 DQ-2): `gcc` is
   needed only for Go (cgo), so it belongs to the `golang` capability and lands
   only where Go development is enabled — never in a global package set.
5. **Test row** — add an eval-wiring row to the repo's `capabilities-eval` test
   (the `featureStubs` idiom: an `attrsOf (submodule {enable})` for `programs.*`
   so the eval is pure — no overlay/allowUnfree). Assert the feature turns on when
   the capability is enabled, respects the isHuman split, and is reachable via its
   bundle. Package builds are validated downstream (homelab `--override-input`).

## 6. How to RESOLVE CONFLICTS when a consumer is behind (the reconciliation playbook)

The pin **is** the version (`feedback-pin-is-the-version`): these are HEAD-consumed
internal APIs with no compat shims and no `LIB_VERSION` guards. When a consumer —
**especially the drifted darwin machine** — arrives with an older API, you
reconcile by a **coordinated cutover**, not by adding back-compat. Concrete failure
classes seen during the Plan-5/Plan-6 cutover:

- **A module uses a renamed option** (e.g. `programs.claude.*` → `programs.claude-code.*`):
  bump the sibling flake input in lockstep (the pin is the version) and grep for
  **both dotted and nested forms** of the old name — `foo.bar = true` hides from a
  `grep 'foo.bar'` when written as `foo = { bar = true; }`
  (`feedback-grep-nested-and-dotted`). Fix every consumer edge in the same landing.
- **A capability references a removed feature (or a removed feature is still
  referenced):** single-ownership + import-once means there is exactly **one**
  owner to fix. Fix that owner, then re-lock. Do not add a second definition to
  paper over it.
- **The darwin machine arrives with an older API:** sync the marketplace, read this
  skill, bump the sibling inputs together, then run
  `nix eval darwinConfigurations.<host>...` to surface option/type errors **before**
  attempting a build. Eval catches the option/wiring class of error without needing
  the Mac.

General procedure: identify the single owner → change it → bump the pins that
consume it in the same coordinated landing → validate by eval (cross-repo:
`pn workspace flake-check`) → then build. Never introduce a version constant or a
runtime version-guard to bridge two revs.

## 7. System (NixOS/darwin) vs HM (capability) — the placement decision procedure

**Design premise:** HM is the default for **every** tool. There are essentially no
host-level "exceptions", because every account a human or agent actually WORKS AS
gets an HM profile (`home-manager.users.<name>` manages any user, including
`isSystemUser` service/CI users). "This host has no HM user" is a **fixable gap**,
never a justification for parking a tool in `environment.systemPackages`.

**Root is the one deliberate non-exception:** root stays a NixOS/darwin-defined
system identity with **NO HM profile**. You do not do tooling as root — you use a
named `sudo` user. Root's only tools are the recovery floor (Q2).

When asked to "add tool/config X", answer these in order — **first YES wins**:

- **Q1 — Is X OS/machine config or a service definition, not a runnable user
  tool?** (users/groups, networking, firewall, boot, kernel, filesystems,
  `services.*`; darwin system defaults, homebrew, dock, launchd _daemon
  definitions_.) → **NixOS/darwin config.** (Not an "exception" — X isn't a tool.)
- **Q2 — Must the BINARY exist before any user's HM activates, or is it exec'd by a
  system service?**
  - _Exec'd by a system service_ → vendor it into the **service's own closure**:
    `${pkgs.X}` in the `ExecStart`, `runtimeInputs`, or
    `systemd.services.<x>.path` (darwin: `phillipgreenii.system.launchdServices`).
    **NOT** root `environment.systemPackages`.
  - _Needed before HM activates (root recovery / bootstrap)_ → the ONE legitimate
    `environment.systemPackages` tool case: a MINIMAL, `lib.lowPrio` recovery floor
    (shell, vim, curl, git-minimal, openssh — the existing `core/base.nix`). Kept
    deliberately tiny; HM versions win via `lowPrio`.
- **Q3 — Otherwise** (any tool a human/agent/service-user invokes: interactively,
  in their scripts, in pre-commit, in "developing tool X", as a CI job step) →
  **HM capability**, in that account's HM profile. If the account has no HM profile
  yet, **add one** — do NOT fall back to `environment.systemPackages`.

**Supporting rules:**

- **Dependency rule:** a tool pulled in only because another needs it rides with
  that capability (`gcc` → `golang`); never blanket-installed.
- **CI / build-runner:** the runner MUST be a dedicated non-root least-privilege
  user WITH its own HM profile carrying the CI toolchain. Copies of dev tools in
  the runner host's `environment.systemPackages` are **violations to remove**;
  anything the runner _service_ itself execs goes to the scoped
  `systemd.services.<x>.path`, not root's env.
- **No `/nix/store` duplication either way.** System vs HM is a difference of PATH
  visibility + activation context, **not** disk. "Same tool, two homes" means the
  same package in two accounts' HM profiles (e.g. `promtool` in a dev human's HM
  AND the CI user's HM) — NOT one-in-system-one-in-HM.
- **Daemon/HM split pattern (validated — reuse it):** a tool with both a daemon and
  an interactive form (ollama, ccpool, pa-monitor) embeds `${pkgs.<tool>}` in its
  service unit so the SERVICE closure is self-contained; the on-PATH copy is the HM
  capability. Toggling the HM capability off never breaks the daemon.
- **`sudo` nuance:** an HM-only tool for user `alice` is NOT on PATH under
  `sudo <tool>` (sudo switches to root's env). Handle by escalating the **step**,
  not the toolchain — the workspace deploy pattern
  `nixos-rebuild switch --flake …#{hostname} --sudo` builds/evaluates as the
  invoking user (HM tools resolve) and escalates only activation. Do **NOT** give
  root a toolchain to work around this.

## 8. Cross-references

- Skill: `pn-workspace-rules` — the workspace build/validate/land conventions that
  govern any change to these repos.
- Memory: `project-capabilities-cross-repo` (the Light model design),
  `feedback-pin-is-the-version` (no version constants; coordinated cutover),
  `feedback-grep-nested-and-dotted` (audit both option forms),
  `feedback-system-vs-hm-placement` (the Q1/Q2/Q3 procedure),
  `feedback-useglobalpkgs-true` (HM modules never set `nixpkgs.config`/overlays).
- Beads: epic `tc-oqzb9`; `tc-oqzb9.5` (this convergence); `tc-oqzb9.6` (remaining
  system→HM fleet moves — the follow-up execution of this procedure).
