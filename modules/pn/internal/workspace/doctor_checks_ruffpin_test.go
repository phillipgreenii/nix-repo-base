// internal/workspace/doctor_checks_ruffpin_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ruffHookConfig is a generated-config excerpt shaped like the real
// git-hooks.nix output: the nixpkgs ruff version lives only inside the hook
// `entry` store paths.
func ruffHookConfig(version string) string {
	return `{
  "repos": [
    {
      "hooks": [
        {
          "entry": "/nix/store/1insayrbsdp71hzan5cqqjck6a8mh97m-ruff-` + version + `/bin/ruff check --fix",
          "id": "ruff",
          "name": "ruff"
        },
        {
          "entry": "/nix/store/1insayrbsdp71hzan5cqqjck6a8mh97m-ruff-` + version + `/bin/ruff format",
          "id": "ruff-format",
          "name": "ruff format"
        }
      ]
    }
  ]
}
`
}

// pyprojectWithRuff renders a pyproject.toml declaring ruff in the
// `[dependency-groups] dev` list, the layout both uv packages in
// phillipgreenii-nix-support-apps use.
func pyprojectWithRuff(requirement string) string {
	return `[project]
name = "demo"
version = "0.1.0"
dependencies = ["requests>=2.32.4"]

[dependency-groups]
dev = [
    "` + requirement + `",
    "mypy>=1.14.1",
]

[tool.ruff]
line-length = 100
`
}

// ruffPinWorkspace builds a one-repo workspace whose repo is a real git repo, so
// the check's isGitRepo gate passes.
func ruffPinWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apps")
	initRealRepo(t, dir)
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"apps": {URL: "git@github.com:o/apps.git", Branch: "main"},
		}},
	}
	return ws, dir
}

func writeRuffPackage(t *testing.T, repoDir, pkg, requirement string) {
	t.Helper()
	pkgDir := filepath.Join(repoDir, "packages", pkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "pyproject.toml"), pyprojectWithRuff(requirement))
}

// TestCheckRuffPin_MatchingExactPinIsClean is the post-fix steady state
// (bd pg2-671gg): both packages exact-pinned to the nixpkgs ruff the hooks run.
func TestCheckRuffPin_MatchingExactPinIsClean(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), ruffHookConfig("0.15.14"))
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff==0.15.14")
	writeRuffPackage(t, dir, "work-activity-tracker", "ruff==0.15.14")

	if fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("matching exact pins must produce no findings; got %+v", fs)
	}
}

// TestCheckRuffPin_StalePinIsError is the regression the check exists for: the
// next nixpkgs ruff bump moves the hook and leaves the pin behind.
func TestCheckRuffPin_StalePinIsError(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), ruffHookConfig("0.16.4"))
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff==0.15.14")

	fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasFindingForRepo(fs, "ruff-pin-drift", "apps", SevError) {
		t.Fatalf("stale pin must be a ruff-pin-drift error; got %+v", fs)
	}
	f := findingByID(t, fs, "ruff-pin-drift")
	for _, want := range []string{"0.15.14", "0.16.4", "packages/pd-schedule-manager/pyproject.toml"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message must name %q: %q", want, f.Message)
		}
	}
	if f.Fixable || f.fix != nil {
		t.Error("ruff-pin findings must NOT be auto-fixable (the remedy needs a relock and a reformat)")
	}
	if !strings.Contains(f.Manual, "uv lock") {
		t.Errorf("manual hint must include the relock step; got %q", f.Manual)
	}
}

