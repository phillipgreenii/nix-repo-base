# Activation-Script Output Consistency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the 7 author-controlled `system.activationScripts` a consistent `[tag]` header + colored `✓`/`⚠`/`✗` (ASCII fallback) status style with blank-line separators, driven by one shared helper, and make color render on the `pn workspace apply` path.

**Architecture:** A pure string-builder (`lib.activationHelpers` + `lib.mkActivationSection`) is added to `phillipg-nix-repo-base`'s flake `lib` and consumed via `inputs.phillipgreenii-nix-base.lib.…` by the 5 ziprecruiter and 2 personal activation scripts. A one-line `pn` change exports `CLICOLOR_FORCE=1` to the apply subprocess when pn's own stdout is a TTY, so ANSI flows through pn's stdout pipe. No nix/darwin internals change; only the `.text` we author is rewritten.

**Tech Stack:** Nix (flake-parts, nix-darwin), Bash (activation scripts), Go (the `pn` workspace CLI), `pkgs.lib.runTests` + `pkgs.runCommand` for checks.

**Spec:** `docs/superpowers/specs/2026-06-29-activation-script-output-consistency-design.md`

## Global Constraints

- **Output-only change.** Operational commands in every activation script MUST stay byte-for-byte identical. Only `echo`/`printf` log lines and module-arg/wrapper additions change. (Especially: local-proxy's `__BUILD_TIME__` sed substitution and heredoc bodies; beads-dolt's `dolt init`/`jq` commands.)
- **Helper access idiom:** `inputs.phillipgreenii-nix-base.lib.mkActivationSection` / `inputs.phillipgreenii-nix-base.lib.activationHelpers` (matches existing `mkGitHash`/`mkBashBuilders`).
- **Color rule (runtime bash):** emit ANSI when `CLICOLOR_FORCE` non-empty OR `[ -t 1 ]`; NEVER when `NO_COLOR` non-empty (`NO_COLOR` wins).
- **Glyph rule (runtime bash):** UTF-8 glyphs (`✓`/`⚠`/`✗`) when `LC_ALL`/`LC_CTYPE` matches `*UTF-8*`/`*utf8*`; otherwise width-padded ASCII markers `[OK]   ` / `[WARN] ` / `[FAIL] ` (each marker field 7 chars: marker + padding so messages align). Decision keys off `LC_CTYPE`/`LC_ALL`, never `LANG`.
- **Escaping:** every helper prints via `printf '%s\n' "  <prefix>$*"` — `$*` is NEVER a printf format string.
- **Tags:** lowercase-kebab matching the service (`colima`, `beads-dolt`, `sleepwatcher`, `local-proxy`, `searxng`, `terminal`, `launchd-health-check`).
- **Cardinal rule:** never point `flake.nix` input URLs at local paths; the worktree set / `pn` inject `--override-input` at build time.
- **Commits:** one per task; branch `activation-output-consistency` in every repo. No `--no-verify`. Pre-commit/treefmt hooks may reformat — re-stage and re-commit.

---

### Task 0: Create the coordinated worktree set

**Files:** none (workspace setup).

The change spans 3 repos that must build together. The spec's spec commits already exist on branch `activation-output-consistency` in the canonical `phillipg-nix-repo-base` checkout. Move that branch into a coordinated worktree set so all repos share one feature branch and the canonical checkouts stay untouched (P1).

