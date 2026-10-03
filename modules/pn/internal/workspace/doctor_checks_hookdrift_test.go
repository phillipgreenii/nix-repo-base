// internal/workspace/doctor_checks_hookdrift_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hdRun(ws *Workspace) []Finding {
	return ws.checkHookDrift(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
}

// hdWant asserts exactly one finding, of the given severity, whose message
// contains msg and whose fix line contains manual.
func hdWant(t *testing.T, fs []Finding, sev Severity, msg, manual string) {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("want exactly one finding mentioning %q, got %+v", msg, fs)
	}
	f := fs[0]
	if f.CheckID != hookDriftCheckID || f.Repo != "repo" || f.Severity != sev || f.Skipped {
		t.Errorf("want a non-skipped %s finding for repo with severity %v, got %+v", hookDriftCheckID, sev, f)
	}
	if !strings.HasPrefix(f.Message, `hook drift: repo "repo": `) || !strings.Contains(f.Message, msg) {
		t.Errorf("message %q must start with the drift prefix and mention %q", f.Message, msg)
	}
	if !strings.Contains(f.Manual, manual) {
		t.Errorf("fix line %q must contain %q", f.Manual, manual)
	}
}

func TestCheckHookDrift_CleanRepoIsClean(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	hbGoodInstall(t, dir)
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("a healthy repo must produce no drift finding; got %+v", fs)
	}
}

func TestCheckHookDrift_UndeclaredRepoIsStillAudited(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, false)
	mustMkdir(t, filepath.Join(dir, ".githooks"))
	hdWant(t, hdRun(ws), SevError, filepath.Join(dir, ".githooks")+" exists", "rm -r "+filepath.Join(dir, ".githooks"))
}

func TestCheckHookDrift_GithooksDirIsError(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	mustMkdir(t, filepath.Join(dir, ".githooks"))
	writeFile(t, filepath.Join(dir, ".githooks", "pre-commit"), "#!/bin/sh\n")
	hdWant(t, hdRun(ws), SevError, "commit-time shim's hook directory was removed", "review it, then remove it")
}

func TestCheckHookDrift_GithooksFileIsNotADirectory(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	writeFile(t, filepath.Join(dir, ".githooks"), "just a file\n")
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("only a .githooks DIRECTORY is the removed shim; got %+v", fs)
	}
}

func TestCheckHookDrift_RelativeLocalHooksPathIsError(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	hbGoodInstall(t, dir)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	hdWant(t, hdRun(ws), SevError,
		"core.hooksPath=.githooks (local scope, "+filepath.Join(dir, ".git", "config")+") is a relative path",
		"git -C "+dir+" config --local --unset core.hooksPath")
}

func TestCheckHookDrift_AbsoluteHooksPathIsNotDrift(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	hbGoodInstall(t, dir)
	// The bundle-state check owns an absolute path that bypasses the hooks
	// directory; the drift row is about RELATIVE values only.
	runGitT(t, dir, "config", "--local", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("an absolute core.hooksPath is not drift; got %+v", fs)
	}
	runGitT(t, dir, "config", "--local", "core.hooksPath", "/nonexistent/hooks")
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("an absolute core.hooksPath is not drift; got %+v", fs)
	}
}

func TestCheckHookDrift_RelativeGlobalHooksPathIsError(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "[core]\n\thooksPath = relhooks\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	hdWant(t, hdRun(ws), SevError, "core.hooksPath=relhooks (global scope, "+global+")", "remove core.hooksPath=relhooks from "+global)
	_ = dir
}

func TestCheckHookDrift_ShadowedRelativeValueStillCounts(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "[core]\n\thooksPath = relhooks\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	// A later absolute local value wins in git, but the relative global one is
	// still drift.
	runGitT(t, dir, "config", "--local", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	hdWant(t, hdRun(ws), SevError, "core.hooksPath=relhooks (global scope", "remove core.hooksPath=relhooks")
}

func TestCheckHookDrift_HomeRelativeHooksPathIsNotDrift(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	runGitT(t, dir, "config", "--local", "core.hooksPath", "~/hooks")
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("a ~/ core.hooksPath is expanded by git and is not relative; got %+v", fs)
	}
}

func TestCheckHookDrift_HandMadeConfigIsWarning(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	hbGoodInstall(t, dir)
	p := filepath.Join(dir, ".pre-commit-config.yaml")
	writeFile(t, p, "repos: []\n")
	hdWant(t, hdRun(ws), SevWarning, p+" is a regular file", "rm -r "+p)
}

func TestCheckHookDrift_HandLinkedConfigIsWarning(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	target := filepath.Join(t.TempDir(), "canonical-config.yaml")
	writeFile(t, target, "repos: []\n")
	p := filepath.Join(dir, ".pre-commit-config.yaml")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	hdWant(t, hdRun(ws), SevWarning, "is a symlink to "+target, "rm -r "+p)
}

func TestCheckHookDrift_OldGeneratedConfigLinkIsNotDrift(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	// The ADR 0016 symlink into /nix/store (live or dangling) is kept until the
	// follow-up retires it (ADR 0032, R2).
	p := filepath.Join(dir, ".pre-commit-config.yaml")
	if err := os.Symlink("/nix/store/deadbeefdeadbeefdeadbeefdeadbeef-gone/config.yaml", p); err != nil {
		t.Fatal(err)
	}
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("the old generated symlink must not be reported; got %+v", fs)
	}
}

func TestCheckHookDrift_LinkedWorktreesAreAudited(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGitT(t, dir, "worktree", "add", "-q", "-b", "feature", wt)
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("a clean linked worktree must be clean; got %+v", fs)
	}
	mustMkdir(t, filepath.Join(wt, ".githooks"))
	writeFile(t, filepath.Join(wt, ".pre-commit-config.yaml"), "repos: []\n")
	fs := hdRun(ws)
	if len(fs) != 2 {
		t.Fatalf("want a .githooks error and a hand-made config warning in the worktree, got %+v", fs)
	}
	var gotDir, gotCfg bool
	for _, f := range fs {
		gotDir = gotDir || (f.Severity == SevError && strings.Contains(f.Message, filepath.Join(wt, ".githooks")))
		gotCfg = gotCfg || (f.Severity == SevWarning && strings.Contains(f.Message, filepath.Join(wt, ".pre-commit-config.yaml")))
	}
	if !gotDir || !gotCfg {
		t.Errorf("findings must name the worktree paths: %+v", fs)
	}
}

func TestCheckHookDrift_NonGitRepoDirIsSkipped(t *testing.T) {
	ws, dir := hbDoctorWorkspace(t, true)
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(dir, ".githooks"))
	if fs := hdRun(ws); len(fs) != 0 {
		t.Fatalf("a directory that is not a git work tree is not audited; got %+v", fs)
	}
}

func TestRegisterChecks_IncludesHookDrift(t *testing.T) {
	ws := &Workspace{}
	for _, c := range ws.registerChecks() {
		if c.id == hookDriftCheckID {
			return
		}
	}
	t.Fatalf("registerChecks must include %q", hookDriftCheckID)
}