// TestCheckRuffPin_FloatingSpecIsWarning covers the pre-fix state and any
// regression back to it: an unbounded/`>=` spec a relock can float again.
func TestCheckRuffPin_FloatingSpecIsWarning(t *testing.T) {
	for _, requirement := range []string{"ruff>=0.8.0", "ruff", "ruff~=0.15.0", "ruff==0.15.*"} {
		t.Run(requirement, func(t *testing.T) {
			ws, dir := ruffPinWorkspace(t)
			writeFile(t, filepath.Join(dir, preCommitConfigName), ruffHookConfig("0.15.14"))
			writeRuffPackage(t, dir, "pd-schedule-manager", requirement)

			fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
			if !hasFindingForRepo(fs, "ruff-pin-floating", "apps", SevWarning) {
				t.Fatalf("%q is not exact-pinned; expected a ruff-pin-floating warning, got %+v", requirement, fs)
			}
			if f := findingByID(t, fs, "ruff-pin-floating"); f.Fixable {
				t.Error("ruff-pin-floating must not be auto-fixable")
			}
		})
	}
}

// TestCheckRuffPin_AbsentConfigSkips is the degradation requirement: the
// generated config is gitignored (ADR 0016) and therefore missing in every fresh
// clone and worktree. That must SKIP, never fail.
func TestCheckRuffPin_AbsentConfigSkips(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff==0.15.14")

	fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 1 {
		t.Fatalf("absent config must yield exactly one skipped finding; got %+v", fs)
	}
	f := fs[0]
	if f.CheckID != "ruff-pin" || !f.Skipped {
		t.Fatalf("absent config must be a skipped ruff-pin finding; got %+v", f)
	}
	if !strings.Contains(f.Message, "absent") {
		t.Errorf("skip must state the reason; got %q", f.Message)
	}
	// A skipped finding must not colour the exit code.
	report := &DoctorReport{Findings: fs}
	if report.HasErrors() {
		t.Error("a skipped ruff-pin finding must not make the report erroneous")
	}
	if report.ExitCode(true) != 0 {
		t.Error("a skipped ruff-pin finding must not fail even --strict")
	}
	if got := collectSkipped(fs); len(got) != 1 || got[0] != "ruff-pin" {
		t.Errorf("skip must be reported in report.Skipped; got %v", got)
	}
}

// TestCheckRuffPin_NoRuffDependencyIsSilent — a repo that declares no ruff
// dependency is outside the invariant, config present or not.
func TestCheckRuffPin_NoRuffDependencyIsSilent(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), ruffHookConfig("0.15.14"))
	pkgDir := filepath.Join(dir, "packages", "plain")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "pyproject.toml"), "[project]\nname = \"plain\"\nversion = \"0.1.0\"\n")

	if fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("a repo with no ruff dependency must produce no findings; got %+v", fs)
	}
}

// TestCheckRuffPin_NoRuffHookIsSilent — a config that runs no nixpkgs ruff hook
// leaves only ONE toolchain, so there is nothing to unify (this is what keeps the
// check silent on phillipg-nix-repo-base, whose generated config has no ruff
// hook).
func TestCheckRuffPin_NoRuffHookIsSilent(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName),
		`{"repos":[{"hooks":[{"entry":"/nix/store/aaaa-treefmt-1.2.3/bin/treefmt","id":"treefmt"}]}]}`)
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff>=0.8.0")

	if fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("no nixpkgs ruff hook means nothing to unify; got %+v", fs)
	}
}

// TestCheckRuffPin_AmbiguousHookVersionsWarn — two different ruff versions in one
// config leaves no single pin target, so warn rather than pick one.
func TestCheckRuffPin_AmbiguousHookVersionsWarn(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName),
		ruffHookConfig("0.15.14")+ruffHookConfig("0.16.4"))
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff==0.15.14")

	fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasFindingForRepo(fs, "ruff-pin", "apps", SevWarning) {
		t.Fatalf("two hook ruff versions must warn; got %+v", fs)
	}
	if f := findingByID(t, fs, "ruff-pin"); !strings.Contains(f.Message, "0.15.14") || !strings.Contains(f.Message, "0.16.4") {
		t.Errorf("warning must name both versions; got %q", f.Message)
	}
}

