package cli

import (
	"errors"
	"os"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// A name that is no workspace repo exits 2 (ExitCodeError), which pre-land-eval
// reads as "not a member" and answers with its locked-input fallback.
func TestWorkspaceOverrides_UnknownRepoExitsTwo(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	_, _, err := runCobraCmd(t, []string{"overrides", "no-such-repo"})
	if err == nil {
		t.Fatal("expected an error for a repo that is not in the workspace")
	}
	if got := ExitCode(err); got != 2 {
		t.Errorf("exit code = %d, want 2 (err: %v)", got, err)
	}
	var ec ExitCodeError
	if !errors.As(err, &ec) || ec.Msg == "" {
		t.Errorf("want an ExitCodeError carrying a message, got %#v", err)
	}
}

// A member repo with no workspace siblings prints nothing and exits 0.
func TestWorkspaceOverrides_MemberWithoutSiblingsPrintsNothing(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	out, _, err := runCobraCmd(t, []string{"overrides", "myterm"})
	if err != nil {
		t.Fatalf("overrides myterm: %v", err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}

func TestWorkspaceOverrides_RequiresExactlyOneArgument(t *testing.T) {
	withFakeWorkspace(t, terminalToml)
	for _, args := range [][]string{{"overrides"}, {"overrides", "a", "b"}} {
		if _, _, err := runCobraCmd(t, args); err == nil {
			t.Errorf("%v: expected a usage error", args)
		}
	}
}

func TestWorkspaceOverrides_OpenFailurePropagates(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })
	if _, _, err := runCobraCmd(t, []string{"overrides", "x"}); err == nil {
		t.Error("expected the workspace open failure to propagate")
	}
}
