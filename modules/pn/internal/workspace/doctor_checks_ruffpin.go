// internal/workspace/doctor_checks_ruffpin.go
package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// checkRuffPin guards the single-ruff-version invariant for uv Python packages
// (bd pg2-671gg): a package's `ruff` dependency MUST be exact-pinned to the SAME
// version as the nixpkgs ruff that the repo's generated
// `.pre-commit-config.yaml` `ruff` / `ruff-format` hooks execute.
//
// Two independently-pinned toolchains formatted the same files: the app's uv
// ruff (resolved from a `>=` spec, so it floated to the newest release on every
// relock) and the nixpkgs ruff (fixed until the flake's nixpkgs input moves).
// When the float crossed the 0.15→0.16 formatter-behaviour boundary the two
// disagreed, the mutating quick-check rewrote a committed file mid-commit, and
// pre-commit's "files were modified by this hook" rule halted
// `pn workspace update` with rc=1. Exact-pinning closed that, but trades a
// FLOATING drift for a SILENT STALE PIN: the next nixpkgs ruff bump moves the
// hook and leaves the pin behind with nothing to notice. This check is what
// notices.
//
// Findings (per pyproject.toml, per repo):
//   - ruff-pin-drift (ERROR)   — exact-pinned, but to a different version than
//     the nixpkgs ruff the hooks run.
//   - ruff-pin-floating (WARN) — not exact-pinned (`>=`, `~=`, unbounded, or a
//     multi-clause range), so a relock can float it again.
//   - ruff-pin (SKIP)          — the generated config is absent, so the nixpkgs
//     side is unknown and nothing is asserted.
//
// The check is READ-ONLY: it reads pyproject.toml files and the generated
// config, and runs no commands. It is deliberately NOT auto-fixable — the remedy
// is a pin edit PLUS a `uv lock` relock PLUS (across a formatter boundary) a
// reformat of the affected sources, which is a reviewable change, not a
// mechanical repair. Every finding carries a `Manual` hint instead.
func (ws *Workspace) checkRuffPin(_ context.Context, _ *doctorEnv) []Finding {
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		out = append(out, ws.ruffPinFindings(name, repoDir)...)
	}
	return out
}

// ruffPinFindings audits one repo. It returns nothing when the repo declares no
// ruff dependency (the invariant does not apply) or when its generated config
// runs no nixpkgs ruff hook (there is no second toolchain to unify).
func (ws *Workspace) ruffPinFindings(repo, repoDir string) []Finding {
	pins := findRuffRequirements(repoDir)
	if len(pins) == 0 {
		return nil
	}

	versions, status := nixpkgsRuffVersions(repoDir)
	switch status {
	case ruffConfigAbsent:
		// The generated config is a git-hooks.nix symlink into /nix/store; it is
		// gitignored (ADR 0016) and therefore missing in every fresh clone and
		// worktree until the hooks are installed. Absent config means the nixpkgs
		// side is UNKNOWN, so skip loudly rather than fail falsely.
		return []Finding{{
			CheckID: "ruff-pin", Repo: repo, Severity: SevError, Skipped: true,
			// renderHuman prints Manual only for "[manual]"-tagged findings, so a
			// skipped finding's hint would be invisible outside --json; the remedy
			// is repeated in the message for that reason.
			Message: fmt.Sprintf(
				"repo %q declares a ruff dependency but %s is absent (generated, gitignored); nixpkgs ruff version unknown, pin not verified — run `nix run .#install-pre-commit-hooks` and re-run doctor",
				repo, preCommitConfigName,
			),
			Manual: fmt.Sprintf("generate it, then re-run doctor:  (cd %s && nix run .#install-pre-commit-hooks)", repoDir),
		}}
	case ruffConfigNoRuffHook:
		return nil // config present, no nixpkgs ruff hook — only one toolchain
	}

	if len(versions) > 1 {
		return []Finding{{
			CheckID: "ruff-pin", Repo: repo, Severity: SevWarning,
			Message: fmt.Sprintf(
				"repo %q: %s runs %d different nixpkgs ruff versions (%s); cannot assert a single pin",
				repo, preCommitConfigName, len(versions), strings.Join(versions, ", "),
			),
		}}
	}
	want := versions[0]

	var out []Finding
	for _, p := range pins {
		switch {
		case !p.exact:
			out = append(out, Finding{
				CheckID: "ruff-pin-floating", Repo: repo, Severity: SevWarning,
				Message: fmt.Sprintf(
					"%s declares ruff as %q; it MUST be exact-pinned (\"ruff==%s\") to the nixpkgs ruff the %s hooks run, or a relock can float it across a formatter-behaviour boundary",
					p.file, p.requirement, want, preCommitConfigName,
				),
				Manual: ruffPinManual(repoDir, p.file, want),
			})
		case p.version != want:
			out = append(out, Finding{
				CheckID: "ruff-pin-drift", Repo: repo, Severity: SevError,
				Message: fmt.Sprintf(
					"%s pins ruff==%s but the %s hooks run nixpkgs ruff %s; the app and its pre-commit checks would format differently",
					p.file, p.version, preCommitConfigName, want,
				),
				Manual: ruffPinManual(repoDir, p.file, want),
			})
		}
	}
	return out
}

