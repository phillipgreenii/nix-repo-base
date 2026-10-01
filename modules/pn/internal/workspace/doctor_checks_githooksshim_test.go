// internal/workspace/doctor_checks_githooksshim_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const shimTestConfig = "# DO NOT MODIFY\n{\"repos\":[{\"hooks\":[{\"stages\":[\"pre-commit\"]},{\"stages\":[\"pre-push\"]}]}]}\n"

func writeShim(t *testing.T, dir, stage string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, ".githooks", stage)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func shimDoctorEnv(ws *Workspace) *doctorEnv { return &doctorEnv{ws: ws, mode: "primary"} }

func TestCheckGitHooksShimWired_AllStagesPresentIsClean(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), shimTestConfig)
	writeShim(t, dir, "pre-commit", 0o755)
	writeShim(t, dir, "pre-push", 0o755)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")

	if fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws)); len(fs) != 0 {
		t.Fatalf("want no findings, got %+v", fs)
	}
}

func TestCheckGitHooksShimWired_MissingStageIsError(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), shimTestConfig)
	writeShim(t, dir, "pre-commit", 0o755) // pre-push shim absent
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")

	fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws))
	f := findingByID(t, fs, "git-hooks-shim-wired")
	if f.Skipped || f.Severity != SevError {
		t.Errorf("must be a hard error, got %+v", f)
	}
	if !strings.Contains(f.Message, "pre-push (missing)") || strings.Contains(f.Message, "pre-commit (") {
		t.Errorf("message should name only pre-push: %q", f.Message)
	}
}

func TestCheckGitHooksShimWired_TreeWithoutGithooksDirIsError(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), shimTestConfig)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks") // no .githooks/ at all

	fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws))
	f := findingByID(t, fs, "git-hooks-shim-wired")
	if !strings.Contains(f.Message, "pre-commit (missing)") {
		t.Errorf("got %q", f.Message)
	}
}

func TestCheckGitHooksShimWired_NonExecutableStageIsError(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), shimTestConfig)
	writeShim(t, dir, "pre-commit", 0o644)
	writeShim(t, dir, "pre-push", 0o755)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")

	fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws))
	f := findingByID(t, fs, "git-hooks-shim-wired")
	if !strings.Contains(f.Message, "pre-commit (not executable)") {
		t.Errorf("got %q", f.Message)
	}
}

// Option OFF: no local core.hooksPath, or an absolute (legacy) one, is out of scope.
func TestCheckGitHooksShimWired_LegacyReposUnaffected(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), shimTestConfig)
	if fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws)); len(fs) != 0 {
		t.Fatalf("unset hooksPath: want none, got %+v", fs)
	}
	runGitT(t, dir, "config", "--local", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	if fs := ws.checkGitHooksShimWired(context.Background(), shimDoctorEnv(ws)); len(fs) != 0 {
		t.Fatalf("absolute hooksPath: want none, got %+v", fs)
	}
}

// pre-commit-hook-live must audit the dir git really uses, not .git/hooks.
func TestCheckPreCommitHookLive_FollowsCoreHooksPath(t *testing.T) {
	ws, dir := preCommitHookWorkspace(t)
	writeFile(t, filepath.Join(dir, preCommitConfigName), "generated config")
	writeShim(t, dir, "pre-commit", 0o755)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	// Legacy .git/hooks/pre-commit is a dangling store symlink: bypassed, so NOT a finding.
	if err := os.Symlink("/nix/store/deadbeefdeadbeefdeadbeefdeadbeef-gone", hookPathFor(dir)); err != nil {
		t.Fatal(err)
	}
	if fs := ws.checkPreCommitHookLive(context.Background(), shimDoctorEnv(ws)); len(fs) != 0 {
		t.Fatalf("bypassed legacy hook must not be flagged; got %+v", fs)
	}
	// And a missing shim in the resolved dir IS flagged.
	if err := os.Remove(filepath.Join(dir, ".githooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	fs := ws.checkPreCommitHookLive(context.Background(), shimDoctorEnv(ws))
	if !hasSkippedFinding(t, fs, "pre-commit-hook-live", "apps") {
		t.Fatalf("missing resolved hook must be flagged; got %+v", fs)
	}
}