- [ ] **Step 1: Park base's canonical checkout back on main** (so the branch is free to be checked out in the set)

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git switch main
```

- [ ] **Step 2: Create the set on the feature branch**

```bash
cd /Users/phillipg/phillipg_mbp
pn workspace worktree add activation-output-consistency
```

Expected: a set under `.worktrees/activation-output-consistency/` with one worktree per repo, all on branch `activation-output-consistency`. base's worktree carries the two spec commits (the branch already existed there); the other repos' branches are created fresh off their `main`.

- [ ] **Step 3: Enter the set and detach PN_WORKSPACE_ROOT**

```bash
cd /Users/phillipg/phillipg_mbp/.worktrees/activation-output-consistency
unset PN_WORKSPACE_ROOT
pn workspace status
```

Expected: every repo reports it is on `activation-output-consistency`. **All subsequent tasks run inside this set.** Paths below are relative to the set root.

---

### Task 1: `lib.activationHelpers` + `lib.mkActivationSection` in base

**Files:**

- Create: `phillipg-nix-repo-base/lib/activation.nix`
- Create: `phillipg-nix-repo-base/lib/activation-tests.nix`
- Modify: `phillipg-nix-repo-base/flake.nix` (the `lib =` block ~line 254-278; the `checks` block near `version-lib` ~line 128 and `claude-marketplace-lib` ~line 141)

**Interfaces:**

- Produces: `activationHelpers` (string — bash defining `act_ok`/`act_warn`/`act_fail`/`act_info` + color/glyph detection) and `mkActivationSection { tag, headline ? null, body } -> string`. Both pure (imported with `{ }`, no `pkgs`, no `lib`).

- [ ] **Step 1: Write the helper builder**

Create `phillipg-nix-repo-base/lib/activation.nix`:

```nix
# Pure string-builders for consistent system.activationScripts output.
# Spec: docs/superpowers/specs/2026-06-29-activation-script-output-consistency-design.md
{ }:
let
  # POSIX single-quote escaping (no lib dependency).
  esc = s: "'" + (builtins.replaceStrings [ "'" ] [ "'\\''" ] s) + "'";

  # Bash defining act_* plus color/glyph detection. Idempotent: safe to emit
  # multiple times in one shell (last definition wins). Also injected verbatim
  # into child scripts that run in their own process (see beads-dolt).
  activationHelpers = ''
    if [ -n "''${NO_COLOR:-}" ]; then
      _act_color=0
    elif [ -n "''${CLICOLOR_FORCE:-}" ]; then
      _act_color=1
    elif [ -t 1 ]; then
      _act_color=1
    else
      _act_color=0
    fi
    case "''${LC_ALL:-''${LC_CTYPE:-}}" in
      *UTF-8* | *utf-8* | *UTF8* | *utf8*) _act_utf8=1 ;;
      *) _act_utf8=0 ;;
    esac
    if [ "$_act_utf8" = 1 ]; then
      _act_m_ok='✓ ' ; _act_m_warn='⚠ ' ; _act_m_fail='✗ '
    else
      _act_m_ok='[OK]   ' ; _act_m_warn='[WARN] ' ; _act_m_fail='[FAIL] '
    fi
    if [ "$_act_color" = 1 ]; then
      _act_c_ok=$'\033[32m' ; _act_c_warn=$'\033[33m' ; _act_c_fail=$'\033[31m' ; _act_c_off=$'\033[0m'
    else
      _act_c_ok='' ; _act_c_warn='' ; _act_c_fail='' ; _act_c_off=''
    fi
    act_ok()   { printf '%s\n' "  ''${_act_c_ok}''${_act_m_ok}''${_act_c_off}$*"; }
    act_warn() { printf '%s\n' "  ''${_act_c_warn}''${_act_m_warn}''${_act_c_off}$*"; }
    act_fail() { printf '%s\n' "  ''${_act_c_fail}''${_act_m_fail}''${_act_c_off}$*"; }
    act_info() { printf '%s\n' "    $*"; }
  '';

  mkActivationSection =
    { tag, headline ? null, body }:
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
```

- [ ] **Step 2: Write the pure (string-shape) tests**

Create `phillipg-nix-repo-base/lib/activation-tests.nix`:

```nix
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
  testPrintfSafeForm = {
    expr = lib.hasInfix "printf '%s\\n'" act.activationHelpers;
    expected = true;
  };
  testAsciiMarkersPresent = {
    expr = lib.hasInfix "[OK]   " act.activationHelpers && lib.hasInfix "[WARN] " act.activationHelpers;
    expected = true;
  };
  testColorGuards = {
    expr = lib.hasInfix "CLICOLOR_FORCE" act.activationHelpers && lib.hasInfix "NO_COLOR" act.activationHelpers;
    expected = true;
  };
  testHelpersIsString = {
    expr = builtins.isString act.activationHelpers;
    expected = true;
  };
}
```

- [ ] **Step 3: Wire the lib export + checks into `flake.nix`**

In `phillipg-nix-repo-base/flake.nix`, append to the `lib = … ;` merge chain (after the module-helpers `//` block, ~line 277):

```nix
          # Activation-script output helpers
          // (import ./lib/activation.nix { })
```

In the `checks` block (alongside `version-lib`/`claude-marketplace-lib`), add:

