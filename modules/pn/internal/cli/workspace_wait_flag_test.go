package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// --wait on build and apply is an optional-value flag (bead pg2-sdbhh4.11):
// --wait=<duration> waits that long, a bare --wait waits indefinitely, and the
// value needs '=' because pflag never takes a space-separated value for it.

func parseWait(t *testing.T, args ...string) (time.Duration, error) {
	t.Helper()
	var wait time.Duration
	cmd := &cobra.Command{Use: "x"}
	addWaitFlag(cmd, &wait)
	err := cmd.ParseFlags(args)
	return wait, err
}

func TestWaitFlag_Values(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{nil, 0, false},
		{[]string{"--wait=5m"}, 5 * time.Minute, false},
		{[]string{"--wait=90s"}, 90 * time.Second, false},
		{[]string{"--wait=0s"}, 0, false},
		{[]string{"--wait"}, workspace.WaitForever, false},
		{[]string{"--wait=-5m"}, 0, true},
		{[]string{"--wait=soon"}, 0, true},
		{[]string{"--wait="}, 0, true},
	} {
		got, err := parseWait(t, tc.args...)
		if (err != nil) != tc.wantErr || (err == nil && got != tc.want) {
			t.Errorf("args %v: got %v, err=%v; want %v, wantErr=%v", tc.args, got, err, tc.want, tc.wantErr)
		}
	}
}

// A space-separated value is NOT the value of --wait: it stays a positional
// argument. build and apply reject it rather than waiting forever.
func TestWaitFlag_SpaceSeparatedValueIsRejected(t *testing.T) {
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { openWorkspace = orig })

	for _, verb := range []string{"build", "apply"} {
		_, _, err := runCobraCmd(t, []string{verb, "--wait", "5m"})
		if err == nil || !strings.Contains(err.Error(), "--wait=5m") {
			t.Errorf("%s --wait 5m: want an error pointing at --wait=5m, got %v", verb, err)
		}
		// --wait=5m and a bare --wait both parse and reach the workspace open.
		for _, a := range [][]string{{verb, "--wait=5m"}, {verb, "--wait"}} {
			if _, _, err := runCobraCmd(t, a); err != os.ErrNotExist {
				t.Errorf("%v: want the flag to parse and reach openWorkspace, got %v", a, err)
			}
		}
	}
}

// The help text documents the = form, the exit status and what is not covered.
func TestWaitFlag_HelpDocumentsContract(t *testing.T) {
	for _, verb := range []string{"build", "apply"} {
		out, _, err := runCobraCmd(t, []string{verb, "--help"})
		if err != nil {
			t.Fatalf("%s --help: %v", verb, err)
		}
		for _, want := range []string{"--wait=<duration>", "needs '='", "exits 75", "flake-check is not covered"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s --help lacks %q", verb, want)
			}
		}
	}
	if workspace.LockBusyExitCode != 75 {
		t.Errorf("help text says 75; LockBusyExitCode = %d", workspace.LockBusyExitCode)
	}
}