// ruffPinManual is the copy-pasteable remedy: edit the pin, then relock so
// uv.lock (load-bearing for the build, per ADR 0022) agrees with it.
func ruffPinManual(repoDir, relFile, want string) string {
	pkgDir := filepath.Join(repoDir, filepath.Dir(relFile))
	return fmt.Sprintf("set \"ruff==%s\" in %s, then relock:  (cd %s && uv lock)", want, relFile, pkgDir)
}

// preCommitConfigName is the generated git-hooks.nix config at a repo root.
// pre-commit resolves it from the git root, not from the flake directory, so it
// is always looked up there.
const preCommitConfigName = ".pre-commit-config.yaml"

type ruffConfigStatus int

const (
	ruffConfigFound ruffConfigStatus = iota
	ruffConfigAbsent
	ruffConfigNoRuffHook
)

// nixStoreRuffRe matches a nixpkgs ruff store path as it appears in a generated
// hook `entry`, e.g. `/nix/store/<hash>-ruff-0.15.14/bin/ruff check --fix`.
// Requiring the version to start with a digit keeps it from matching sibling
// derivations such as `ruff-lsp-<version>`; requiring the `/bin/ruff` suffix
// keeps it to paths actually executed as ruff.
var nixStoreRuffRe = regexp.MustCompile(`/nix/store/[0-9a-z]+-ruff-([0-9][^/"'\s]*)/bin/ruff`)

// nixpkgsRuffVersions returns the sorted distinct nixpkgs ruff versions the
// repo's generated pre-commit config executes. The file is a symlink into
// /nix/store, so an unreadable path (never generated, or a dangling symlink
// after a GC) is reported as absent rather than as a mismatch.
func nixpkgsRuffVersions(repoDir string) ([]string, ruffConfigStatus) {
	data, err := os.ReadFile(filepath.Join(repoDir, preCommitConfigName))
	if err != nil {
		return nil, ruffConfigAbsent
	}
	seen := map[string]bool{}
	for _, m := range nixStoreRuffRe.FindAllStringSubmatch(string(data), -1) {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		return nil, ruffConfigNoRuffHook
	}
	versions := make([]string, 0, len(seen))
	for v := range seen {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return versions, ruffConfigFound
}

// ruffPin is one ruff requirement found in one pyproject.toml. file is relative
// to the repo root so findings and Manual hints stay readable.
type ruffPin struct {
	file        string
	requirement string
	version     string // set only when exact
	exact       bool
}

// ruffPinWalkMaxDepth bounds the pyproject.toml search to the conventional
// layouts (a repo-root pyproject, or `packages/<pkg>/pyproject.toml` and
// `lib/fixtures/<x>/pyproject.toml` at depth 3), so the walk stays cheap on a
// large repo.
const ruffPinWalkMaxDepth = 4

// ruffPinPrunedDirs are never descended: VCS metadata, virtualenvs, tool caches,
// vendored trees, and build output. Nothing under them is a declared dependency
// of this repo.
var ruffPinPrunedDirs = map[string]bool{
	".git": true, ".venv": true, "venv": true, ".direnv": true,
	"node_modules": true, ".mypy_cache": true, ".ruff_cache": true,
	".pytest_cache": true, "__pycache__": true, "htmlcov": true,
	"dist": true, "build": true, "target": true,
	".worktrees": true, ".workforests": true,
}

// findRuffRequirements returns every ruff requirement declared by every
// pyproject.toml in the repo, in path order. Errors are swallowed as "no pin
// here" (the edges.go convention): the check asserts a version MATCH and must
// never turn an unreadable or unparseable file into a drift report.
func findRuffRequirements(repoDir string) []ruffPin {
	var pins []ruffPin
	_ = filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // unreadable subtree declares no dependency
		}
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == repoDir {
				return nil
			}
			depth := len(strings.Split(rel, string(filepath.Separator)))
			if ruffPinPrunedDirs[d.Name()] || depth >= ruffPinWalkMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "pyproject.toml" {
			return nil
		}
		for _, req := range ruffRequirementsIn(path) {
			spec, _ := parseRuffRequirement(req)
			v, exact := exactRuffVersion(spec)
			pins = append(pins, ruffPin{file: rel, requirement: req, version: v, exact: exact})
		}
		return nil
	})
	return pins
}

