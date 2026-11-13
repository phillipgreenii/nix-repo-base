package store

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	pnexec "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestRunOptimise_RealNixThrowawayStore runs the real `nix store optimise`
// through the real runner against an empty throwaway store (--store <tmp>), and
// checks the printed command line carries nice and (on darwin) taskpolicy -b.
func TestRunOptimise_RealNixThrowawayStore(t *testing.T) {
	for _, tool := range []string{"nix", "nice"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	tmp := t.TempDir()
	s := NewWithEnv(pnexec.NewRealRunner(), Env{Home: tmp, NixStoreDir: tmp})
	var out bytes.Buffer
	if err := s.runOptimise(context.Background(), &out, "--store", tmp+"/store"); err != nil {
		t.Skipf("nix store optimise not runnable against a throwaway store here: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "nice -n 10 ") || !strings.Contains(got, "nix store optimise") {
		t.Errorf("command line missing nice:\n%s", got)
	}
	if strings.Contains(got, "taskpolicy") != osIsDarwin() {
		t.Errorf("taskpolicy -b must be present exactly on darwin:\n%s", got)
	}
	if !strings.Contains(got, "finished after") {
		t.Errorf("end report missing:\n%s", got)
	}
}
