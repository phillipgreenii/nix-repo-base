package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func mkRepoDir(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", d, err)
	}
	return d
}

func openWS(t *testing.T, root, toml string) *Workspace {
	t.Helper()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w
}

func TestOverrideInputArgs_GitFileAndInputName(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dir-base")
	w := openWS(t, root, `
[repos.dir-base]
url = "github:owner/base"
input-name = "real-base"
`)
	got := w.overrideInputArgs(overrideOpts{})
	want := []string{"--override-input", "real-base", "git+file://" + filepath.Join(root, "dir-base")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestOverrideInputArgs_ExcludeTerminalAndMissingDir(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dep") // present
	// "leaf" dir intentionally absent.
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.dep]
url = "github:owner/dep"

[repos.leaf]
url = "github:owner/leaf"
`)
	got := w.overrideInputArgs(overrideOpts{ExcludeTerminal: true})
	want := []string{"--override-input", "dep", "git+file://" + filepath.Join(root, "dep")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}

func TestOverrideInputArgs_OverridePathSwap(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dep")
	alt := t.TempDir() // stand-in worktree
	w := openWS(t, root, `
[repos.dep]
url = "github:owner/dep"
`)
	got := w.overrideInputArgs(overrideOpts{OverridePaths: map[string]string{"dep": alt}})
	want := []string{"--override-input", "dep", "git+file://" + alt}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v want %#v", got, want)
	}
}
