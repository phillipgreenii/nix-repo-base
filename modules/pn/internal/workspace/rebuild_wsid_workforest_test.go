package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Real-workforest tests (V-3) of the wsid uniqueness step (bead pg2-y683ln).
// The canonical workspace and its workforest set are REAL: a real git repo
// fixture, a real WorkforestAdd (real git worktree add, real set toml copy).
// Only the nix/darwin-rebuild and probe commands of the build verb run on a
// FakeRunner, and XDG_DATA_HOME points at a temp dir so the wsid registry is
// hermetic (never the machine's ~/.local/share/pn-workspace/wsids).

const wsidWfConfig = `
[workspace]
id = "wsidwf"
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"
`

// wsidWfBuild runs `pn workspace build` for the workspace rooted at root (with
// the terminal repo dir leaf under it) on a fake runner that accepts the build
// command and the probes.
func wsidWfBuild(t *testing.T, root string) error {
	t.Helper()
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("darwin-rebuild", []string{"build", "--flake", leafDir + "#wsidhost"}, exec.Result{}, nil)
	for i := 0; i < 3; i++ {
		f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("h\n")}, nil)
	}
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	f.AddResponse("git", []string{"--exec-path"}, exec.Result{Stdout: []byte("/g\n")}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open(%s): %v", root, err)
	}
	defer w.Close()
	w.hostnameFn = func() string { return "wsidhost" }
	var out bytes.Buffer
	return w.Build(context.Background(), &out, BuildOptions{})
}

// wsidWfCanonical creates the canonical workspace (a real git repo "leaf" plus
// pn-workspace.toml) and a REAL workforest set for it under .workforests/.
func wsidWfCanonical(t *testing.T) (canonical, setRoot string) {
	t.Helper()
	canonical = t.TempDir()
	mkRepoDir(t, canonical, "leaf")
	initRealRepo(t, filepath.Join(canonical, "leaf"))
	writeFile(t, filepath.Join(canonical, "pn-workspace.toml"), wsidWfConfig)
	writeFile(t, filepath.Join(canonical, LockFileName), "{}\n")
	trustWS(t, canonical)

	w, err := Open(canonical, exec.NewRealRunner())
	if err != nil {
		t.Fatalf("Open canonical: %v", err)
	}
	defer w.Close()
	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "runner-build", NoHooks: true}); err != nil {
		t.Fatalf("WorkforestAdd: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	setRoot = filepath.Join(canonical, ".workforests", "runner-build")
	if _, err := os.Stat(filepath.Join(setRoot, "pn-workspace.toml")); err != nil {
		t.Fatalf("set config missing: %v", err)
	}
	return canonical, setRoot
}

func readWsidEntry(t *testing.T, wsid string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wsidRegistryDir(), wsid))
	if err != nil {
		t.Fatalf("read wsid entry %q: %v", wsid, err)
	}
	return string(b)
}

func listWsidDir(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(wsidRegistryDir())
	if err != nil {
		t.Fatalf("read wsid dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A build from a real workforest set root passes the wsid check even though the
// canonical root already claimed the id, and leaves the registry untouched.
func TestRebuild_WsidCheck_WorkforestSetRootExempt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	canonical, setRoot := wsidWfCanonical(t)

	// The canonical root claims the id first (as any build/apply from it does).
	if err := wsidWfBuild(t, canonical); err != nil {
		t.Fatalf("canonical build: %v", err)
	}
	if got := readWsidEntry(t, "wsidwf"); got != filepath.Clean(canonical) {
		t.Fatalf("registry entry = %q, want canonical %q", got, canonical)
	}
	before := listWsidDir(t)

	if err := wsidWfBuild(t, setRoot); err != nil {
		t.Fatalf("build from set root must pass the wsid check, got: %v", err)
	}
	if got := readWsidEntry(t, "wsidwf"); got != filepath.Clean(canonical) {
		t.Errorf("registry entry changed to %q, want canonical %q", got, canonical)
	}
	if after := listWsidDir(t); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("wsid dir changed: before %v, after %v", before, after)
	}
}

// A set root must not REGISTER an id either: with no canonical claim, building
// from the set leaves the registry empty.
func TestRebuild_WsidCheck_WorkforestSetRootDoesNotRegister(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, setRoot := wsidWfCanonical(t)

	if err := wsidWfBuild(t, setRoot); err != nil {
		t.Fatalf("build from set root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsidRegistryDir(), "wsidwf")); !os.IsNotExist(err) {
		t.Errorf("set root registered a wsid entry (stat err = %v)", err)
	}
}

// A second root that is NOT a workforest set and reuses the id is still refused.
func TestRebuild_WsidCheck_NonSetRootReusingIdStillRefused(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	canonical, _ := wsidWfCanonical(t)
	if err := wsidWfBuild(t, canonical); err != nil {
		t.Fatalf("canonical build: %v", err)
	}

	other := t.TempDir() // not under any .workforests dir
	mkRepoDir(t, other, "leaf")
	writeFile(t, filepath.Join(other, "pn-workspace.toml"), wsidWfConfig)
	err := wsidWfBuild(t, other)
	if err == nil {
		t.Fatal("a non-set root reusing the wsid MUST be refused")
	}
	if !strings.Contains(err.Error(), "already used by workspace") {
		t.Errorf("err = %v, want the wsid-uniqueness refusal", err)
	}
	if got := readWsidEntry(t, "wsidwf"); got != filepath.Clean(canonical) {
		t.Errorf("registry entry = %q, want canonical %q", got, canonical)
	}
}
