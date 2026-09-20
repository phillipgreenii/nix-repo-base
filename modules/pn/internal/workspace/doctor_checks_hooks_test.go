// internal/workspace/doctor_checks_hooks_test.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
)

// TestCheckHookExpressions_WarnsBogusNixRunAndNeverFire covers the two advisory
// doctor checks for per-repo event hooks (bd pg2-uswb, subsumes pg2-id4a):
//   - hook-nix-run-output: a {nix_run <attr>} whose attr is not a flake output.
//   - hook-never-fires: a per-repo hook whose events never process the repo.
func TestCheckHookExpressions_WarnsBogusNixRunAndNeverFire(t *testing.T) {
	root := t.TempDir()
	f := exec.NewFakeRunner()
	// goodattr resolves; bogusattr's eval is unregistered → error → treated absent.
	f.AddResponse("nix", []string{"eval", filepath.Join(root, "dep") + "#goodattr", "--apply", "_: true"}, exec.Result{}, nil)
	ws := &Workspace{
		root: root, runner: f,
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{
				"term": {URL: "u", Branch: "main"},
				"leaf": {URL: "u", Branch: "main", Hooks: []EventHook{{When: []string{"post-build"}, Run: []string{"echo hi"}}}},
				"dep":  {URL: "u", Branch: "main", Hooks: []EventHook{{When: []string{"post-rebase"}, Run: []string{"{nix_run goodattr}", "{nix_run bogusattr}"}}}},
			},
		},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term", lock: emptyLock()}
	fs := ws.checkHookExpressions(context.Background(), env)

	if !hasFindingForRepo(fs, "hook-never-fires", "leaf", SevWarning) {
		t.Error("expected hook-never-fires for leaf (post-build hook on a non-terminal repo)")
	}
	if !hasFindingForRepo(fs, "hook-nix-run-output", "dep", SevWarning) {
		t.Error("expected hook-nix-run-output for dep's {nix_run bogusattr}")
	}
	for _, ff := range fs {
		if ff.CheckID == "hook-nix-run-output" && strings.Contains(ff.Message, "goodattr") {
			t.Errorf("goodattr resolves; should not warn: %q", ff.Message)
		}
		if ff.CheckID == "hook-never-fires" && ff.Repo == "dep" {
			t.Error("dep's post-rebase hook is repo-iterating (fires); should not be never-fires")
		}
		if ff.Severity != SevWarning {
			t.Errorf("hook findings must be advisory (SevWarning); got %v for %s", ff.Severity, ff.CheckID)
		}
	}
}

// TestCheckHookExpressions_NeverFireGuardTerminalUnset verifies that when
// workspace.terminal is unset, a build/apply-only hook is NOT flagged never-fire
// (we cannot know which repo is the terminal) (bd pg2-id4a guard).
func TestCheckHookExpressions_NeverFireGuardTerminalUnset(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{
			Repos: map[string]RepoConfig{
				"leaf": {URL: "u", Branch: "main", Hooks: []EventHook{{When: []string{"post-build"}, Run: []string{"echo hi"}}}},
			},
		},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "", lock: emptyLock()}
	for _, ff := range ws.checkHookExpressions(context.Background(), env) {
		if ff.CheckID == "hook-never-fires" {
			t.Errorf("must not flag never-fires when terminal is unset: %q", ff.Message)
		}
	}
}

// TestCheckHookExpressions_SkipsNixRunProbeOffline verifies the nix-eval output
// probe is skipped under --offline (never-fire, pure config, still runs).
func TestCheckHookExpressions_SkipsNixRunProbeOffline(t *testing.T) {
	root := t.TempDir()
	f := exec.NewFakeRunner() // no nix responses scripted
	ws := &Workspace{
		root: root, runner: f,
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{
				"term": {URL: "u", Branch: "main"},
				"dep":  {URL: "u", Branch: "main", Hooks: []EventHook{{When: []string{"post-rebase"}, Run: []string{"{nix_run bogusattr}"}}}},
			},
		},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term", offline: true, lock: emptyLock()}
	for _, ff := range ws.checkHookExpressions(context.Background(), env) {
		if ff.CheckID == "hook-nix-run-output" {
			t.Errorf("nix_run output probe must be skipped when offline; got %q", ff.Message)
		}
	}
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			t.Errorf("no nix eval should run when offline; got %v", c.Args)
		}
	}
}