// ruffRequirementsIn returns the ruff requirement strings a pyproject.toml
// declares. The document is DECODED rather than regexed — so a commented-out pin
// is invisible — and only genuine dependency tables are inspected, so a `"ruff"`
// string appearing as some tool's configuration value is not mistaken for an
// unbounded requirement.
func ruffRequirementsIn(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	var out []string
	for _, req := range declaredRequirements(doc) {
		if _, ok := parseRuffRequirement(req); ok {
			out = append(out, req)
		}
	}
	return out
}

// requirementTablePaths are the keys under which a pyproject.toml may declare
// requirement strings. Each resolved value is flattened by requirementStrings,
// so both a bare list (`project.dependencies`) and a map of lists
// (`dependency-groups`, `project.optional-dependencies`) are covered.
var requirementTablePaths = [][]string{
	{"project", "dependencies"},
	{"project", "optional-dependencies"},
	{"dependency-groups"},
	{"tool", "uv", "dev-dependencies"},
	{"tool", "uv", "constraint-dependencies"},
	{"tool", "uv", "override-dependencies"},
	{"build-system", "requires"},
}

// declaredRequirements flattens every requirement string in doc's dependency
// tables, deterministically ordered.
func declaredRequirements(doc map[string]any) []string {
	var out []string
	for _, path := range requirementTablePaths {
		out = append(out, requirementStrings(valueAt(doc, path))...)
	}
	return out
}

// valueAt walks doc along path, returning nil if any hop is missing or is not a
// table.
func valueAt(doc map[string]any, path []string) any {
	var cur any = doc
	for _, key := range path {
		table, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = table[key]
		if !ok {
			return nil
		}
	}
	return cur
}

// requirementStrings flattens v into its string leaves. Maps are traversed in
// sorted key order (so `dependency-groups` and `optional-dependencies` yield a
// stable sequence); non-string leaves — e.g. a `{include-group = "dev"}` entry in
// a dependency group — are skipped, since they name a group, not a requirement.
func requirementStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			out = append(out, requirementStrings(t[k])...)
		}
		return out
	default:
		return nil
	}
}

// parseRuffRequirement reports whether req is a PEP 508 requirement naming ruff
// and, if so, returns its version specifier ("==0.15.14", ">=0.8.0", or "" when
// unbounded). A direct URL reference (`ruff @ https://…`) pins no version this
// check can compare, so it is not treated as a ruff requirement.
func parseRuffRequirement(req string) (spec string, ok bool) {
	s := strings.TrimSpace(req)
	if i := strings.IndexByte(s, ';'); i >= 0 { // drop the environment marker
		s = strings.TrimSpace(s[:i])
	}
	n := 0
	for n < len(s) && isRequirementNameByte(s[n]) {
		n++
	}
	if normalizeRequirementName(s[:n]) != "ruff" {
		return "", false
	}
	rest := strings.TrimSpace(s[n:])
	if strings.HasPrefix(rest, "[") { // extras
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return "", false
		}
		rest = strings.TrimSpace(rest[end+1:])
	}
	if rest != "" && !strings.ContainsAny(rest[:1], "=<>!~") {
		return "", false // "@ <url>", or some other string that merely starts with "ruff"
	}
	return rest, true
}

// exactRuffVersion returns the version a specifier pins EXACTLY. Only a single
// `==`/`===` clause with a literal version qualifies: a wildcard (`==0.15.*`) or
// any multi-clause range still lets a relock move the resolved version.
func exactRuffVersion(spec string) (string, bool) {
	if strings.ContainsRune(spec, ',') {
		return "", false
	}
	for _, op := range []string{"===", "=="} {
		if !strings.HasPrefix(spec, op) {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(spec, op))
		if v == "" || strings.ContainsRune(v, '*') {
			return "", false
		}
		return v, true
	}
	return "", false
}

func isRequirementNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '-' || b == '_' || b == '.'
}

// normalizeRequirementName applies PEP 503 name normalisation (lowercase, runs
// of -/_/. collapsed to a single -) so "Ruff" and "ruff" compare equal.
func normalizeRequirementName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' || r == '.' {
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		prevDash = false
		b.WriteRune(r)
	}
	return b.String()
}
