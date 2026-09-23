// internal/workspace/doctor_checks_nixcachetrust.go
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// checkNixCacheTrusted guards against pn having zero visibility into nix's own
// flake-config trust layer (bd tc-3ips5, confirmed gap from tc-dywnx's sweep):
// a repo can declare nixConfig.extra-substituters / extra-trusted-public-keys
// in its flake.nix, but nix only actually USES those caches once the user has
// interactively accepted the exact combination — recorded, per nix's own
// trust-on-first-use store, in ~/.local/share/nix/trusted-settings.json
// (resolved properly below, not hardcoded). When a repo's nixConfig changes,
// or on a fresh machine before first accept, nix silently falls back to not
// using those caches: builds still succeed, just slower, with only a
// transient stderr warning during evaluation (observed live 2026-09-19,
// tc-dywnx). Structurally identical to the hooks-trusted gap tc-atsmj already
// fixed (checkHooksTrusted, doctor_checks_hooks.go) one layer up the trust
// stack — pn already has a compensating-control doctor check for ITS OWN
// trust-on-first-use (workspace hooks); this extends the same idea to nix's.
//
// Applicability: a repo that declares NEITHER list in nixConfig has nothing
// for this check to assert about — no finding (case 4 below).
//
// Severity mirrors hooks-trusted/pre-commit-hook-live's precedent for this
// exact shape of problem (a condition that degrades silently rather than
// failing loudly, where the detection itself must not become a new
// hard-failure mode) — ratified by GAP 3's ruling in bd tc-atsmj:
// Severity: SevError, Skipped: true, so the finding is fully visible in
// human/--json output (DoctorReport.Skipped, renderHuman) but NEVER fails the
// exit code, not even under --strict (doctor.go's ExitCode / hasAny both
// exclude Skipped findings unconditionally).
func (ws *Workspace) checkNixCacheTrusted(ctx context.Context, _ *doctorEnv) []Finding {
	trusted, trustFileExists := loadNixTrustedSettings(nixTrustedSettingsPathFn())

	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		flakeRel := ws.resolveFlakePath(name)
		if flakeRel == "" {
			continue
		}
		absFlake := filepath.Join(repoDir, flakeRel)
		if _, err := os.Stat(absFlake); err != nil {
			continue // no flake.nix on disk yet -- nothing to read
		}
		cfg, declared := readNixConfig(ctx, ws.runner, absFlake)
		if !declared {
			continue // no nixConfig.extra-substituters/extra-trusted-public-keys declared
		}
		if f := nixCacheTrustFinding(name, cfg, trusted, trustFileExists); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// nixTrustedSettingsPathFn resolves the on-disk path to nix's own
// trusted-settings.json. It is a package-level var (not a plain function
// call), the injection seam this check's tests override so they never touch
// the real machine's trust-on-first-use store -- the same shape of seam
// internal/trust's tests use for THEIR trust record (t.Setenv on the XDG base
// dir env var trust.stateDir reads), generalized to a full path override here
// because nix's own file resolves under a DIFFERENT XDG base directory
// (XDG_DATA_HOME, not XDG_STATE_HOME) than pn's own trust records do.
var nixTrustedSettingsPathFn = defaultNixTrustedSettingsPath

// defaultNixTrustedSettingsPath returns
// ${XDG_DATA_HOME:-~/.local/share}/nix/trusted-settings.json, mirroring nix's
// own getDataDir()-based resolution (the file this repo observed live at
// exactly that path).
func defaultNixTrustedSettingsPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "nix", "trusted-settings.json")
}

// nixTrustedSettings mirrors trusted-settings.json's on-disk shape: two
// independent maps (one per nixConfig setting kind), each keying a
// space-joined, already-concatenated settings string to whether the user
// accepted exactly that combination. Kept as two separate maps (rather than
// merged) since a repo's extra-substituters list and extra-trusted-public-keys
// list are matched against the trust file independently.
type nixTrustedSettings struct {
	Substituters map[string]bool `json:"extra-substituters"`
	TrustedKeys  map[string]bool `json:"extra-trusted-public-keys"`
}

// loadNixTrustedSettings reads and parses path. A missing file returns a
// zero-value nixTrustedSettings and exists=false -- the fresh-machine,
// before-first-accept case named explicitly in the bead. A genuine
// read/parse error is swallowed the same way (the edges.go convention: an
// error here must never be mistaken for "trusted", only ever for "not yet
// trusted").
func loadNixTrustedSettings(path string) (nixTrustedSettings, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nixTrustedSettings{}, false
	}
	var s nixTrustedSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nixTrustedSettings{}, false
	}
	return s, true
}