```nix
            activation-lib =
              let
                failures = pkgs.lib.runTests (import ./lib/activation-tests.nix { lib = pkgs.lib; });
              in
              pkgs.runCommand "check-activation-lib" { } (
                if failures == [ ] then
                  "touch $out"
                else
                  "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
              );

            activation-behavior =
              let
                sectionFile = pkgs.writeText "demo-section.sh" (
                  (import ./lib/activation.nix { }).mkActivationSection {
                    tag = "demo";
                    headline = "checking";
                    body = ''
                      act_ok "all good"
                      act_warn 'careful %s \ $HOME'
                      act_fail "broke"
                      act_info "fyi"
                    '';
                  }
                );
              in
              pkgs.runCommand "check-activation-behavior" { } ''
                set -euo pipefail
                # runCommand stdout is a pipe (no TTY) and CLICOLOR_FORCE unset => no ANSI.
                plain=$(LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash ${sectionFile})
                printf '%s\n' "$plain"
                if printf '%s' "$plain" | grep -q $'\033'; then echo "FAIL: ANSI without force"; exit 1; fi
                if ! printf '%s' "$plain" | grep -q '✓'; then echo "FAIL: missing UTF-8 glyph"; exit 1; fi
                # Forced color (the pn apply path).
                forced=$(CLICOLOR_FORCE=1 LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash ${sectionFile})
                if ! printf '%s' "$forced" | grep -q $'\033\[32m'; then echo "FAIL: no green when forced"; exit 1; fi
                # ASCII fallback when locale is not UTF-8.
                ascii=$(LC_ALL=C LC_CTYPE=C ${pkgs.bash}/bin/bash ${sectionFile})
                if ! printf '%s' "$ascii" | grep -q '\[OK\]'; then echo "FAIL: no ASCII marker"; exit 1; fi
                if printf '%s' "$ascii" | grep -q '✓'; then echo "FAIL: glyph leaked into ASCII mode"; exit 1; fi
                # Arbitrary message stays literal (%, backslash, $).
                if ! printf '%s' "$plain" | grep -F 'careful %s \ $HOME' >/dev/null; then echo "FAIL: msg not literal"; exit 1; fi
                touch $out
              '';
```

- [ ] **Step 4: Run the checks to verify they pass**

```bash
cd phillipg-nix-repo-base
nix build .#checks.aarch64-darwin.activation-lib .#checks.aarch64-darwin.activation-behavior -L
```

Expected: both build successfully. `activation-behavior` prints the demo section (glyphs, no color) during the build.

- [ ] **Step 5: Commit**

```bash
git -C phillipg-nix-repo-base add lib/activation.nix lib/activation-tests.nix flake.nix
git -C phillipg-nix-repo-base commit -m "feat(lib): add mkActivationSection activation-output helpers"
```

---

### Task 2: `pn` exports CLICOLOR_FORCE on the apply path

**Files:**

- Modify: `phillipg-nix-repo-base/modules/pn/internal/workspace/apply.go` (the `exec.RunOptions` at ~line 87)
- Test: `phillipg-nix-repo-base/modules/pn/internal/workspace/apply_test.go` (create if absent)

**Interfaces:**

- Consumes: existing `colorEnabled(w io.Writer) bool` (`tree.go:191`, same package), `exec.RunOptions.Env map[string]string` (`exec.go:29`).
- Produces: `applyColorEnv(colorOK bool) map[string]string`.

- [ ] **Step 1: Write the failing test**

Create/append `phillipg-nix-repo-base/modules/pn/internal/workspace/apply_test.go`:

```go
package workspace

import "testing"

func TestApplyColorEnv(t *testing.T) {
	if got := applyColorEnv(false); got != nil {
		t.Fatalf("colorOK=false: want nil env, got %v", got)
	}
	got := applyColorEnv(true)
	if got["CLICOLOR_FORCE"] != "1" {
		t.Fatalf("colorOK=true: want CLICOLOR_FORCE=1, got %v", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd phillipg-nix-repo-base/modules/pn
go test ./internal/workspace/ -run TestApplyColorEnv
```

Expected: FAIL — `undefined: applyColorEnv`.

- [ ] **Step 3: Implement `applyColorEnv` and call it from apply**

In `apply.go`, add the helper (near the other package-level helpers):

