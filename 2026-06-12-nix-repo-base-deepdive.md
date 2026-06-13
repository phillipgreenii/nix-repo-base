# phillipg-nix-repo-base — Deepdive Review (2026-06-12)

Negative-space review: problems, risks, and gaps only. Findings are weighted by blast radius — this repo is the foundational lib consumed by every other `nix-*` flake, so a builder bug propagates to every consumer.

## Scope & Coverage

**Deep-read (line-by-line):**
- `flake.nix`, `lib/bash-builders.nix`, `lib/go-builders.nix`, `lib/version.nix`, `lib/version-tests.nix`, `lib/bash-builders-version-tests.nix`, `lib/bash-builders-tests/default.nix`
- `nix/packages.nix`, `nix/checks.nix`, `nix/dev-env.nix`, `nix/module-helpers.nix`
- `update-locks.sh`, `lib/scripts/update-locks-lib.bash`, `lib/scripts/update-cache-lib.bash`, `modules/ul/*` (scripts.nix, resolver script + default.nix), `modules/pn/default.nix`, `modules/pn/run-from-source.sh`, `home/pn/default.nix`
- `justfile`, `treefmt.nix`, `.github/workflows/*` (ci.yml fully, update-flakes.yml partially), ADR index + ADR 0005 + ADR 0006, `docs/using-mkGoBuilders.md`
- All Go under `modules/pn/` (~81 files) — deep-read via two delegated review passes: (a) `internal/workspace` (30 source files), (b) `cmd/pn` + `internal/cli` + `internal/exec` + `internal/store` + `internal/osx`. Test files skimmed; non-test files read in full.

**Skimmed:** bats test files (setup blocks + test counts), `pn-workspace-rules/CLAUDE.md`, `testdata/pn-workspace-reference.toml`, plugin manifests, fixture `.sh` bodies.

**Skipped:** `docs/superpowers/` plans/specs (historical), ADRs 0000–0004 (index only), `.beads/`, `docs/worktrees.md`, `flake.lock`/`go.sum` contents.

Note: the stack hint mentions `mkPythonPackage`; no such builder exists in this repo (only `checks.nix:testPythonProject`). ADR 0006 references `mkPythonPackage` as a consumer of `mkSrcDigest` — it presumably lives in a consuming repo and is out of scope here, but the ADR's claim that it "drops the gitHash argument" cannot be verified from this repo.

## Executive Summary

1. **The bash-builders test suite never runs.** `lib/bash-builders-tests/default.nix` (5 fixtures, ~18 bats tests, including the only coverage of config injection) is referenced by nothing — not `flake.nix`, not any check. The foundational builder is effectively untested in CI.
2. **`mkBashScript` config values are injected unescaped into bash** (`bash-builders.nix:124`), so any value containing `"`, `$`, or backticks becomes executable code in every consumer's script. Same for variable names.
3. **`pn` shipped mid-port:** `pn store deepclean` runs `sudo nix-store --gc` unconditionally (its dry-run/keep options are unreachable — no flags are registered anywhere in the CLI), several commands discard all subprocess output and "succeed" silently, and hooks from a *discovered* `pn-workspace.toml` execute via `sh -c` with no trust gate.
4. **Docs and the shipped agent plugin contradict the code.** `docs/using-mkGoBuilders.md` instructs consumers to pass `version` (silently discarded by `mkGoApp`) and describes a "dev"-rejection contract that no longer exists; `pn-workspace-rules/CLAUDE.md` tells agents to run `pn-workspace-build`/`pn-ws-nix`, commands that no longer exist.
5. **ADR 0006's nvd-attribution promise is unimplemented for bash scripts:** the script derivation hardcodes `version = "0.0.0"` so the digest never reaches the store-path name.
6. The workspace graph layer has **two parallel subsystems with divergent semantics** (`git+file://` vs `path:` overrides; cycle-error vs cycle-tolerate; hard-fail vs silent-empty on eval errors), and the silent paths can quietly produce wrong builds.

---

## Security

### S1 — HIGH: Shell injection through `mkBashScript` config/exportedConfig
**Location:** `lib/bash-builders.nix:120-134` (`mkConfigLine`)
**Problem:** `scriptLine = "${prefix}${varName}=\"${value}\"";` interpolates the value into a double-quoted bash context with no escaping. A value containing `$(...)`, backticks, `$VAR`, or an embedded `"` is evaluated when the script starts. `varName` is likewise unvalidated (not checked to be a bash identifier). The same string is repeated in `testExport`.
**Why it matters:** This is the shared builder for every bash script in every consumer repo. Today the values are developer-authored, so this is primarily a correctness landmine (a URL with `$` or a path with spaces silently corrupts), but it is also a latent injection vector the moment a config value is derived from anything external. The dead test fixture `sample-cmd-with-config` only tests benign values — and never runs (T1).
**Recommendation:** `scriptLine = "${prefix}${varName}=${lib.escapeShellArg value}"` and assert `builtins.match "[A-Za-z_][A-Za-z0-9_]*" varName != null` at eval time with a clear error.

### S2 — HIGH: Workspace hooks execute arbitrary `sh -c` from a discovered config with no trust gate
**Location:** `modules/pn/internal/workspace/hooks.go:52`; discovery at `modules/pn/internal/cli/workspace.go:76-85`
**Problem:** `resolveWorkspaceRoot` walks up from CWD to find `pn-workspace.toml`; any hook strings in it run via `sh -c` on read-only-sounding commands (`pn workspace status`).
**Why it matters:** `cd` into any untrusted checkout containing a `pn-workspace.toml` → arbitrary code execution. Same attack class direnv mitigates with `direnv allow`.
**Recommendation:** Add a trust gate (hash-pinned approval file outside the workspace, direnv-style), or refuse hooks unless the root was given explicitly (flag/env).