// TestCheckRuffPin_IgnoresPrunedTreesAndComments guards the two ways a naive
// implementation reports a phantom pin: a stale pin inside a virtualenv or
// vendored tree, and a commented-out pin (invisible only because the TOML is
// decoded, not regexed). Both live beside a CORRECT real pin, so a clean result
// proves the real pin was still read.
func TestCheckRuffPin_IgnoresPrunedTreesAndComments(t *testing.T) {
	ws, dir := ruffPinWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), ruffHookConfig("0.15.14"))
	writeRuffPackage(t, dir, "pd-schedule-manager", "ruff==0.15.14")

	for _, pruned := range []string{".venv", "node_modules", ".git"} {
		sub := filepath.Join(dir, "packages", "pd-schedule-manager", pruned, "vendored")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(sub, "pyproject.toml"), pyprojectWithRuff("ruff==0.9.9"))
	}
	writeFile(t, filepath.Join(dir, "pyproject.toml"), `[project]
name = "root"
version = "0.1.0"

[dependency-groups]
dev = [
    # "ruff==0.9.9",  <- commented out; MUST NOT be read as a pin
    "mypy>=1.14.1",
]
`)

	if fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("pruned trees and comments must not yield pins; got %+v", fs)
	}
}

// TestCheckRuffPin_SkipsAbsentRepoDir — a configured repo not cloned on disk is
// checkRepos' business, not this check's.
func TestCheckRuffPin_SkipsAbsentRepoDir(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"missing": {URL: "u", Branch: "main"},
		}},
	}
	if fs := ws.checkRuffPin(context.Background(), &doctorEnv{ws: ws, mode: "primary"}); len(fs) != 0 {
		t.Fatalf("an uncloned repo must be skipped silently; got %+v", fs)
	}
}

// TestParseRuffRequirement covers the requirement-string parser directly: the
// name must normalise to ruff (PEP 503), extras and markers are stripped, and a
// URL reference pins nothing comparable.
func TestParseRuffRequirement(t *testing.T) {
	cases := []struct {
		req      string
		wantSpec string
		wantOK   bool
	}{
		{"ruff==0.15.14", "==0.15.14", true},
		{"Ruff == 0.15.14", "== 0.15.14", true},
		{"ruff>=0.8.0", ">=0.8.0", true},
		{"ruff", "", true},
		{"ruff>=0.8.0; python_version >= '3.10'", ">=0.8.0", true},
		{"ruff[extra]==0.15.14", "==0.15.14", true},
		{"ruff @ https://example.invalid/ruff.whl", "", false},
		{"ruff-lsp==0.0.62", "", false},
		{"mypy>=1.14.1", "", false},
		{"ruff format --check", "", false},
	}
	for _, c := range cases {
		spec, ok := parseRuffRequirement(c.req)
		if ok != c.wantOK || strings.TrimSpace(spec) != strings.TrimSpace(c.wantSpec) {
			t.Errorf("parseRuffRequirement(%q) = (%q, %v); want (%q, %v)", c.req, spec, ok, c.wantSpec, c.wantOK)
		}
	}
}

// TestExactRuffVersion covers which specifiers actually FREEZE the version — the
// distinction the whole check rests on.
func TestExactRuffVersion(t *testing.T) {
	cases := []struct {
		spec    string
		want    string
		isExact bool
	}{
		{"==0.15.14", "0.15.14", true},
		{"=== 0.15.14", "0.15.14", true},
		{"", "", false},
		{">=0.8.0", "", false},
		{"~=0.15.0", "", false},
		{"==0.15.*", "", false},
		{">=0.15.14,<0.16", "", false},
		{"==", "", false},
	}
	for _, c := range cases {
		got, exact := exactRuffVersion(c.spec)
		if exact != c.isExact || got != c.want {
			t.Errorf("exactRuffVersion(%q) = (%q, %v); want (%q, %v)", c.spec, got, exact, c.want, c.isExact)
		}
	}
}

func findingByID(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.CheckID == id {
			return f
		}
	}
	t.Fatalf("no %q finding in %+v", id, fs)
	return Finding{}
}