```go
// applyColorEnv forces ANSI color through the apply pipe when the user's
// terminal supports it. pn pipes darwin-rebuild's stdout, so the activation
// script's [ -t 1 ] is always false; CLICOLOR_FORCE is how the activation
// helpers know to emit color anyway.
func applyColorEnv(colorOK bool) map[string]string {
	if colorOK {
		return map[string]string{"CLICOLOR_FORCE": "1"}
	}
	return nil
}
```

Then change the apply `Run` call (~line 87) from:

```go
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{Dir: terminalDir, Stdout: out, Stderr: out}); err != nil {
```

to:

```go
	if _, err := ws.runner.Run(ctx, cmdArgs[0], full, exec.RunOptions{
		Dir:    terminalDir,
		Stdout: out,
		Stderr: out,
		Env:    applyColorEnv(colorEnabled(os.Stdout)),
	}); err != nil {
```

Ensure `os` is imported in `apply.go` (add to the import block if missing).

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd phillipg-nix-repo-base/modules/pn
go test ./internal/workspace/ -run TestApplyColorEnv
go build ./...
```

Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git -C phillipg-nix-repo-base add modules/pn/internal/workspace/apply.go modules/pn/internal/workspace/apply_test.go
git -C phillipg-nix-repo-base commit -m "feat(pn): force CLICOLOR_FORCE on apply when stdout is a TTY"
```

---

### Task 3: personal — `ci-test` specialArgs + retrofit terminal & launchd-health-check

**Files:**

- Modify: `phillipgreenii-nix-personal/flake.nix` (`darwinConfigurations.ci-test`, ~line 183)
- Modify: `phillipgreenii-nix-personal/darwin/terminal/default.nix`
- Modify: `phillipgreenii-nix-personal/darwin/system/launchd-services.nix`

**Interfaces:**

- Consumes: `inputs.phillipgreenii-nix-base.lib.mkActivationSection` (Task 1).

- [ ] **Step 1: Give `ci-test` access to `inputs`**

In `phillipgreenii-nix-personal/flake.nix`, change:

```nix
        darwinConfigurations.ci-test = nix-darwin.lib.darwinSystem {
          system = "aarch64-darwin";
          modules = [
```

to add `specialArgs` (so the modules can take `inputs` during standalone `nix flake check`):

```nix
        darwinConfigurations.ci-test = nix-darwin.lib.darwinSystem {
          system = "aarch64-darwin";
          specialArgs = { inherit inputs; };
          modules = [
```

- [ ] **Step 2: Retrofit `terminal/default.nix`**

Change the module signature `{ lib, pkgs, ... }:` to `{ lib, pkgs, inputs, ... }:`.

Wrap the `terminalProfile` activation text. Current shape:

```nix
  system.activationScripts.terminalProfile.text = ''
    echo "Configuring Terminal.app PGII profile..."
    … operational PlistBuddy commands (unchanged) …
    echo "Terminal.app PGII profile configured successfully."
  '';
```

becomes:

```nix
  system.activationScripts.terminalProfile.text =
    inputs.phillipgreenii-nix-base.lib.mkActivationSection {
      tag = "terminal";
      headline = "configuring Terminal.app PGII profile";
      body = ''
        … operational PlistBuddy commands (unchanged) …
        act_ok "PGII profile configured"
      '';
    };
```

(Delete the two original `echo` lines; keep every PlistBuddy command byte-identical.)

- [ ] **Step 3: Retrofit `launchd-services.nix` health check**

Change the module signature to include `inputs` (add `inputs` to its argument set).

This block already has structure. Wrap its `postActivation` text with `mkActivationSection` (tag `launchd-health-check`, headline `verifying ${toString totalDaemonCount} daemon(s)`) and map signals:

- the header `echo "[launchd-health-check] verifying …"` → becomes the section `headline` (delete the bracket-prefixed echo).
- `echo "  ok  $label"` → `act_ok "$label"`
- `echo "  FAIL $label (final state=…)"` → `act_fail "$label (final state=…)"`
- `echo "  WARN cannot resolve uid…"` → `act_warn "cannot resolve uid…"`
- `echo "[launchd-health-check] all services running"` → `act_ok "all services running"`
- `echo "[launchd-health-check] FAIL: one or more services did not reach state=running"` → `act_fail "one or more services did not reach state=running"` (keep the following `exit 1` unchanged).

Keep all `launchctl`/state-machine logic and the `_check_*` functions byte-identical.