### S3 — HIGH: `pn store deepclean` runs `sudo nix-store --gc` unconditionally; safety options are unreachable
**Location:** `modules/pn/internal/store/deepclean.go:41`; `modules/pn/internal/cli/store.go:36-38`
**Problem:** The CLI hardcodes `store.DeepCleanOptions{}` and registers no `--dry-run`/`--keep`/`--keep-since` flags, so `DryRun` is always false and the privileged GC always runs — with no confirmation, and (see U2) no visible output. `KeepSince`/`Keep` are dead fields; the generation-pruning retention the bash predecessor implied is a TODO.
**Why it matters:** A privileged, irreversible store GC behind a single typo (`pn store deepclean` with no guard; note also `Args: cobra.NoArgs` is missing, so `pn store deepclean dry-run` silently runs the destructive path).
**Recommendation:** Wire the flags, require `--yes`/interactive confirmation before the sudo path, and hide the command until the retention port lands.

### S4 — MEDIUM: `update-locks.sh` executes unpinned code from GitHub HEAD on every run
**Location:** `update-locks.sh:30` — `UL_LIB_DIR="${UL_LIB_DIR:-$(nix run "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir")}"`
**Problem:** Unpinned `nix run github:...` fetches and executes whatever is on the default branch at invocation time; it also makes every lock update depend on network + GitHub availability. Extra wrinkle: this *is* nix-repo-base — the repo fetches its own published copy from the network to locate a file that sits in its own tree two directories away (the resolver can prefer the on-disk copy, but the resolver itself comes from the network).
**Why it matters:** Supply-chain exposure (branch compromise = code exec on every `update-locks.sh` run across all repos) and a bootstrap circularity/availability problem.
**Recommendation:** Pin a rev/tag in the URL, or short-circuit locally: `UL_LIB_DIR="${UL_LIB_DIR:-$SCRIPT_DIR/lib/scripts}"` in this repo, and have consumers resolve via their flake input (already locked) instead of a fresh `nix run`.

### S5 — MEDIUM: `pn workspace nix` deny-list is bypassed by any leading flag
**Location:** `modules/pn/internal/workspace/nix.go:55-72`
**Problem:** `matchesDeniedSubcommand` matches positionally from `args[0]`; `pn workspace nix --verbose flake update` sails past the guard and mutates `flake.lock` under overridden inputs — exactly what the deny list exists to prevent. Untested shape.
**Recommendation:** Skip leading `-*` tokens (and their values) before prefix matching; add a test.

### S6 — MEDIUM: Repo keys/URLs flow into git argv and path joins unvalidated
**Location:** `modules/pn/internal/workspace/init.go:38-44` and every `filepath.Join(ws.root, name)` site
**Problem:** A TOML key like `[repos."../elsewhere"]` escapes the workspace root; a URL starting with `-` is parsed by git as an option. `reconcileFromFilesystem` also writes harvested URLs back into the config.
**Recommendation:** Validate `name == filepath.Base(name)` and reject leading `-`/`.`; pass `--` before git positional args.

### S7 — LOW: Substituter trust is broadcast to all consumers and CI
**Location:** `flake.nix:4-13` (`nixConfig.extra-substituters` numtide/flox), `.github/workflows/ci.yml` (`accept-flake-config = true`)
**Problem:** CI auto-accepts any flake-supplied substituters/keys; consumers inherit the prompt. Third-party caches become a binary supply chain for everything built downstream.
**Recommendation:** Keep, but consciously: pin the decision in an ADR, and scope `accept-flake-config` in CI to this repo's known config rather than blanket-true.

### S8 — LOW: Inconsistent action pinning in CI
**Location:** `.github/workflows/ci.yml` — `actions/checkout` and `flake-checker-action` are SHA-pinned; `DeterminateSystems/determinate-nix-action@v3` and `nix-community/cache-nix-action@v7` are tag-pinned.
**Recommendation:** SHA-pin all of them; the mixed policy means the unpinned ones are the effective trust floor.

### S9 — LOW: TCC check trusts ambient `sqlite3` and swallows all errors
**Location:** `modules/pn/internal/osx/tcc_darwin.go:62-65`; `modules/pn/default.nix` runtimeDeps (nix/git/nvd only)
**Problem:** Any failure — including `sqlite3` missing from PATH — is treated as "FDA not granted" and returns nil silently; the privacy check can report success having checked nothing.
**Recommendation:** Add `pkgs.sqlite` to runtimeDeps; distinguish exec-start errors from authorization failures; print the skip warning the bash version had. Also: `TCC_DB_PATH` env (tcc_darwin.go:50) is a test seam shipping in production — drop it (tests already have `CheckOptions.DBPath`).

---

## Architecture

### A1 — HIGH: Two override mechanisms with different Nix semantics (`git+file://` vs `path:`)
**Location:** `modules/pn/internal/workspace/helpers.go:59` vs `helpers.go:86`
**Problem:** `pn workspace build/apply/flake-check` pin local repos with `git+file://` (excludes untracked files); `pn workspace nix` pins with `path:` (includes everything on disk). The same tree can produce different results between `pn workspace build` and `pn workspace nix build` — a not-yet-`git add`ed module silently missing from one.
**Why it matters:** This is the exact class of "works here, fails there" confusion the workspace tool exists to eliminate.
**Recommendation:** Pick one scheme (document why) and use it everywhere.

