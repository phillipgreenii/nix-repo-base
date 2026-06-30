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
	if ExitCode(err) == 0 {
		t.Fatalf("broken toml should exit non-zero; out=%s err=%s", out.String(), errBuf.String())
	}
}