- [ ] **Step 4: Verify personal evaluates standalone**

```bash
cd phillipgreenii-nix-personal
nix flake check -L 2>&1 | tail -20
```

Expected: passes (the `specialArgs` fix lets `ci-test` resolve `inputs`).

- [ ] **Step 5: Commit**

```bash
git -C phillipgreenii-nix-personal add flake.nix darwin/terminal/default.nix darwin/system/launchd-services.nix
git -C phillipgreenii-nix-personal commit -m "feat(darwin): consistent activation output for terminal + launchd health check"
```

---

### Task 4: ziprecruiter — retrofit colima, sleepwatcher, local-proxy, searxng

**Files:**

- Modify: `phillipg-nix-ziprecruiter/darwin/services/colima/default.nix`
- Modify: `phillipg-nix-ziprecruiter/darwin/services/sleepwatcher/default.nix`
- Modify: `phillipg-nix-ziprecruiter/darwin/services/local-proxy/default.nix`
- Modify: `phillipg-nix-ziprecruiter/darwin/services/searxng/default.nix`

**Interfaces:**

- Consumes: `inputs.phillipgreenii-nix-base.lib.mkActivationSection` (Task 1).

For EACH of the four files: add `inputs` to the module argument set (e.g. `{ config, lib, pkgs, ... }:` → `{ config, lib, pkgs, inputs, ... }:`), then wrap the existing `system.activationScripts.postActivation.text = lib.mkAfter ''…'';` body with `mkActivationSection`, deleting the old header `echo` and mapping the status lines.

- [ ] **Step 1: colima**

```nix
    system.activationScripts.postActivation.text = lib.mkAfter (
      inputs.phillipgreenii-nix-base.lib.mkActivationSection {
        tag = "colima";
        headline = "ensuring config";
        body = ''
          COLIMA_USER="''${SUDO_USER:-$(/usr/bin/stat -f '%Su' /dev/console)}"
          COLIMA_HOME=$(dscl . -read "/Users/$COLIMA_USER" NFSHomeDirectory | awk '{print $2}')
          if sudo -H -u "$COLIMA_USER" \
            COLIMA_MOUNT_TYPE="${mountTypeArg}" \
            COLIMA_TEMPLATE="$COLIMA_HOME/.colima/_templates/default.yaml" \
            COLIMA_CONFIG="$COLIMA_HOME/.colima/default/colima.yaml" \
            COLIMA_BIN="/opt/homebrew/bin/colima" \
            ${ensureConfigScript}/bin/colima-ensure-config ${mountArgs}; then
            act_ok "colima config ensured"
          else
            act_warn "colima-ensure-config failed"
          fi
        '';
      }
    );
```