### A2 — HIGH: Dual DAG subsystems with divergent error philosophy; silent paths can produce wrong builds
**Location:** `modules/pn/internal/workspace/dag.go:41-46` (hard-fails on flakes without an explicit `inputs` attr — valid Nix); `inputs.go:27-31` + `discover.go:39-40` (swallow *all* eval/parse errors into an empty input map); `dag.go:131-144` (cycle leftovers silently appended) vs `graph.go:184-186` (cycle errors).
**Problem:** One path aborts `lock`/`init`/`tree` for a legal flake shape; the other silently drops a repo's out-edges on any transient `nix eval` failure, so `--override-input` flags are silently omitted and the user builds against locked upstreams while believing they used local clones. Two Kahn implementations (name-matching `buildDAG`, slug-matching `buildGraph`) disagree on cycles.
**Recommendation:** Single graph subsystem; `f: builtins.attrNames (f.inputs or {})` for the eval; distinguish "no flake.nix" (skip) from "eval failed" (warn or abort); warn on cycle fallback.

### A3 — MEDIUM: Builders are closed boxes — no overridability, and they clobber caller intent
**Location:** `lib/bash-builders.nix:96-330` (plain attrset return, hand-rolled `stdenv.mkDerivation`, no `lib.makeOverridable`, no `finalAttrs`); `lib/go-builders.nix:132-147`
**Problem:** A consumer cannot `.overrideAttrs` the meaningful unit (the returned record), add a postInstall to a bash script, or change meta without forking the builder. Worse, `mkGoApp` silently discards a caller-passed `version` (`removeAttrs args [... "version" ...]`) and silently *overwrites* a caller's `passthru.overrideModAttrs` (line 146: builder's value wins in the `//` merge) — both are accepted then ignored, the worst failure mode for an API.
**Recommendation:** `throw` on a caller-supplied `version` (or honor it); compose `overrideModAttrs` functions instead of replacing; consider exposing the script drv via `finalAttrs`-style so `overrideAttrs` works.

### A4 — MEDIUM: ADR 0006's nvd attribution is not implemented for bash scripts
**Location:** `lib/bash-builders.nix:162` — `version = "0.0.0"; # placeholder, replaced at build time`
**Problem:** The comment is wrong: nothing replaces the derivation's `version`. The digest only reaches the runtime `--version` string. The store path stays `<hash>-name-0.0.0` forever, so `nvd` (which keys on name/version) cannot attribute a script change — contradicting ADR 0006's "nvd per-artifact attribution is preserved via the digest". (Uncertain only in how nvd renders a same-name path change; the version string definitely never moves.)
**Recommendation:** `version = "0.0.0+${srcDigest}"` on the derivation. This is still rev-independent (the existing `bash-version-rev-independent` check would keep passing) and costs nothing — the drv already changes whenever the digest does.

### A5 — MEDIUM: `mkSrcDigest` silently degrades to whole-repo churn on the wrong argument type
**Location:** `lib/version.nix:10-17`
**Problem:** The digest hashes the *string coercion* of each input. For a path literal (`./module`) interpolation imports the subtree and yields a content-addressed store path — correct. But for a string (`"${self}/modules/x"`), `self + "/modules/x"`, or `self` itself, the string embeds the whole-flake store hash, so the digest changes on **every repo change** — silently reintroducing the exact per-commit rebuild/churn problem ADR 0006 exists to fix. No assertion distinguishes the cases, and `version-tests.nix` only tests plain strings (the degenerate case), never actual paths.
**Recommendation:** Assert input types (`builtins.isPath s || s ? outPath`) and `throw` with guidance on strings; add an eval-time test that a path input's digest is independent of a sibling-file change (the bash check covers this indirectly for scripts, nothing covers `mkGoApp`).

### A6 — MEDIUM: Build-time `date` makes outputs non-reproducible (accepted by ADR 0006, consequences underplayed)
**Location:** `lib/bash-builders.nix:184-185`
**Problem:** The script's bytes embed the build timestamp. Same drv built twice produces different outputs: `nix build --check` / `nix store verify` will fail, and a substituted binary's timestamp reflects whenever the cache built it, not anything about your tree. ADR 0006 explicitly accepts the timestamp byte being non-reproducible, but doesn't note the `--check`/verification breakage or that the timestamp can be actively misleading under substitution.
**Recommendation:** Either drop the timestamp from `--version` (the digest is the identity of record anyway, per the ADR), or amend the ADR to document the `--check` consequence so it isn't rediscovered as a bug.

### A7 — MEDIUM: `RevLock` is write-only — its documented purpose is unimplemented
**Location:** `modules/pn/internal/workspace/rev_lock.go:18-20`
**Problem:** The doc says it "drives --override-input pinning"; no override path ever reads a recorded rev. The reproducibility story the file promises doesn't exist.
**Recommendation:** Implement rev-pinned overrides or fix the comment before something trusts it.

### A8 — LOW: Legacy `self` coupling in builder factories
**Location:** `lib/bash-builders.nix:8-15` (requires `self` only to compute a `gitHash` that ADR 0006 deprecated; still exported at line 373), `lib/go-builders.nix:10` ("accepted for signature parity but unused")
**Recommendation:** Drop `self` from both signatures (or make optional) and stop exporting `gitHash`; every removed argument is one less thing consumers thread through.

### A9 — LOW: Flat `//`-merged `lib` output; cross-repo option coupling in module helpers
**Location:** `flake.nix:158-204`; `nix/module-helpers.nix:84` (`phillipgreenii.system.persistentDockApps`)
**Problem:** Five attrsets merged with `//` — a key collision silently shadows; the helper hard-references an option defined in a different repo, so `mkProgramModule` with `dockApp` fails with an unrelated-looking eval error for any consumer lacking that module.
**Recommendation:** Namespace the lib (`lib.version`, `lib.builders`, `lib.devEnv`, `lib.modules`) — breaking, but this is the API everything consumes; document the persistentDockApps contract or assert it with a readable message.

