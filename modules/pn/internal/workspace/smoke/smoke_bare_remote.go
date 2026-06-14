//go:build smoke

package smoke

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupBareRemote initializes a bare git repo at <dir>/<name>.git seeded with
// the given files (executable scripts get +x), and returns its file:// URL.
// The function creates a temporary working clone, commits the files, pushes to
// the bare repo, and cleans up the working clone.
func setupBareRemote(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	bareDir := filepath.Join(dir, name+".git")
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatalf("setupBareRemote %s: mkdir bare: %v", name, err)
	}

	// Init bare repo.
	if out, err := gitCmd(t, bareDir, "init", "--bare", "-b", "main"); err != nil {
		t.Fatalf("setupBareRemote %s: git init --bare: %v\n%s", name, err, out)
	}

	// Create a temporary working clone to seed the initial commit.
	workDir, err := os.MkdirTemp("", "pn-smoke-bare-work-*")
	if err != nil {
		t.Fatalf("setupBareRemote %s: create work dir: %v", name, err)
	}
	t.Cleanup(func() { os.RemoveAll(workDir) })

	bareURL := fmt.Sprintf("file://%s", bareDir)

	// Clone the (empty) bare repo.
	if out, err := gitCmd(t, workDir, "clone", bareURL, workDir); err != nil {
		// An empty bare repo clone may warn; that's okay as long as the dir exists.
		_ = out
	}
	if out, err := gitCmd(t, workDir, "config", "user.email", "smoke@test.invalid"); err != nil {
		t.Fatalf("setupBareRemote %s: git config email: %v\n%s", name, err, out)
	}
	if out, err := gitCmd(t, workDir, "config", "user.name", "smoke"); err != nil {
		t.Fatalf("setupBareRemote %s: git config name: %v\n%s", name, err, out)
	}

	// Write and stage seed files.
	for relPath, content := range files {
		fullPath := filepath.Join(workDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("setupBareRemote %s: mkdir for %s: %v", name, relPath, err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(relPath, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
			t.Fatalf("setupBareRemote %s: write %s: %v", name, relPath, err)
		}
		if out, err := gitCmd(t, workDir, "add", relPath); err != nil {
			t.Fatalf("setupBareRemote %s: git add %s: %v\n%s", name, relPath, err, out)
		}
	}

	// Commit and push.
	if out, err := gitCmd(t, workDir, "commit", "-m", "init"); err != nil {
		t.Fatalf("setupBareRemote %s: git commit: %v\n%s", name, err, out)
	}
	if out, err := gitCmd(t, workDir, "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("setupBareRemote %s: git push: %v\n%s", name, err, out)
	}

	return bareURL
}

// gitCmd runs a git command in dir and returns (combined output, error).
func gitCmd(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Use a minimal but functional env for git setup commands (no GIT_CONFIG_GLOBAL
	// override since we need the user config to set name/email via git config).
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=smoke",
		"GIT_AUTHOR_EMAIL=smoke@test.invalid",
		"GIT_COMMITTER_NAME=smoke",
		"GIT_COMMITTER_EMAIL=smoke@test.invalid",
		"LC_ALL=C",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// bareRemoteHead returns the short SHA of HEAD in a bare git repo.
func bareRemoteHead(t *testing.T, bareDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", bareDir, "rev-parse", "HEAD")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("bareRemoteHead %s: git rev-parse HEAD: %v", bareDir, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// workspaceHead returns the short SHA of HEAD in a workspace repo clone dir.
func workspaceHead(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("workspaceHead %s: git rev-parse HEAD: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitStashList returns the lines of git stash list in a repo dir.
func gitStashList(t *testing.T, repoDir string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "stash", "list")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("gitStashList %s: %v", repoDir, err)
		return nil
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// addCommitInClone creates a new file and commits it in a workspace clone dir.
// Returns the new HEAD SHA.
func addCommitInClone(t *testing.T, cloneDir, filename, content string) string {
	t.Helper()
	path := filepath.Join(cloneDir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("addCommitInClone: write %s: %v", path, err)
	}
	if out, err := gitCmd(t, cloneDir, "add", filename); err != nil {
		t.Fatalf("addCommitInClone: git add: %v\n%s", err, out)
	}
	if out, err := gitCmd(t, cloneDir, "commit", "-m", "smoke: add "+filename); err != nil {
		t.Fatalf("addCommitInClone: git commit: %v\n%s", err, out)
	}
	return workspaceHead(t, cloneDir)
}

// gitResetHard resets a repo's HEAD to the given ref.
func gitResetHard(t *testing.T, repoDir, ref string) {
	t.Helper()
	if out, err := gitCmd(t, repoDir, "reset", "--hard", ref); err != nil {
		t.Fatalf("gitResetHard %s to %s: %v\n%s", repoDir, ref, err, out)
	}
}

// bareRemoteURL returns the file:// URL for a bare repo under dir.
func bareRemoteURL(dir, name string) string {
	return fmt.Sprintf("file://%s", filepath.Join(dir, name+".git"))
}

// --- S18 extra: build marker exists ---

func assertS18BuildMarker(t *testing.T, wsRoot string) {
	t.Helper()
	// pn workspace build only runs the build command on the terminal (consumer).
	consumerBuilt := filepath.Join(wsRoot, "consumer", "built.txt")
	if _, err := os.Stat(consumerBuilt); os.IsNotExist(err) {
		t.Errorf("S18: consumer/built.txt not found after workspace build")
	}
}

// --- S19 extra: apply marker exists ---

func assertS19ApplyMarker(t *testing.T, wsRoot string) {
	t.Helper()
	// pn workspace apply only runs the apply command on the terminal (consumer).
	consumerApplied := filepath.Join(wsRoot, "consumer", "applied.txt")
	if _, err := os.Stat(consumerApplied); os.IsNotExist(err) {
		t.Errorf("S19: consumer/applied.txt not found after workspace apply")
	}
}

// --- S20 extra: update markers and topo order ---

func assertS20UpdateMarkers(t *testing.T, wsRoot string) {
	t.Helper()
	// Both repos should have updated.txt markers (update runs per-repo).
	for _, repo := range []string{"producer", "consumer"} {
		marker := filepath.Join(wsRoot, repo, "updated.txt")
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			t.Errorf("S20: %s/updated.txt not found after workspace update", repo)
		}
	}
	// order.log should show producer before consumer (topo order).
	orderLog := filepath.Join(wsRoot, "order.log")
	data, err := os.ReadFile(orderLog)
	if err != nil {
		t.Errorf("S20: read order.log: %v", err)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Errorf("S20: order.log has %d lines, want >= 2: %q", len(lines), string(data))
		return
	}
	if lines[0] != "producer" {
		t.Errorf("S20: order.log first entry = %q, want producer", lines[0])
	}
	if lines[1] != "consumer" {
		t.Errorf("S20: order.log second entry = %q, want consumer", lines[1])
	}
}

// --- S21 extra: push advances bare remote HEADs ---

func assertS21PushAdvanced(t *testing.T, wsRoot string) {
	t.Helper()
	remotesDir := filepath.Join(wsRoot, "remotes")
	for _, repo := range []string{"producer", "consumer"} {
		bareDir := filepath.Join(remotesDir, repo+".git")
		cloneDir := filepath.Join(wsRoot, repo)

		bareHead := bareRemoteHead(t, bareDir)
		cloneHead := workspaceHead(t, cloneDir)

		if bareHead == "" || cloneHead == "" {
			// Error already reported by the helper.
			return
		}
		if bareHead != cloneHead {
			t.Errorf("S21: %s bare remote HEAD %s != workspace clone HEAD %s (push did not advance remote)",
				repo, bareHead, cloneHead)
		}
	}
}

// --- S22 extra: rebase brought workspace to remote HEAD, no stash entries ---

func assertS22RebaseResult(t *testing.T, wsRoot, scenarioName string) {
	t.Helper()
	remotesDir := filepath.Join(wsRoot, "remotes")
	// The scenario uses a single-repo workspace (consumer only).
	bareDir := filepath.Join(remotesDir, "consumer.git")
	cloneDir := filepath.Join(wsRoot, "consumer")

	bareHead := bareRemoteHead(t, bareDir)
	cloneHead := workspaceHead(t, cloneDir)

	if bareHead == "" || cloneHead == "" {
		return
	}
	if bareHead != cloneHead {
		t.Errorf("%s: workspace HEAD %s != remote HEAD %s after rebase",
			scenarioName, cloneHead, bareHead)
	}

	stash := gitStashList(t, cloneDir)
	if len(stash) > 0 {
		t.Errorf("%s: stash is not empty after rebase: %v", scenarioName, stash)
	}
}

// --- S22b extra: autostash file survived the round-trip ---

func assertS22AutostashRoundTrip(t *testing.T, wsRoot string) {
	t.Helper()
	cloneDir := filepath.Join(wsRoot, "consumer")
	// After rebase with autostash, the dirty modification to flake.nix should
	// still be present (autostash was re-applied after the rebase).
	flakeNix := filepath.Join(cloneDir, "flake.nix")
	data, err := os.ReadFile(flakeNix)
	if err != nil {
		t.Errorf("S22b: read flake.nix after autostash round-trip: %v", err)
	} else if !strings.Contains(string(data), "dirty-content") {
		t.Errorf("S22b: flake.nix does not contain 'dirty-content' after autostash round-trip;\nactual content: %s", string(data))
	}
	stash := gitStashList(t, cloneDir)
	if len(stash) > 0 {
		t.Errorf("S22b: stash not empty after autostash round-trip: %v", stash)
	}
}