(The `colima-ensure-config` script's own internal echoes are a separate concern and are left as-is; only the activation wrapper is restyled.)

- [ ] **Step 2: sleepwatcher**

Replace the header `echo "setting up sleepwatcher stable wrapper..."` with the section wrapper (tag `sleepwatcher`, headline `setting up stable wrapper`); keep all operational lines; append `act_ok "sleepwatcher wrapper ready"` at the end of the body.

- [ ] **Step 3: local-proxy**

Wrap with tag `local-proxy`, headline `setting up TLS`. Map the progress echoes:

- `echo "generating self-signed cert for ${cfg.domain}..."` → `act_info "generating self-signed cert for ${cfg.domain}"`
- `echo "cert and CA installed"` → `act_ok "cert and CA installed"`
- `echo "restarting local-proxy daemon..."` → `act_info "restarting local-proxy daemon"`

**Do NOT touch** the `__BUILD_TIME__` sed substitution or any heredoc body — those are operational.

- [ ] **Step 4: searxng**

Wrap with tag `searxng`, headline `setting up env file` (preserve the `lib.mkIf … lib.mkAfter` wrapping — apply `mkActivationSection` to the inner text). Map:

- `echo "creating SearXNG env file at $ENV_FILE with generated SEARXNG_SECRET..."` → `act_info "creating env file at $ENV_FILE with generated secret"`
- `echo "created $ENV_FILE (owner $USER)"` → `act_ok "created $ENV_FILE (owner $USER)"`

- [ ] **Step 5: Build the workspace to verify ziprecruiter still evaluates**

```bash
cd /Users/phillipg/phillipg_mbp/.worktrees/activation-output-consistency
unset PN_WORKSPACE_ROOT
pn workspace build 2>&1 | tail -25
```

Expected: build succeeds (the `not writing modified lock file` warnings are benign/expected).

- [ ] **Step 6: Commit**

```bash
git -C phillipg-nix-ziprecruiter add darwin/services/colima/default.nix darwin/services/sleepwatcher/default.nix darwin/services/local-proxy/default.nix darwin/services/searxng/default.nix
git -C phillipg-nix-ziprecruiter commit -m "feat(darwin): consistent activation output for colima, sleepwatcher, local-proxy, searxng"
```

---

### Task 5: ziprecruiter — retrofit beads-dolt-projects (separate child process)

**Files:**

- Modify: `phillipg-nix-ziprecruiter/darwin/services/beads-dolt-projects/default.nix`

**Interfaces:**

- Consumes: `inputs.phillipgreenii-nix-base.lib.{mkActivationSection,activationHelpers}` (Task 1).

The per-project status echoes run inside `makeActivationScript` — a `pkgs.writeShellScript` invoked via `sudo -H -u "$BEADS_USER"`, i.e. a **separate process** that does not inherit the parent's `act_*` functions, `CLICOLOR_FORCE`, or `LC_CTYPE`. So inject `activationHelpers` into the child and forward both env vars through `sudo`.

- [ ] **Step 1: Add `inputs` to the module args** (e.g. `{ config, lib, pkgs, ... }:` → `{ config, lib, pkgs, inputs, ... }:`).

- [ ] **Step 2: Inject helpers + restyle the child script**

In `makeActivationScript`, prepend the helpers and convert the echoes:

```nix
  makeActivationScript =
    project:
    pkgs.writeShellScript "beads-dolt-init-${project.name}" ''
      set -e
      ${inputs.phillipgreenii-nix-base.lib.activationHelpers}
      DB_DIR=${lib.escapeShellArg "${project.dataDir}/${project.database}"}
      BEADS_DIR=${lib.escapeShellArg project.beadsDir}

      act_info "${project.name}: initialising"

      mkdir -p "$DB_DIR"
      mkdir -p "$BEADS_DIR"
      if [ ! -f "$BEADS_DIR/metadata.json" ]; then
        echo '{}' > "$BEADS_DIR/metadata.json"
        act_ok "${project.name}: created minimal metadata.json"
      fi
      if [ ! -d "$DB_DIR/.dolt" ]; then
        cd "$DB_DIR"
        ${pkgs.unstable.dolt}/bin/dolt init --name beads --email beads@localhost
        act_ok "${project.name}: created dolt database ${project.database}"
      fi

      if [ -f "$BEADS_DIR/metadata.json" ]; then
        ${pkgs.jq}/bin/jq \
          --argjson port ${toString project.port} \
          --arg db ${lib.escapeShellArg project.database} \
          '.dolt_server_port = $port | .dolt_database = $db | .dolt_mode = "server" | .dolt_server_host = "127.0.0.1"' \
          "$BEADS_DIR/metadata.json" \
          > "$BEADS_DIR/metadata.json.tmp" \
          && mv "$BEADS_DIR/metadata.json.tmp" "$BEADS_DIR/metadata.json"
        act_ok "${project.name}: updated metadata.json (port=${toString project.port} db=${project.database})"
      fi
    '';
```

(The `dolt init`, `jq`, `mkdir`, `mv` commands are operational — unchanged.)

- [ ] **Step 3: Wrap the parent loop + forward env through sudo**

Replace the `postActivation` block with a single section header in the parent and per-project child invocations that forward `CLICOLOR_FORCE`/`LC_CTYPE`:

```nix
    system.activationScripts.postActivation.text = lib.mkAfter (
      inputs.phillipgreenii-nix-base.lib.mkActivationSection {
        tag = "beads-dolt";
        headline = "initialising projects";
        body = lib.concatMapStringsSep "\n" (
          project:
          let
            script = makeActivationScript project;
          in
          ''
            BEADS_USER="''${SUDO_USER:-$(/usr/bin/stat -f '%Su' /dev/console)}"
            sudo -H \
              CLICOLOR_FORCE="''${CLICOLOR_FORCE:-}" \
              LC_CTYPE="''${LC_CTYPE:-}" \
              -u "$BEADS_USER" ${script} \
              || act_fail "${project.name}: init failed"
          ''
        ) cfg.projects;
      }
    );
```

- [ ] **Step 4: Build to verify**

```bash
cd /Users/phillipg/phillipg_mbp/.worktrees/activation-output-consistency
unset PN_WORKSPACE_ROOT
pn workspace build 2>&1 | tail -15
```

Expected: build succeeds.

- [ ] **Step 5: Commit**

```bash
git -C phillipg-nix-ziprecruiter add darwin/services/beads-dolt-projects/default.nix
git -C phillipg-nix-ziprecruiter commit -m "feat(darwin): consistent activation output for beads-dolt (child-process helpers)"
```

---

### Task 6: Workspace validation, ADR decision, and manual apply

**Files:**

- Possibly create: `phillipg-nix-repo-base/docs/adr/00NN-activation-output-convention.md` (decision below)

- [ ] **Step 1: Full workspace gates**

```bash
cd /Users/phillipg/phillipg_mbp/.worktrees/activation-output-consistency
unset PN_WORKSPACE_ROOT
pn workspace flake-check 2>&1 | tail -30
pn workspace build 2>&1 | tail -15
```

Expected: both pass across all repos. flake-check covers personal's `ci-test` (the `inputs` plumbing); build covers ziprecruiter's host config (consumer side).

- [ ] **Step 2: ADR decision**

The base repo's `CLAUDE.md` asks that decisions in an area get an ADR. Decide with the user whether the activation-output convention warrants one. If yes, add `docs/adr/00NN-activation-output-convention.md` (next number; follow `docs/adr/0000-…`'s format) recording: the `[tag]` + glyph house style, `mkActivationSection`, the `CLICOLOR_FORCE`-through-pn mechanism, and the all-stdout / `act_fail`-no-exit decisions. Commit to `phillipg-nix-repo-base`.

