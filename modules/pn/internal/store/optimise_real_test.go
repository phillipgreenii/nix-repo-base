//go:build realnix

package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	pnexec "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestRunOptimise_RealNixThrowawayStore runs the real `nix store optimise`
// (build tag realnix; run by the pn-realnix-tests integration gate, because the
// nix build sandbox has no taskpolicy)
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
		t.Fatalf("nix store optimise failed against a throwaway store: %v\n%s", err, out.String())
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

// TestOptimise_RealNixDedupsThrowawayStore drives the new `pn store optimise`
// entry point against the real nix and a throwaway local store: two identical
// files are added, the step runs, and the two store paths must share one inode
// (link count at least 2) with a non-empty .links. It skips only when nix or
// nice is not installed; a nix that is on PATH but fails the optimise FAILS.
func TestOptimise_RealNixDedupsThrowawayStore(t *testing.T) {
	for _, tool := range []string{"nix", "nice"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	// Resolve symlinks: nix refuses a store whose parent path is a symlink
	// (macOS TMPDIR lives under /var -> /private/var).
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte("identical content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	storeRoot := filepath.Join(root, "store")
	nixStoreDir := filepath.Join(storeRoot, "nix", "store")
	nixCmd := func(args ...string) string {
		t.Helper()
		full := append([]string{"--extra-experimental-features", "nix-command"}, args...)
		b, err := exec.Command("nix", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("nix %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	pa := nixCmd("store", "add-file", "--store", storeRoot, filepath.Join(src, "a"))
	pb := nixCmd("store", "add-file", "--store", storeRoot, "--name", "b", filepath.Join(src, "b"))
	// add-file prints the logical /nix/store path; the physical file is under the chroot.
	pa = filepath.Join(nixStoreDir, filepath.Base(pa))
	pb = filepath.Join(nixStoreDir, filepath.Base(pb))

	// Free-space gate off (the host volume is irrelevant to a throwaway store)
	// and the idle gate forced off.
	cfgDir := filepath.Join(root, "home", ".config", "pn")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "store.toml"), []byte("optimise_min_free_gb = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewWithEnv(pnexec.NewRealRunner(), Env{Home: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "home", ".config"), NixStoreDir: nixStoreDir})
	var out bytes.Buffer
	if err := s.optimise(context.Background(), &out, OptimiseOptions{Force: true}, "--store", storeRoot); err != nil {
		t.Fatalf("pn store optimise failed against a throwaway store: %v\n%s", err, out.String())
	}

	sta, err := os.Stat(pa)
	if err != nil {
		t.Fatal(err)
	}
	stb, err := os.Stat(pb)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sta, stb) {
		t.Errorf("identical files were not hard-linked:\n%s", out.String())
	}
	if n := sta.Sys().(*syscall.Stat_t).Nlink; n < 2 {
		t.Errorf("hard-link count = %d, want at least 2", n)
	}
	if got := countLinks(nixStoreDir); got < 1 {
		t.Errorf(".links entries = %d, want at least 1", got)
	}
	if !strings.Contains(out.String(), "finished after") {
		t.Errorf("end report missing:\n%s", out.String())
	}
}

func osIsDarwin() bool { return runtime.GOOS == "darwin" }
