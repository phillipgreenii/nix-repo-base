// internal/workspace/realgit_test.go
package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitT runs git in dir and returns trimmed stdout, failing the test on error.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRealRepo creates a real git repo at dir with an initial commit on main.
func initRealRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", ".")
	runGitT(t, dir, "commit", "-q", "-m", "init")
}

// addCommit writes file=content, commits it, and returns the new HEAD sha.
func addCommit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", ".")
	runGitT(t, dir, "commit", "-q", "-m", msg)
	return headRev(t, dir)
}

func headRev(t *testing.T, dir string) string {
	t.Helper()
	return runGitT(t, dir, "rev-parse", "HEAD")
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	return runGitT(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// setupLocalBareRemote creates a bare repo beside dir, adds it as origin,
// and pushes the current branch. Returns the bare repo path.
func setupLocalBareRemote(t *testing.T, dir string) string {
	t.Helper()
	bare := dir + ".git"
	runGitT(t, dir, "init", "-q", "--bare", bare)
	runGitT(t, dir, "remote", "add", "origin", bare)
	runGitT(t, dir, "push", "-q", "origin", currentBranch(t, dir))
	return bare
}

// dirtyTrackedFile modifies an already-tracked file without committing.
func dirtyTrackedFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRealGitHelpers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	initRealRepo(t, dir)
	if b := currentBranch(t, dir); b != "main" {
		t.Fatalf("branch: want main, got %s", b)
	}
	h1 := headRev(t, dir)
	h2 := addCommit(t, dir, "a.txt", "x", "add a")
	if h1 == h2 || len(h2) != 40 {
		t.Fatalf("addCommit did not advance HEAD: %s -> %s", h1, h2)
	}
	bare := setupLocalBareRemote(t, dir)
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare remote not created: %v", err)
	}
}