- [ ] **Step 3: Manual visual check (USER runs this)**

The activation itself MUST be run by the user (never from agent context):

```bash
cd /Users/phillipg/phillipg_mbp/.worktrees/activation-output-consistency
unset PN_WORKSPACE_ROOT
pn workspace apply
```

Expected: each controlled section prints a `[tag]` header, colored `✓`/`⚠`/`✗` status lines (color visible because pn set `CLICOLOR_FORCE`), and a blank line between sections.

- [ ] **Step 4: Integration / cleanup**

Use the `superpowers:finishing-a-development-branch` skill to land the set (the spec's manual merge-back recipe: `pn workspace rebase main` in the set, `git merge --ff-only` per canonical repo, then remove the set and delete branches). Push / open PRs only if the user asks.

---

## Self-Review

**Spec coverage:**

- `mkActivationSection` + `activationHelpers` pure builder → Task 1. ✓
- `[tag]` header / glyph + ASCII width-padded markers / blank-line separator → Task 1 (helper) + Tasks 3-5 (applied). ✓
- Color/glyph runtime guards (CLICOLOR_FORCE / `-t 1` / NO_COLOR; LC_CTYPE/LC_ALL) → Task 1. ✓
- `printf '%s\n'` escaping → Task 1 (helper) + `activation-behavior` test. ✓
- pn `CLICOLOR_FORCE` on apply → Task 2. ✓
- personal `ci-test` specialArgs → Task 3 Step 1. ✓
- beads-dolt child-process helper injection + sudo env forwarding → Task 5. ✓
- Tests: runTests string-shape + runCommand executing + pn Go test → Tasks 1-2. ✓
- Workspace gates (flake-check + build) + manual apply → Task 6. ✓
- Coordinated worktree set → Task 0; integration → Task 6 Step 4. ✓
- ADR decision → Task 6 Step 2. ✓
- All 7 scripts retrofitted: colima, sleepwatcher, local-proxy, searxng (Task 4), beads-dolt (Task 5), terminal, launchd-health-check (Task 3). ✓

**Placeholder scan:** No TBD/TODO; every code step shows full content; retrofit steps name exact echo→`act_*` mappings.

**Type consistency:** `mkActivationSection { tag, headline ? null, body }` and `activationHelpers` used identically across Tasks 1, 3, 4, 5. `applyColorEnv(bool) -> map` consistent between Task 2 impl and test. `act_ok`/`act_warn`/`act_fail`/`act_info` names consistent throughout.
