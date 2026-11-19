package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// scopeToml has one repo and a pre-update hook, so a test can prove a usage
// error stops before any hook runs.
const scopeToml = `[workspace]
name = "test"
terminal = "myterm"

[repos.myterm]
url = "github:owner/myterm"

[[hooks]]
when = ["pre-update"]
run = ["pre-update-hook"]
`

// scopeWorkspace stubs openWorkspace with a FakeRunner workspace (the update
// command calls the concrete Workspace.Update, so there is no options seam) and
// answers the in-place flow's clean-tree probes plus the pre-update hook.
func scopeWorkspace(t *testing.T) *exec.FakeRunner {
	t.Helper()
	fr := withFakeWorkspace(t, scopeToml)
	w, err := openWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	trustCLIWorkspace(t, w)
	repo := w.Root() + "/myterm"
	fr.AddResponse("sh", []string{"-c", "pre-update-hook"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "diff", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	return fr
}

func calledUpdateLocks(fr *exec.FakeRunner) bool {
	for _, c := range fr.Calls() {
		if c.Name == "./update-locks.sh" {
			return true
		}
	}
	return false
}

func TestWorkspaceUpdate_ScopeFlagReachesUpdate(t *testing.T) {
	fr := scopeWorkspace(t)
	stdout, _, err := runCobraCmd(t, []string{"update", "--in-place", "--scope", "flake"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if calledUpdateLocks(fr) {
		t.Errorf("--scope flake must not run update-locks.sh; calls=%v", fr.Calls())
	}
	if !strings.Contains(stdout, "scope flake — skipping update-locks.sh") {
		t.Errorf("missing the flake banner; got:\n%s", stdout)
	}
}

func TestWorkspaceUpdate_SiblingsOnlyIsAnAliasForScopeSiblings(t *testing.T) {
	for _, args := range [][]string{
		{"update", "--in-place", "--siblings-only"},
		{"update", "--in-place", "--siblings-only", "--scope", "siblings"},
		{"update", "--in-place", "--scope", "siblings"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fr := scopeWorkspace(t)
			stdout, _, err := runCobraCmd(t, args)
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if calledUpdateLocks(fr) {
				t.Errorf("the siblings scope must not run update-locks.sh")
			}
			if !strings.Contains(stdout, "scope siblings — skipping update-locks.sh") {
				t.Errorf("missing the siblings banner; got:\n%s", stdout)
			}
		})
	}
}

func TestWorkspaceUpdate_UsageErrorsExit2AndRunNoHook(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{
			"alias conflicts with full",
			[]string{"update", "--siblings-only", "--scope", "full"},
			"--siblings-only is an alias for --scope siblings and conflicts with --scope full; pass one",
		},
		{
			"alias conflicts with flake",
			[]string{"update", "--siblings-only", "--scope", "flake"},
			"--siblings-only is an alias for --scope siblings and conflicts with --scope flake; pass one",
		},
		{"unknown scope", []string{"update", "--scope", "bogus"}, "want siblings, flake or full"},
		{"trailing --scope without a value", []string{"update", "--scope"}, "flag needs an argument"},
		{"explicit empty value", []string{"update", "--scope="}, "want siblings, flake or full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := scopeWorkspace(t)
			_, _, err := runCobraCmd(t, c.args)
			var ec ExitCodeError
			if !errors.As(err, &ec) || ec.Code != 2 {
				t.Fatalf("error = %#v, want ExitCodeError with code 2", err)
			}
			if !strings.Contains(ec.Msg, c.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", ec.Msg, c.wantMsg)
			}
			if n := len(fr.Calls()); n != 0 {
				t.Errorf("a usage error must run nothing (no pre-update hook); calls=%v", fr.Calls())
			}
		})
	}
}

func TestWorkspaceUpdateScopeHelpText(t *testing.T) {
	cmd := workspaceUpdateCmd(new(string))
	for _, want := range []string{
		"--scope chooses how much is relocked:",
		"siblings  workspace-sibling flake inputs only; skips update-locks.sh",
		"full      ./update-locks.sh, then every flake input (default)",
		"The CLI defaults to full; /pn-workspace-update and 'pnwf update-relock' default to flake.",
		"Afterwards, 'pn workspace explain' reports where the time went.",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long is missing %q", want)
		}
	}
	if got := cmd.Flags().Lookup("scope").Usage; got != "siblings, flake or full (see above)" {
		t.Errorf("--scope usage = %q", got)
	}
	if got := cmd.Flags().Lookup("siblings-only").Usage; got != "alias for --scope siblings" {
		t.Errorf("--siblings-only usage = %q", got)
	}
	if got := workspaceUpgradeCmd(new(string)).Short; got != "Update (--scope full) + apply each workspace repo" {
		t.Errorf("upgrade Short = %q", got)
	}
}