### A10 — LOW: Hand-concatenated TOML in the home module
**Location:** `home/pn/default.nix:18-27`
**Problem:** String-built TOML (`map (d: ''"${d}"'')`) breaks on quotes/backslashes in paths; `keep_days = 14` / `keep_count = 3` are hardcoded non-options. Also `with lib;` (line 16) — the idiom statix/your own pre-commit lint discourages. (Per the Go review, the consuming options structs are currently dead anyway — see U2.)
**Recommendation:** `(pkgs.formats.toml {}).generate`; expose keepDays/keepCount as options.

---

## Best Practices / Code Quality

### B1 — HIGH: `docs/using-mkGoBuilders.md` documents an API that no longer exists
**Location:** `docs/using-mkGoBuilders.md` (quick start `inherit version;`, "factory rejects the literal `dev`", "MUST be `self.lib.mkVersion self`", version-format section); `modules/pn/run-from-source.sh:4-6` ("version guard passes")
**Problem:** ADR 0006 replaced the version contract, but the consumer-facing how-to still instructs passing `version` — which `mkGoApp` now **silently strips** (`go-builders.nix:132-138`) — and documents a `throw` that was removed. A consumer following the docs gets no error and a version they didn't ask for. `run-from-source.sh` injects a version to satisfy a guard that's gone.
**Why it matters:** This repo's docs are the contract for every other flake; ADR 0006 explicitly supersedes ADR 0005's contract but nobody swept the how-to.
**Recommendation:** Rewrite the doc against the current signature; make `mkGoApp` `throw` on a `version` argument with a pointer to ADR 0006 (turns silent drift into a loud migration).

### B2 — MEDIUM: `versionPath` defaults differ between the two Go entry points
**Location:** `lib/go-builders.nix:31` (`mkGoBinary` → `"main.Version"`) vs `go-builders.nix:119` (`mkGoApp` → `"main.version"`)
**Problem:** Moving a package from `mkGoBinary` to `mkGoApp` (the documented "thin wrapper" escape hatch) silently changes the ldflags target; Go's linker does not warn on a missing `-X` symbol, so `--version` quietly reverts to the in-source fallback. The doc itself warns about exactly this silent no-op.
**Recommendation:** One default. Better: post-build assertion that the binary's `--version` output contains the digest (cheap `grep` in checkPhase).

### B3 — MEDIUM: `completions` arg is replaced, not merged
**Location:** `lib/go-builders.nix:22-26, 63-80`
**Problem:** The doc says "set e.g. `completions.fish = false`" — but passing `completions = { fish = false; }` *replaces* the default attrset, and `completions.bash` then throws `attribute 'bash' missing`. Doc and code disagree.
**Recommendation:** `completions = defaults // userCompletions` inside the builder.

### B4 — MEDIUM: `vendorHash ? null` default invites the known footgun
**Location:** `lib/go-builders.nix:19, 117`
**Problem:** `null` means "no vendoring needed"; a new consumer who forgets the argument gets a mid-build `missing go.sum entry`-style failure instead of an eval-time "you must provide vendorHash". Given the documented history of vendorHash/local-replace pain, the silent default is the wrong polarity. The pinned FOD name (`${pname}-go-modules`) also means a *stale-but-existing* local FOD output keeps satisfying builds after `go.mod` changes until the hash is touched — correct Nix behavior, but worth a sentence in the doc since it defers the error to CI.
**Recommendation:** Make `vendorHash` required (consumers with no deps can pass `null` explicitly), or default to `lib.fakeHash` so the first build prints the real hash.

### B5 — MEDIUM: `fix-lint` operates on the immutable store copy — it can never fix anything
**Location:** `flake.nix:83-85` — `statix fix ${./.}`
**Problem:** `${./.}` interpolates the flake source into `/nix/store/...` (read-only). `statix fix` against it either errors or silently fixes nothing; the package is dead weight that looks like tooling.
**Recommendation:** `pkgs.writeShellScriptBin "fix-lint" ''exec ${lib.getExe pkgs.statix} fix "$(git rev-parse --show-toplevel)"''` (run from the working tree).

### B6 — MEDIUM: Non-string config values become JSON *file paths*, including ints and bools
**Location:** `lib/bash-builders.nix:126-134`
**Problem:** `config.PORT = 8080;` yields `PORT=/nix/store/...-script-PORT.json` — the script must `cat`/`jq` a file to get `8080`. Astonishing for scalars; undocumented in the builder header (which says only "attrset of local variables").
**Recommendation:** Inline scalars (`builtins.isInt/isBool/isFloat` → `toString`), keep the JSON-file indirection for lists/attrsets, and document it.

### B7 — LOW: `runtimeDeps` PATH semantics differ between bash and Go builders
**Location:** `lib/bash-builders.nix:216-218` (`--prefix PATH`) vs `lib/go-builders.nix:82` (`--suffix PATH`)
**Problem:** Bash scripts' deps shadow the user's tools; Go binaries' deps defer to them. `modules/pn/default.nix:17-23` even documents the suffix behavior as deliberate — but the bash builder contradicts it. Consumers get different shadowing per language for the same `runtimeDeps` argument.
**Recommendation:** Standardize (suffix, per the pn rationale) or expose the choice.

### B8 — LOW: `allowWarnings` in the shellcheck check helper swallows *errors* too
**Location:** `nix/checks.nix:43-56` — `errorHandling = " || true"`
**Problem:** `|| true` masks every failure, including syntax errors; the check becomes decorative while looking enabled.
**Recommendation:** `shellcheck --severity=error` for the lenient mode instead of discarding exit codes.

