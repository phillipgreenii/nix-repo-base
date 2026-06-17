package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// hashRepoDir returns the hex-encoded SHA-256 of the cleaned absolute repoDir,
// matching the keying logic in appliedHashFile.
func hashRepoDir(repoDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoDir)))
	return fmt.Sprintf("%x", sum)
}

func TestNeedsRebuild_Force(t *testing.T) {
	w := &Workspace{runner: exec.NewFakeRunner()}
	got, err := w.needsRebuild(context.Background(), []string{"/x"}, true, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("force should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_DirtyTree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte(" M file\n")}, nil)
	w := &Workspace{runner: f}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("dirty tree should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_CleanUnchangedSkips(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Use the full-path-keyed filename (sha256 of "/repo") to match appliedHashFile.
	if err := os.WriteFile(filepath.Join(hashDir, hashRepoDir("/repo")), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	out := &bytes.Buffer{}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, out)
	if err != nil || got {
		t.Fatalf("clean+unchanged should skip: %v %v", got, err)
	}
}

func TestMarkApplied_WritesHead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("deadbeef\n")}, nil)
	w := &Workspace{runner: f}
	if err := w.markApplied(context.Background(), []string{"/repo"}); err != nil {
		t.Fatalf("markApplied: %v", err)
	}
	// Cache file is now keyed by sha256 of the full path, not the basename.
	got, err := os.ReadFile(filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash", hashRepoDir("/repo")))
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if string(got) != "deadbeef\n" {
		t.Errorf("stored hash: got %q", got)
	}
}

func TestCheckNixDaemon_ErrorPath(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, &exec.CommandError{Name: "nix", Args: []string{"eval"}, Result: exec.Result{ExitCode: 1}})
	w := &Workspace{runner: f}
	if err := w.checkNixDaemon(context.Background()); err == nil {
		t.Fatal("expected daemon-check error")
	}
}

// TestAppliedHashFile_FullPathKey verifies that two repo dirs sharing the same
// basename but differing in their full path (e.g. primary/foo vs set/foo) are
// keyed to DISTINCT cache files so a set and the primary never collide.
func TestAppliedHashFile_FullPathKey(t *testing.T) {
	primary := "/home/user/ws/foo"
	set := "/home/user/ws-set/foo"
	if appliedHashFile(primary) == appliedHashFile(set) {
		t.Errorf("appliedHashFile collision: same basename %q but different full paths %q and %q both map to %q",
			filepath.Base(primary), primary, set, appliedHashFile(primary))
	}
}

// TestAppliedHashFile_SamePathSameKey verifies that the same repoDir always
// maps to the same cache file (deterministic keying).
func TestAppliedHashFile_SamePathSameKey(t *testing.T) {
	dir := "/home/user/ws/foo"
	if appliedHashFile(dir) != appliedHashFile(dir) {
		t.Error("appliedHashFile is not deterministic")
	}
}

// TestCleanStaleHashCacheEntries_RemovesBasenameKeyed verifies that stale
// basename-keyed files (not 64-char hex) are removed and valid hex-keyed files
// are preserved.
func TestCleanStaleHashCacheEntries_RemovesBasenameKeyed(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Valid hex-keyed files (64-char hex strings — sha256 output).
	hexKey1 := hashRepoDir("/home/user/ws/repo-a")
	hexKey2 := hashRepoDir("/home/user/ws/repo-b")
	if err := os.WriteFile(filepath.Join(hashDir, hexKey1), []byte("aabbcc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hashDir, hexKey2), []byte("ddeeff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stale basename-keyed files — short names, not 64-char hex.
	stale1 := "repo-a"
	stale2 := "nix-repo-base"
	stale3 := "some-other-repo"
	for _, name := range []string{stale1, stale2, stale3} {
		if err := os.WriteFile(filepath.Join(hashDir, name), []byte("oldvalue\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := cleanStaleHashCacheEntries(); err != nil {
		t.Fatalf("cleanStaleHashCacheEntries: %v", err)
	}

	// Hex-keyed files must still exist.
	for _, name := range []string{hexKey1, hexKey2} {
		if _, err := os.Stat(filepath.Join(hashDir, name)); err != nil {
			t.Errorf("hex-keyed file %q was unexpectedly removed", name)
		}
	}

	// Stale basename-keyed files must be gone.
	for _, name := range []string{stale1, stale2, stale3} {
		if _, err := os.Stat(filepath.Join(hashDir, name)); !os.IsNotExist(err) {
			t.Errorf("stale basename-keyed file %q was not removed (err: %v)", name, err)
		}
	}
}

// TestCleanStaleHashCacheEntries_Idempotent verifies that running cleanup twice
// on a directory that already contains only hex-keyed files is a no-op (no
// error, no files removed).
func TestCleanStaleHashCacheEntries_Idempotent(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hexKey := hashRepoDir("/home/user/ws/repo")
	if err := os.WriteFile(filepath.Join(hashDir, hexKey), []byte("cafebabe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := cleanStaleHashCacheEntries(); err != nil {
			t.Fatalf("run %d: cleanStaleHashCacheEntries: %v", i+1, err)
		}
	}

	if _, err := os.Stat(filepath.Join(hashDir, hexKey)); err != nil {
		t.Errorf("hex-keyed file was unexpectedly removed on second run")
	}
}

// TestCleanStaleHashCacheEntries_EmptyDir verifies that an empty cache
// directory does not cause an error.
func TestCleanStaleHashCacheEntries_EmptyDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := cleanStaleHashCacheEntries(); err != nil {
		t.Fatalf("cleanStaleHashCacheEntries on empty dir: %v", err)
	}
}

// TestCleanStaleHashCacheEntries_MissingDir verifies that a missing cache
// directory (no prior apply) does not cause an error.
func TestCleanStaleHashCacheEntries_MissingDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// Do NOT create the directory — simulate a first-ever run.
	if err := cleanStaleHashCacheEntries(); err != nil {
		t.Fatalf("cleanStaleHashCacheEntries with missing dir: %v", err)
	}
}

// TestMarkApplied_SetVsPrimaryNoCollision verifies that markApplied for one
// repo path is not read back as "applied" by needsRebuild for a repo with the
// same basename but a different full path.
func TestMarkApplied_SetVsPrimaryNoCollision(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	primaryDir := "/primary/ws/repo"
	setDir := "/set/ws/repo"
	const headHash = "cafebabe"

	// Mark the primary repo as applied.
	fMark := exec.NewFakeRunner()
	fMark.AddResponse("git", []string{"-C", primaryDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte(headHash + "\n")}, nil)
	wMark := &Workspace{runner: fMark}
	if err := wMark.markApplied(context.Background(), []string{primaryDir}); err != nil {
		t.Fatalf("markApplied: %v", err)
	}

	// needsRebuild for setDir (same basename, different parent) must see no stored hash
	// and therefore return rebuild=true.
	fCheck := exec.NewFakeRunner()
	fCheck.AddResponse("git", []string{"-C", setDir, "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	fCheck.AddResponse("git", []string{"-C", setDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte(headHash + "\n")}, nil)
	wCheck := &Workspace{runner: fCheck}
	rebuild, err := wCheck.needsRebuild(context.Background(), []string{setDir}, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("needsRebuild: %v", err)
	}
	if !rebuild {
		t.Error("needsRebuild should return true for setDir: primary's applied-hash must not bleed into set's cache entry")
	}
}
