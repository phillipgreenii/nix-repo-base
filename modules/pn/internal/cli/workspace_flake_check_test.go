package cli

import (
	"strings"
	"testing"
)

// A repo that is no workspace member is a bad invocation: exit 2, not the exit 10
// that means a check ran and failed (1 is the generic error). pre-land-eval relies on the difference.
func TestWorkspaceFlakeCheckNoBuild_UnknownRepoExitsTwo(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	_, _, err := runCobraCmd(t, []string{"flake-check", "--no-build", "no-such-repo"})
	if err == nil {
		t.Fatal("expected an error for a repo that is not in the workspace")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("exit code = %d, want 2 (err: %v)", got, err)
	}
}

func TestWorkspaceFlakeCheckNoBuild_OptionsWithoutNoBuildExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"flake-check", "myterm"},
		{"flake-check", "--rev", "HEAD"},
		{"flake-check", "--caller", "ff-1c"},
		{"flake-check", "--no-ifd"},
	} {
		withFakeWorkspace(t, terminalToml)
		_, _, err := runCobraCmd(t, args)
		if got := ExitCode(err); got != 2 {
			t.Errorf("%v: exit code = %d, want 2 (err: %v)", args, got, err)
		}
	}
}

func TestWorkspaceFlakeCheckNoBuild_UnknownCallerExitsTwo(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	_, _, err := runCobraCmd(t, []string{"flake-check", "--no-build", "--caller", "cron", "myterm"})
	if got := ExitCode(err); got != 2 {
		t.Errorf("exit code = %d, want 2 (err: %v)", got, err)
	}
}

// The help text is the contract the land step reads: what the flag is, why it is
// not nix's own, how to select one repo and where the log goes.
func TestWorkspaceFlakeCheck_HelpDocumentsNoBuild(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	stdout, _, err := runCobraCmd(t, []string{"flake-check", "--help"})
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{
		"--no-build",
		"drvPath",
		"NOT nix's own",
		"read-only",
		"--no-write-lock-file",
		"<repo-or-path>",
		"--rev",
		"flake-check-no-build.jsonl",
		"no retry and no inconclusive class",
		"10 means a check ran and reported failures",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("flake-check --help should mention %q:\n%s", want, stdout)
		}
	}
}
