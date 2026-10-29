package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorCmd_BrokenTomlExitsNonZero(t *testing.T) {
	root := t.TempDir()
	// invalid toml (repo missing url/remotes)
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"),
		[]byte("[repos.bad]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PN_WORKSPACE_ROOT", root)

	var out, errBuf bytes.Buffer
	err := executeWithVersion("test", []string{"workspace", "doctor"}, &out, &errBuf)
	// A report with findings is exit 10 (a check that ran and failed); 1 is
	// reserved for doctor's own failure.
	if got := ExitCode(err); got != 10 {
		t.Fatalf("broken toml: doctor reports findings, want exit 10, got %d; out=%s err=%s", got, out.String(), errBuf.String())
	}
}