// tomlV1/tomlV2 are two distinct pn-workspace.toml bodies used to simulate an
// edit changing the trusted hash (bd tc-atsmj GAP 1).
const (
	tomlV1 = "[workspace]\nterminal = \"leaf\"\n\n[repos.leaf]\nurl = \"github:owner/leaf\"\nbranch = \"main\"\n"
	tomlV2 = "[workspace]\nterminal = \"leaf\"\n\n[repos.leaf]\nurl = \"github:owner/leaf\"\nbranch = \"main\"\n# edited\n"
)

// TestCheckHooksTrusted_NeverAllowedWarns reproduces the observed case (bd
// tc-hm3uh / tc-atsmj): a workspace whose pn-workspace.toml has never been
// through `pn workspace allow` gets a hooks-trusted finding naming the fix.
func TestCheckHooksTrusted_NeverAllowedWarns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty trust store => never allowed
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), tomlV1)
	ws := &Workspace{root: root, runner: exec.NewFakeRunner()}

	fs := ws.checkHooksTrusted(context.Background(), &doctorEnv{})

	if !hasFinding(fs, "hooks-trusted", SevWarning) {
		t.Fatalf("expected a hooks-trusted warning for a never-allowed workspace; got %+v", fs)
	}
	for _, f := range fs {
		if f.CheckID == "hooks-trusted" && !strings.Contains(f.Manual, "pn workspace allow") {
			t.Errorf("hooks-trusted finding must hint at `pn workspace allow`; got Manual=%q", f.Manual)
		}
	}
}

// TestCheckHooksTrusted_ChangedSinceAllowedWarns: trust an initial version of
// pn-workspace.toml, then edit it (bd tc-atsmj GAP 1's "changed since last
// allow" case) — the finding must reappear even though the workspace WAS
// trusted at some point.
func TestCheckHooksTrusted_ChangedSinceAllowedWarns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), tomlV1)
	if err := trust.Allow(root); err != nil {
		t.Fatalf("trust.Allow: %v", err)
	}
	// Edit pn-workspace.toml after allow: re-blocks per trust.go's doc comment.
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), tomlV2)
	ws := &Workspace{root: root, runner: exec.NewFakeRunner()}

	fs := ws.checkHooksTrusted(context.Background(), &doctorEnv{})

	if !hasFinding(fs, "hooks-trusted", SevWarning) {
		t.Fatalf("expected a hooks-trusted warning after editing an allowed pn-workspace.toml; got %+v", fs)
	}
}

// TestCheckHooksTrusted_AllowedNoFinding: run `pn workspace allow` (simulated
// via trust.Allow) against the CURRENT content, and the finding must clear —
// the second half of the reproduction the bead's acceptance criteria describe.
func TestCheckHooksTrusted_AllowedNoFinding(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), tomlV1)
	ws := &Workspace{root: root, runner: exec.NewFakeRunner()}

	// Before allow: finding present.
	if !hasFinding(ws.checkHooksTrusted(context.Background(), &doctorEnv{}), "hooks-trusted", SevWarning) {
		t.Fatal("expected a hooks-trusted warning before allow")
	}

	// pn workspace allow
	if err := trust.Allow(root); err != nil {
		t.Fatalf("trust.Allow: %v", err)
	}

	fs := ws.checkHooksTrusted(context.Background(), &doctorEnv{})
	for _, f := range fs {
		if f.CheckID == "hooks-trusted" {
			t.Errorf("expected no hooks-trusted finding after allow; got %+v", f)
		}
	}
}