// nixConfigSpec is one repo's declared nixConfig.extra-substituters /
// extra-trusted-public-keys, as read from its flake.nix.
type nixConfigSpec struct {
	Substituters []string
	TrustedKeys  []string
}

// declared reports whether cfg has anything for checkNixCacheTrusted to
// assert about -- a repo declaring neither list has opted into nothing here.
func (c nixConfigSpec) declared() bool {
	return len(c.Substituters) > 0 || len(c.TrustedKeys) > 0
}

// nixConfigEvalExpr is the --apply expression passed to `nix eval` in
// readNixConfig, extracted to a named constant (rather than an inline
// literal) so this check's tests can script a FakeRunner response against
// the exact same string without risking silent drift between the two.
const nixConfigEvalExpr = `c: { s = c.extra-substituters or []; k = c."extra-trusted-public-keys" or []; }`

// readNixConfig evaluates repo's flake.nix nixConfig attribute via `nix
// eval`, reusing the exact --file/--apply pattern edges.go's evalInputSpecs
// already established in this package for extracting structured data out of
// flake.nix, rather than hand-rolling a new Nix-syntax parser. nixConfig is a
// static top-level attribute (unlike `outputs`), so evaluating just it forces
// no input fetching and needs no network -- this is safe to run even
// offline, unlike flakeHasOutput's evaluation of actual flake outputs.
//
// ok is false when the flake declares no nixConfig at all (attribute simply
// absent -- nix eval fails with an ordinary "attribute not found", not a
// crash) or when eval/parse fails for any other reason. Both are swallowed
// the same way (edges.go convention: an un-evaluable nixConfig must read as
// "nothing declared", never as a false claim that something is or isn't
// trusted).
func readNixConfig(ctx context.Context, runner exec.Runner, absFlakePath string) (nixConfigSpec, bool) {
	res, err := runner.Run(ctx, "nix", []string{
		"eval", "--json", "--file", absFlakePath, "nixConfig", "--apply", nixConfigEvalExpr,
	}, exec.RunOptions{})
	if err != nil {
		return nixConfigSpec{}, false
	}
	var raw struct {
		S []string `json:"s"`
		K []string `json:"k"`
	}
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		return nixConfigSpec{}, false
	}
	spec := nixConfigSpec{Substituters: raw.S, TrustedKeys: raw.K}
	return spec, spec.declared()
}

// nixCacheTrustFinding compares repo's declared nixConfig against trusted,
// space-joining each declared list into the same concatenated-string-key
// format nix itself uses (see the package doc comment above), and returns a
// Skipped finding naming which declared setting(s) are not (yet) trusted, or
// nil when everything declared is trusted.
func nixCacheTrustFinding(repo string, cfg nixConfigSpec, trusted nixTrustedSettings, trustFileExists bool) *Finding {
	var mismatches []string
	if len(cfg.Substituters) > 0 && !trusted.Substituters[strings.Join(cfg.Substituters, " ")] {
		mismatches = append(mismatches, "extra-substituters")
	}
	if len(cfg.TrustedKeys) > 0 && !trusted.TrustedKeys[strings.Join(cfg.TrustedKeys, " ")] {
		mismatches = append(mismatches, "extra-trusted-public-keys")
	}
	if len(mismatches) == 0 {
		return nil
	}

	reason := fmt.Sprintf("declared nixConfig.%s is not an accepted entry in trusted-settings.json", strings.Join(mismatches, "/"))
	if !trustFileExists {
		reason = "trusted-settings.json does not exist yet (fresh machine, before first accept)"
	}

	return &Finding{
		CheckID: "nix-cache-trusted", Repo: repo, Severity: SevError, Skipped: true,
		Message: fmt.Sprintf(
			"repo %q declares nixConfig.%s but %s; nix silently ignores these settings at build time until accepted -- builds may run slower or without cache hits (nix prompts to accept on the next interactive build, or run `nix build --accept-flake-config` once reviewed)",
			repo, strings.Join(mismatches, "/"), reason,
		),
		Manual: "review the repo's flake.nix nixConfig, then accept it (nix prompts on the next interactive build; or pass --accept-flake-config once reviewed)",
	}
}