### B9 — LOW: mkBashLibrary/mkBashScript hard-require optional-looking inputs
**Location:** `lib/bash-builders.nix:52, 72` (`bats ${testDir}` with no `pathExists` guard — a library without `tests/` fails its check derivation with a confusing bats error); `bash-builders.nix:246-260` (man page via help2man runs `--help` with no fallback — unlike `go-builders.nix:41-53`'s `_try`, a script without `--help` fails the whole build, and `packages` always includes `manPage`)
**Recommendation:** Guard `tests/` existence with a clear `throw` or skip; port the `_try` pattern (or a `manPage ? true` flag) to `mkBashScript`.

### B10 — LOW (uncertain): `testPythonProject` likely needs network inside the sandbox
**Location:** `nix/checks.nix:88-122` — runs `uv`-backed `check-all.sh` in a `runCommand`
**Problem:** uv resolving/installing deps requires network, unavailable in a sandboxed build; this works only where sandboxing is off (default macOS) and will fail on sandboxed Linux CI. Couldn't verify a consumer; flagging as a portability trap in a shared helper.
**Recommendation:** Document the constraint, or restructure around a pre-fetched venv/derivation.

### B11 — LOW: Update-locks library robustness details
**Location:** `lib/scripts/update-locks-lib.bash:74` (`|| return 0` conflates "attribute missing" with *any* nix failure — broken flake silently skips hook reconciliation); `:83` (multiple `^exec` lines in a hook file produce a multiline var and a bogus `-x` test); `:247-252` (steps run as background jobs — a step that prompts on stdin/tty will hang or SIGTTIN); `lib/scripts/update-cache-lib.bash:76` (`timeout` assumed on PATH on macOS — coreutils-only; missing → misleading "exit 127" daemon diagnosis)
**Recommendation:** Distinguish nix-eval-says-missing from nix-failed (`nix eval --raw .#install-pre-commit-hooks.drvPath` probe); `grep -m1`; document the no-stdin contract for steps; degrade gracefully when `timeout` is absent.

### B12 — LOW: Injected `-v`/`--version` handler steals flags from every script
**Location:** `lib/bash-builders.nix:193-196`
**Problem:** Every `mkBashScript` consumes `-v` as version — a script wanting `-v` for verbose can never have it, and the behavior is injected invisibly (also `set -euo pipefail` is forced at line 190, which changes semantics for scripts written without errexit in mind).
**Recommendation:** Handle only `--version` (leave `-v` to the script), or make the handler opt-out; document the injected prelude in the builder header.

### B13 — NITS
- `lib/bash-builders.nix:354-358`: module check keys become `check-foo.bash` for libraries (uses `l.lib.name`, which includes the extension) vs `check-foo` for scripts — inconsistent attr naming in consumers' `checks`.
- `lib/bash-builders.nix:303-304`: in the check, *both* `config` and `exportedConfig` are exported — but at runtime only `exportedConfig` is. Tests see a different env than production.
- `nix/dev-env.nix:104`: `mkShell { buildInputs = ... }` — use `packages`.
- `nix/dev-env.nix:54-56`: the dotnet-sdk stub workaround deserves a comment explaining which hook drags dotnet in, or it will outlive its reason.
- `modules/pn/default.nix:11`: `src = ./.` includes `default.nix`, `run-from-source.sh`, `.gitignore` in both the build src and the digest — editing the packaging rebuilds and re-versions `pn` unnecessarily; use `lib.fileset` over `cmd/`, `internal/`, `go.mod`, `go.sum`.
- `flake.nix:17`: `nixpkgs-unstable` tracks `master`, not `nixos-unstable` — master is pre-Hydra, unvetted; combined with the weekly auto-update workflow you're pulling unbaked commits on a schedule.
- Naming drift: state dir `zn-self-upgrade` (update-cache-lib.bash:10 and Go `updatecache.go`) is a leftover from a predecessor tool; repo is referred to as `nix-repo-base` (update-locks.sh:30), `phillipg-nix-repo-base` (determine-ul-lib-dir.sh:10), and `phillipgreenii-nix-base` (version.nix:79, using-mkGoBuilders.md). **One of update-locks.sh:30 / determine-ul-lib-dir.sh:10 has the wrong GitHub URL** — they fetch differently-named repos (uncertain which is real; if it's update-locks.sh, S4's `nix run` fails for every consumer).

### Go code quality (from the delegated deep-reads; selected, deduplicated)
- **HIGH** `internal/workspace/init.go:42-46`: multi-remote repos (legal per the schema, `config.go:139`) clone with an *empty URL*; secondary remotes never configured. Init is incompatible with its own config schema.
- **HIGH** `internal/workspace/update.go:114`: unconditional `./update-locks.sh` per repo — any repo without the convention (e.g. third-party `homelab` in the reference TOML) fails every sweep, and `upgrade.go:20-22` aborts the whole upgrade on it.
- **MEDIUM** `internal/workspace/template.go:10-13`: `strings.Fields` on substituted command templates shatters paths with spaces (`~/Library/Mobile Documents/...` is common on the target platform); no quoting mechanism exists.
- **MEDIUM** `internal/cli/*`: every `RunE` uses `context.Background()`; no `signal.NotifyContext`; `exec.CommandContext` is effectively dead — Ctrl-C cannot cancel the fan-out. `main.go:18-21` collapses all errors to exit 1, discarding `CommandError.ExitCode` (breaks scripting around the nix passthrough).
- **MEDIUM** `internal/exec/workerpool.go:41-56`: no `recover` in workers (one panicking repo task kills the process), `Submit`-after-`Close` panics by design, `Close` can block forever; `workspace.Open`'s "callers should Close()" contract is honored by exactly one command (goroutine leak per open).
- **MEDIUM** `internal/exec/exec.go:74-81`: output is always fully buffered in RAM even when streamed — nix/darwin-rebuild logs can run to hundreds of MB.
- **MEDIUM** `internal/workspace/init.go:151-157`: `flakeURLToHTTPS("github:o/r/branch")` yields `https://github.com/o/r/branch.git` — mangles ref-carrying flake URLs the schema permits.
- **MEDIUM** `internal/workspace/init.go:123-139`, `lock.go:63`, `rev_lock.go:80`: config/lock writes are non-atomic (`os.WriteFile`, no temp+rename) and re-marshal the TOML destroying comments; no inter-process locking between concurrent `pn` runs.
- **MEDIUM** `internal/workspace/updatecache.go:22-28`: applied-hash state keyed by repo *basename* in a global state dir — two workspaces with same-named repos stomp each other's `needsRebuild` gate.
- **LOW** `internal/workspace/rebase.go:26`: shells out to `git mu` — a personal alias; fails opaquely for anyone else.
- **LOW** `internal/workspace/hooks.go:71-91`: `rewriteFirstToken` mangles quoted first tokens and `ENV=val cmd` prefixes — and is redundant since hooks already run with `Dir: workspaceRoot`.
- **LOW** `internal/workspace/tree.go:56-60`: `tree --all-inputs` (read-only-sounding) writes `flake.lock` and hits the network as a side effect.
- **LOW** `cmd/pn/cli_integration_test.go:19-27`: `defer os.RemoveAll(tmp)` before `os.Exit(m.Run())` — cleanup never runs.

---

## Testing

### T1 — HIGH: The bash-builders test suite is dead code
**Location:** `lib/bash-builders-tests/default.nix` (defines `checks` for 5 fixtures + a module-shape assertion; 18 bats tests under the fixtures)
**Problem:** Nothing imports it. `flake.nix:101-134` wires formatting, linting, shellcheck, update-locks, version-lib, rev-independence, pn — **not** `bash-builders-tests`. Verified by grep: zero references outside the directory itself. The only test coverage of `mkBashScript`'s config injection, library composition, public/internal packaging, and `mkBashModule` aggregation never executes.
**Why it matters:** The foundational builder used by every consumer has a test suite that has silently rotted out of CI; regressions (like S1) ship invisibly.
**Recommendation:** `checks = ... // (import ./lib/bash-builders-tests { inherit bashBuilders pkgs; }).checks` in `flake.nix`. Then fix whatever broke while it was dark.

### T2 — HIGH: `mkBashScript`'s check tests the raw source, never the assembled artifact
**Location:** `lib/bash-builders.nix:279-308` — the check exports `SCRIPTS_DIR="${src}"` / `LIB_PATH` and runs bats against the *source* files
**Problem:** The shipped script is the assembled one — version handler + injected `set -euo pipefail` + library `source` lines + config lines + wrapper PATH. None of that is exercised by the check. A config-injection breakage (S1), a library composition ordering bug, or a wrapProgram mistake passes tests and fails at runtime in every consumer.
**Recommendation:** Point the check at `${script}/bin/${name}` (export `SCRIPT_UNDER_TEST`), or at minimum add a smoke test (`$out/bin/$name --version` exits 0 and prints the digest) inside the script derivation's checkPhase.

### T3 — MEDIUM: The Go builders have zero tests
**Location:** `lib/go-builders.nix` (no fixture, no check)
**Problem:** Bash got a rev-independence check (`lib/bash-builders-version-tests.nix`) per ADR 0006; `mkGoApp`/`mkGoBinary` — the builders that motivated the ADR — have none: nothing pins the FOD-name behavior (`${pname}-go-modules`), nothing verifies the ldflags version lands in the binary, nothing catches B2/B3.
**Recommendation:** A tiny fixture Go module + eval-time assertions (drvPath rev-independence, goModules drv name) and a build-time `--version`-contains-digest check.

### T4 — MEDIUM: `pn`'s fake-exec test strategy verifies argv shape only
**Location:** `modules/pn/internal/exec/fake.go:47-59`; tests across `internal/*`
**Problem:** The fake matches `(name, args)` but never `Dir`/`Env`/stdin — dropping `Dir: workspaceRoot` from hooks would pass every test. Exact SQL/argv strings duplicated into tests prove only self-consistency, not that git/nix/sqlite accept them. Integration coverage is `--version`, `--help`, `workspace status`; the destructive paths (`store deepclean`, `osx tcc-check`) have zero end-to-end tests. Fuzzing targets `ParseConfig` and a trivially-total helper instead of the external-data parsers (`slug.go` regexes, flake.lock JSON graph builders).
**Recommendation:** Add Dir/Env matchers to FakeRunner; scratch-store/sqlite-file integration tests for store/osx; retarget fuzzing.
**Untested behaviors identified by the deep-read:** init's multi-remote form, missing `update-locks.sh`, deny-list flag bypass (S5), templates with spaces, cycle-fallback lock writes, hook output handling, workerpool panic/Close/Submit-after-Close, zero/negative worker clamping.

### T5 — MEDIUM: `mkSrcDigest` tests only exercise the degenerate string case
**Location:** `lib/version-tests.nix:60-97`
**Problem:** All digest tests pass literal strings ("src-a", "a", "b"). The load-bearing behavior — a *path* input importing to a content-addressed store path, and the string-input degradation of A5 — is untested. The transitivity contract in ADR 0006 ("paths passed MUST equal the derivation's source inputs") has no enforcement or test for `mkGoApp`.
**Recommendation:** Add path-based cases (two fixture dirs; digest stable across unrelated-file changes) and a type-assertion test once A5 lands.

### T6 — LOW: update-locks bats tests mock `nix` to `exit 0` wholesale
**Location:** `lib/tests/test-update-locks-lib.bats:30-38`
**Problem:** Every `nix fmt` / `nix build` path is a no-op in tests; the `_ul_ensure_pre_commit_hooks` three-tier logic and the fmt-failure rollback path are structurally untestable under this mock. Reasonable for unit scope, but nothing else covers them.
**Recommendation:** One nix-enabled check (you already build inside `runCommand` elsewhere) or a smarter mock that can fail on demand (it already could — add cases driving `nix fmt` to fail and asserting rollback).

---

## UX / DX

### U1 — HIGH: The shipped agent plugin documents commands that don't exist
**Location:** `pn-workspace-rules/CLAUDE.md` (entire command table: `pn-workspace-build`, `pn-ws-nix`, `pn-workspace-apply`, `pn-workspace-flake-check`, ...); `testdata/pn-workspace-reference.toml:27` (`pn-osx-tcc-check` hook)
**Problem:** The Go binary's actual surface is `pn workspace build`, `pn workspace nix`, `pn osx tcc-check` (verified against `internal/cli/*.go` `Use:` strings). This plugin is distributed to every consuming repo precisely to steer agents — and it steers them to commands that fail, including the "Completion Gate" they MUST run. Internal Go comments (`flake_check.go:21`) still reference the old names too.
**Recommendation:** Rewrite the plugin doc against the real CLI; bump the plugin version; fix the reference TOML hook example.

### U2 — HIGH: `pn` has no registered flags anywhere; several commands are silent shells
**Location:** no `Flags()` call under `modules/pn/internal/cli` or `cmd`; `internal/store/audit.go:28,33`, `internal/store/deepclean.go:34`, `internal/osx/tcc_darwin.go:70` (captured stdout discarded); `internal/workspace/nix.go:47` (passthrough gets no Stdout/Stderr/Stdin)
**Problem:** `BuildOptions.{BuildCmd,OverridePaths,ShowNixCommandsOnly}`, `ApplyOptions.Force`, `TreeOptions.AllInputs`, `AuditOptions.Full`, `InitOptions` are all dead; `resolveWorkspaceRoot`'s documented `--root` flag doesn't exist. `pn store audit` and `pn osx tcc-check` print nothing and "succeed"; `pn workspace nix build`/`repl`/`run` show no output and cannot prompt. The command surface was wired before the behavior was ported (the TODOs say so), but nothing marks the unfinished commands as such.
**Recommendation:** Wire flags or delete the fields; stream subprocess output for the ported-in-name-only commands; hide/mark experimental until done; propagate real exit codes (`errors.As` → `CommandError.ExitCode` in main).

### U3 — MEDIUM: Convention assumptions make whole workflows fail for ordinary setups
**Location:** `internal/workspace/update.go:114` (every repo must have `./update-locks.sh` — see Go HIGH above), `update.go:90-92` ("git failed" reported as "dirty, skipping" for not-yet-cloned repos), `rebase.go:26` (`git mu` personal alias), `push.go:28-30` (repos without upstream silently skipped — detached HEAD looks "pushed")
**Recommendation:** Gate on file existence; distinguish error classes; inline the real rebase command; print skip notices.

### U4 — MEDIUM: Builder consumption errors surface as cryptic build failures, not eval-time messages
**Location:** `lib/bash-builders.nix:39` (`builtins.readFile (src + "/${name}.bash")` — a name/file mismatch throws a raw readFile error), `:149` (same for `${name}.sh`), `:72` (missing tests dir → bats usage error mid-build); `lib/go-builders.nix` (no arg validation at all — typo'd argument names are silently forwarded to `buildGoModule` or silently dropped by `removeAttrs`)
**Problem:** Given this is the API every repo consumes, the failure UX *is* the product. There is no schema: no required-arg messages, no unknown-arg detection, no type checks.
**Recommendation:** Eval-time asserts with actionable messages (`"mkBashScript ‘${name}’: expected ${name}.sh in ${toString src}"`); see Modernization for the structural fix.

### U5 — LOW: justfile gaps
**Location:** `justfile`
**Problem:** All recipes are pn/Go-centric; there's no recipe for the things this repo uniquely owns — running a single flake check (`nix build .#checks.<sys>.bash-version-rev-independent` is the actual incantation), rebuilding the dead fixture suite, or running `update-locks.sh`. `run *args:` passes `{{ args }}` unquoted (space-containing args shatter).
**Recommendation:** Add `check-one name`, `update-locks` recipes; quote `'{{ args }}'`.

### U6 — LOW: Repo hygiene
**Location:** repo root `result-1`, `result-2`, `result-3` symlinks (stale since May 29, pointing at old check outputs); `.pre-commit-config.yaml` is a symlink into the store (breaks if GC'd before a dev-shell re-entry — the update-locks lib even has self-heal logic for this, evidence the footgun fires)
**Recommendation:** Delete stale result symlinks (they're gitignored but clutter and pin GC roots); consider `nix-direnv`-managed GC rooting for the pre-commit config target.

---

## Modernization & Alternatives

1. **flake-parts + a typed module for builder args** (replaces `flake-utils` + the hand-merged `lib` output, `flake.nix:48,158-204`). The biggest systemic gap is U4/A3: builder arguments have no schema, no validation, no overridability. Defining `mkBashScript`/`mkGoApp` arguments as module options (`lib.evalModules` internally, or full flake-parts `perSystem` modules) gives type errors with locations, defaults documentation for free, and `mkForce`-style overriding — directly addressing the vendorHash/footgun history. This is the single highest-leverage structural change.
2. **nix-unit** (or `lib.runTests` kept, but run via nix-unit's runner) for the pure-eval tests — better diffs, no `runCommand` indirection (`flake.nix:113-122`), and it would make adding the missing path-based `mkSrcDigest` tests (T5) cheap. **namaka** is worth a look for snapshot-testing builder-generated script text (would have caught S1/B6 classes by inspection).
3. **gomod2nix or buildGoApplication** as an alternative to `vendorHash` entirely: deps are pinned by a checked-in `gomod2nix.toml` generated from `go.sum`, eliminating the stale-hash footgun class (B4) at the cost of one more generated file. Given your vendorHash history, evaluate it for `mkGoApp`'s backend; the FOD-name-pinning hack (`go-builders.nix:146`) becomes unnecessary.
4. **`lib.fileset`** for every `src` (modules/pn/default.nix, and as the documented idiom for `mkSrcDigest` inputs): unions are explicit, eval-time-checked, and compose — and they make the ADR 0006 transitivity contract auditable rather than aspirational.
5. **direnv-style trust for workspace hooks** (S2): a `pn workspace allow` writing a content hash under `~/.local/state/pn/trust/` is ~50 lines and closes the only real RCE-class hole.
6. **`pkgs.formats.toml`** for generated TOML (A10); **`sqlc`-style or at least parameterized queries** are overkill for the TCC probe, but the SQL string should at minimum live next to a real-sqlite integration test (T4).
7. **Process-level**: a `docs/` drift check — the using-mkGoBuilders/ADR/plugin mismatches (B1, U1) all postdate ADR 0006 by zero days; a checklist item in the ADR template ("sweep: docs/, plugin CLAUDE.md, run-from-source headers, Go comments") or a CI grep for retired command names (`pn-ws-nix|pn-workspace-build|mkVersion self` in docs) is cheap insurance.
8. Already adopted and fine (no action): treefmt, statix/deadnix in pre-commit, prek, bats, SHA-pinning (partially — see S8), Determinate Nix in CI.

---

## Prioritized Action List

| # | Sev | Finding | Location | Action |
|---|-----|---------|----------|--------|
| 1 | High | Bash-builders test suite never runs (T1) | `lib/bash-builders-tests/default.nix` | Wire `.checks` into `flake.nix` checks; fix fallout |
| 2 | High | Config injection in mkBashScript (S1) | `lib/bash-builders.nix:124` | `lib.escapeShellArg` + varName assert |
| 3 | High | `sudo nix-store --gc` unconditional; flags dead (S3, U2) | `internal/store/deepclean.go:41`, `internal/cli/store.go:36` | Wire flags, require confirmation, hide until ported |
| 4 | High | Hooks `sh -c` with no trust gate (S2) | `internal/workspace/hooks.go:52` | direnv-style allow gate |
| 5 | High | using-mkGoBuilders.md documents removed API; `version` silently dropped (B1, A3) | `docs/using-mkGoBuilders.md`, `lib/go-builders.nix:132-138` | Rewrite doc; `throw` on caller `version` |
| 6 | High | Agent plugin ships stale command names (U1) | `pn-workspace-rules/CLAUDE.md` | Rewrite against real `pn` CLI |
| 7 | High | mkBashScript check never tests assembled script (T2) | `lib/bash-builders.nix:279-308` | Test `${script}/bin/${name}`; add `--version` smoke |
| 8 | High | Override scheme split `git+file://` vs `path:` (A1) | `internal/workspace/helpers.go:59,86` | Unify |
| 9 | High | Silent empty-inputs on eval failure → wrong builds (A2) | `internal/workspace/inputs.go:27-31`, `discover.go:39` | Distinguish missing vs failed; warn/abort |
| 10 | High | Init broken for multi-remote repos; update requires update-locks.sh everywhere | `internal/workspace/init.go:42`, `update.go:114` | Clone from canonicalURL + add remotes; gate on file existence |
| 11 | Med | Bash drv version stuck at 0.0.0 — nvd attribution unimplemented (A4) | `lib/bash-builders.nix:162` | `version = "0.0.0+${srcDigest}"` |
| 12 | Med | mkSrcDigest silently degrades on string inputs (A5, T5) | `lib/version.nix:10-17` | Type assert; add path-based tests |
| 13 | Med | Deny-list flag bypass (S5) | `internal/workspace/nix.go:55` | Skip leading flags; test |
| 14 | Med | `versionPath`/`completions`/`vendorHash` arg footguns (B2, B3, B4) | `lib/go-builders.nix:19-31,117-119` | Unify default; merge completions; require vendorHash |
| 15 | Med | `fix-lint` fixes the store copy (B5) | `flake.nix:83-85` | Run against working tree |
| 16 | Med | `update-locks.sh` unpinned remote exec + repo-name mismatch (S4, B13) | `update-locks.sh:30`, `determine-ul-lib-dir.sh:10` | Pin rev / resolve locally; fix the wrong URL |
| 17 | Med | No signal handling / exit-code collapse / silent passthrough in pn (U2) | `cmd/pn/main.go:18`, `internal/cli/*` | `signal.NotifyContext`, `ExecuteContext`, propagate `CommandError.ExitCode`, stream nix passthrough I/O |
| 18 | Med | Non-atomic TOML/lock writes; comment-destroying re-marshal | `internal/workspace/init.go:123`, `lock.go:63` | temp+rename; doc-preserving TOML edits |
| 19 | Med | Go builders untested (T3) | `lib/go-builders.nix` | Fixture + rev-independence + FOD-name checks |
| 20 | Med | Workerpool panic/Close/leak trio | `internal/exec/workerpool.go:41-56` | recover; guard Submit; Close in CLI |
| 21 | Low | Scalar config → JSON file path surprise (B6) | `lib/bash-builders.nix:126` | Inline scalars |
| 22 | Low | `-v` stolen by version handler (B12) | `lib/bash-builders.nix:193` | `--version` only |
| 23 | Low | PATH prefix/suffix inconsistency (B7) | `bash-builders.nix:217` vs `go-builders.nix:82` | Standardize on suffix |
| 24 | Low | `allowWarnings` swallows errors (B8) | `nix/checks.nix:45` | `--severity=error` |
| 25 | Low | Drop legacy `self`/`gitHash` from builder factories (A8) | `lib/bash-builders.nix:8`, `lib/go-builders.nix:10` | Remove |
