//go:build smoke

package smoke

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	cmd.Env = append(
		os.Environ(),
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

// --- S23 extra: format banners appear in topo order (producer before consumer) ---

func assertS23FormatTopoOrder(t *testing.T, result scenarioResult) {
	t.Helper()
	stdout := string(result.Stdout)
	producerIdx := strings.Index(stdout, "format producer")
	consumerIdx := strings.Index(stdout, "format consumer")

	if producerIdx < 0 {
		t.Errorf("S23: stdout missing 'format producer' banner; got:\n%s", stdout)
		return
	}
	if consumerIdx < 0 {
		t.Errorf("S23: stdout missing 'format consumer' banner; got:\n%s", stdout)
		return
	}
	if producerIdx > consumerIdx {
		t.Errorf("S23: 'format producer' appeared after 'format consumer' (topo order violated);\nstdout:\n%s", stdout)
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

// --- S24 extra: workforest set exists, all repos on feature-x, files copied ---

func assertS24WorkforestAdd(t *testing.T, wsRoot string) {
	t.Helper()
	setDir := filepath.Join(wsRoot, ".workforests", "feature-x")

	// 1. Set dir must exist.
	if _, err := os.Stat(setDir); os.IsNotExist(err) {
		t.Errorf("S24: workforest set dir %s does not exist", setDir)
		return
	}

	// 2. Each repo in the set must be on branch feature-x.
	for _, repo := range []string{"producer", "consumer"} {
		repoDir := filepath.Join(setDir, repo)
		if _, err := os.Stat(repoDir); os.IsNotExist(err) {
			t.Errorf("S24: set repo dir %s does not exist", repoDir)
			continue
		}
		cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("S24: git rev-parse HEAD in %s: %v", repoDir, err)
			continue
		}
		branch := strings.TrimSpace(string(out))
		if branch != "feature-x" {
			t.Errorf("S24: %s in set is on branch %q, want feature-x", repo, branch)
		}
	}

	// 3. pn-workspace.toml and pn-workspace.lock.json must be copied into the set dir.
	for _, f := range []string{"pn-workspace.toml", "pn-workspace.lock.json"} {
		if _, err := os.Stat(filepath.Join(setDir, f)); os.IsNotExist(err) {
			t.Errorf("S24: %s not copied into set dir %s", f, setDir)
		}
	}
}

// --- S27 extra: set dir gone; branches left behind ---

func assertS27WorkforestRemove(t *testing.T, wsRoot string) {
	t.Helper()
	setDir := filepath.Join(wsRoot, ".workforests", "feature-x")

	// 1. Set dir must be gone.
	if _, err := os.Stat(setDir); err == nil {
		t.Errorf("S27: set dir %s still exists after workforest remove", setDir)
	}

	// 2. Branch feature-x must still exist in each canonical repo.
	for _, repo := range []string{"producer", "consumer"} {
		repoDir := filepath.Join(wsRoot, repo)
		cmd := exec.Command("git", "-C", repoDir, "branch", "--list", "feature-x")
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("S27: git branch --list feature-x in %s: %v", repoDir, err)
			continue
		}
		if strings.TrimSpace(string(out)) == "" {
			t.Errorf("S27: branch feature-x was deleted from canonical repo %s (remove should leave branches behind)", repo)
		}
	}
}

// --- S28 extra: workforest add, rm -rf set dir, prune, verify no stale entries ---

func assertS28WorkforestPrune(t *testing.T, wsRoot, pnBin string, env []string) {
	t.Helper()

	// Step 1: Create a workforest set for "feature-z".
	r := runCommand(t, pnBin, wsRoot, []string{"workspace", "workforest", "add", "feature-z"}, env)
	if r.ExitCode != 0 {
		t.Fatalf("S28: workforest add failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	setDir := filepath.Join(wsRoot, ".workforests", "feature-z")

	// Step 2: Manually remove the set dir to create stale .git/worktrees entries.
	if err := os.RemoveAll(setDir); err != nil {
		t.Fatalf("S28: rm -rf set dir: %v", err)
	}

	// Step 3: Run workforest prune.
	r2 := runCommand(t, pnBin, wsRoot, []string{"workspace", "workforest", "prune"}, env)
	if r2.ExitCode != 0 {
		t.Fatalf("S28: workforest prune failed (exit %d)\nstdout: %s\nstderr: %s",
			r2.ExitCode, r2.Stdout, r2.Stderr)
	}

	// Step 4: Verify each canonical repo's git worktree list no longer shows the stale entry.
	for _, repo := range []string{"producer", "consumer"} {
		repoDir := filepath.Join(wsRoot, repo)
		cmd := exec.Command("git", "-C", repoDir, "worktree", "list")
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("S28: git worktree list in %s: %v", repoDir, err)
			continue
		}
		if strings.Contains(string(out), "feature-z") {
			t.Errorf("S28: git worktree list in %s still shows feature-z after prune:\n%s", repoDir, string(out))
		}
	}
}

// --- S29 extra: verbs-in-a-set + P1 primary-unchanged ---

// setEnv returns a copy of env with PN_WORKSPACE_ROOT overridden to newRoot.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			found = true
		} else {
			out = append(out, kv)
		}
	}
	if !found {
		out = append(out, prefix+val)
	}
	return out
}

// gitHeadSHA returns the HEAD SHA of a repo dir (empty string + error on failure).
func gitHeadSHA(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("S29: git rev-parse HEAD in %s: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitStatusPorcelain returns the output of git status --porcelain in a repo dir.
func gitStatusPorcelain(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "status", "--porcelain")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("S29: git status --porcelain in %s: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

func assertS29VerbsInASet(t *testing.T, wsRoot, pnBin string, env []string) {
	t.Helper()
	setDir := filepath.Join(wsRoot, ".workforests", "feature-y")

	// Verify the set dir was created by command.txt's workforest add.
	if _, err := os.Stat(setDir); os.IsNotExist(err) {
		t.Fatalf("S29: set dir %s does not exist (workforest add may have failed)", setDir)
	}

	// Snapshot primary (canonical) repo HEADs and status before running verbs in the set.
	type repoSnapshot struct{ head, status string }
	primarySnapshot := make(map[string]repoSnapshot)
	for _, repo := range []string{"producer", "consumer"} {
		canonDir := filepath.Join(wsRoot, repo)
		primarySnapshot[repo] = repoSnapshot{
			head:   gitHeadSHA(t, canonDir),
			status: gitStatusPorcelain(t, canonDir),
		}
	}

	// Build an env with PN_WORKSPACE_ROOT pointing at the set dir.
	setEnvFull := setEnv(env, "PN_WORKSPACE_ROOT", setDir)

	// Run status (informational; exit 0).
	if r := runCommand(t, pnBin, setDir, []string{"workspace", "status"}, setEnvFull); r.ExitCode != 0 {
		t.Errorf("S29: workspace status in set failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// Run build.
	if r := runCommand(t, pnBin, setDir, []string{"workspace", "build"}, setEnvFull); r.ExitCode != 0 {
		t.Errorf("S29: workspace build in set failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	// Assert build.sh marker was created in the set's consumer dir.
	builtMarker := filepath.Join(setDir, "consumer", "built.txt")
	if _, err := os.Stat(builtMarker); os.IsNotExist(err) {
		t.Errorf("S29: consumer/built.txt not found in set after workspace build")
	}

	// Run update --in-place (worktree-isolation flow is refused inside a set;
	// --in-place relocks the set's own worktrees without touching canonical main).
	if r := runCommand(t, pnBin, setDir, []string{"workspace", "update", "--in-place"}, setEnvFull); r.ExitCode != 0 {
		t.Errorf("S29: workspace update --in-place in set failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// Run rebase main (rebases each set repo onto main).
	if r := runCommand(t, pnBin, setDir, []string{"workspace", "rebase", "main"}, setEnvFull); r.ExitCode != 0 {
		t.Errorf("S29: workspace rebase main in set failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// Run push --set-upstream (bare remotes, feature-y branch has no upstream yet).
	if r := runCommand(t, pnBin, setDir, []string{"workspace", "push", "--set-upstream"}, setEnvFull); r.ExitCode != 0 {
		t.Errorf("S29: workspace push --set-upstream in set failed (exit %d)\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// P1 smoke: assert the primary (canonical) checkouts are unchanged.
	for _, repo := range []string{"producer", "consumer"} {
		canonDir := filepath.Join(wsRoot, repo)
		snap := primarySnapshot[repo]
		afterHead := gitHeadSHA(t, canonDir)
		afterStatus := gitStatusPorcelain(t, canonDir)
		if snap.head != afterHead {
			t.Errorf("S29 P1: primary %s HEAD changed from %s to %s after verbs-in-a-set",
				repo, snap.head, afterHead)
		}
		if snap.status != afterStatus {
			t.Errorf("S29 P1: primary %s status changed after verbs-in-a-set\nbefore: %q\nafter:  %q",
				repo, snap.status, afterStatus)
		}
	}
}

// --- S30 extra: push --set-upstream advances bare remote branch and records upstream ---

func assertS30PushSetUpstream(t *testing.T, wsRoot string) {
	t.Helper()
	remotesDir := filepath.Join(wsRoot, "remotes")
	for _, repo := range []string{"producer", "consumer"} {
		bareDir := filepath.Join(remotesDir, repo+".git")
		cloneDir := filepath.Join(wsRoot, repo)

		// Determine which branch the clone is on.
		branchCmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--abbrev-ref", "HEAD")
		branchCmd.Env = os.Environ()
		branchOut, err := branchCmd.Output()
		if err != nil {
			t.Errorf("S30: %s cannot determine current branch: %v", repo, err)
			continue
		}
		branch := strings.TrimSpace(string(branchOut))

		// 1. Bare remote's ref for this branch must match workspace clone HEAD.
		// (bare remote HEAD points to default branch; we check the feature branch ref)
		bareRef := fmt.Sprintf("refs/heads/%s", branch)
		bareCmd := exec.Command("git", "-C", bareDir, "rev-parse", bareRef)
		bareCmd.Env = os.Environ()
		bareOut, err := bareCmd.Output()
		if err != nil {
			t.Errorf("S30: %s bare remote has no ref %s after push --set-upstream: %v", repo, bareRef, err)
			continue
		}
		bareHead := strings.TrimSpace(string(bareOut))
		cloneHead := workspaceHead(t, cloneDir)
		if bareHead == "" || cloneHead == "" {
			return
		}
		if bareHead != cloneHead {
			t.Errorf("S30: %s bare remote %s = %s, workspace clone HEAD = %s (push did not advance remote branch)",
				repo, branch, bareHead, cloneHead)
		}

		// 2. The workspace clone's branch must now track origin/<branch>.
		upCmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--abbrev-ref", "@{u}")
		upCmd.Env = os.Environ()
		upOut, err := upCmd.Output()
		if err != nil {
			t.Errorf("S30: %s branch %s has no upstream after push --set-upstream (git rev-parse @{u}: %v)", repo, branch, err)
			continue
		}
		upstream := strings.TrimSpace(string(upOut))
		if !strings.HasPrefix(upstream, "origin/") {
			t.Errorf("S30: %s upstream = %q, want origin/<branch>", repo, upstream)
		}
	}
}

// --- S31 extra: rebase [branch] rebased onto local ref, no fetch occurred ---

func assertS31RebaseBranch(t *testing.T, wsRoot string) {
	t.Helper()
	remotesDir := filepath.Join(wsRoot, "remotes")

	for _, repo := range []string{"producer", "consumer"} {
		bareDir := filepath.Join(remotesDir, repo+".git")
		cloneDir := filepath.Join(wsRoot, repo)

		// 1. After `rebase main` the workspace clone's feature branch must contain
		// the "main-extra" commit (i.e. the rebase brought in main's content).
		markerPath := filepath.Join(cloneDir, "main-extra.txt")
		if _, err := os.Stat(markerPath); os.IsNotExist(err) {
			t.Errorf("S31: %s main-extra.txt not present after rebase main (rebase did not bring in main commits)", repo)
		}

		// 2. No fetch: the bare remote's reflog must not have grown after the command.
		// setup.sh wrote the pre-command length to <repo>-reflog-before.txt.
		beforeFile := filepath.Join(wsRoot, repo+"-reflog-before.txt")
		beforeData, err := os.ReadFile(beforeFile)
		if err != nil {
			t.Logf("S31: %s cannot read reflog-before file: %v (skipping no-fetch assertion)", repo, err)
			continue
		}
		before := strings.TrimSpace(string(beforeData))
		beforeN, _ := strconv.Atoi(before)

		// Capture post-command reflog length.
		cmd := exec.Command("git", "-C", bareDir, "reflog", "--all")
		cmd.Env = os.Environ()
		afterOut, afterErr := cmd.Output()
		afterN := 0
		if afterErr == nil {
			afterN = len(strings.Split(strings.TrimSpace(string(afterOut)), "\n"))
			if strings.TrimSpace(string(afterOut)) == "" {
				afterN = 0
			}
		}
		if afterN > beforeN {
			t.Errorf("S31: %s bare remote reflog grew from %d to %d entries after rebase main (fetch should not have occurred)",
				repo, beforeN, afterN)
		}
	}
}

// --- S32 extra: pn workspace update writes events.jsonl ---

// parseJSONLEvents reads a JSONL file and returns each line as a parsed map.
func parseJSONLEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("S32: open events.jsonl %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var recs []map[string]any
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Errorf("S32: events.jsonl line %d is not valid JSON: %v\nline: %s", lineNum, err, string(line))
			continue
		}
		// Assert mandatory keys are present on every event line.
		for _, key := range []string{"time", "level", "kind", "msg"} {
			if _, ok := m[key]; !ok {
				t.Errorf("S32: events.jsonl line %d missing key %q: %v", lineNum, key, m)
			}
		}
		recs = append(recs, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("S32: scan events.jsonl: %v", err)
	}
	return recs
}

// countKind returns the number of records whose "kind" field equals k.
func countKind(recs []map[string]any, k string) int {
	n := 0
	for _, m := range recs {
		if m["kind"] == k {
			n++
		}
	}
	return n
}

func assertS32EventsJSONL(t *testing.T, wsRoot string, env []string) {
	t.Helper()

	// Resolve XDG_STATE_HOME from the scenario env (set by buildScrubbedEnv).
	var xdgState string
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			xdgState = strings.TrimPrefix(kv, "XDG_STATE_HOME=")
			break
		}
	}
	if xdgState == "" {
		t.Fatalf("S32: XDG_STATE_HOME not found in scenario env")
	}

	eventsPath := filepath.Join(xdgState, "pn", "events.jsonl")

	// 1. File must exist.
	if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
		t.Fatalf("S32: events.jsonl not found at %s (workspace update did not write event log)", eventsPath)
	}

	recs := parseJSONLEvents(t, eventsPath)
	if t.Failed() {
		return
	}

	// 2. At least one run_start and one run_end.
	if countKind(recs, "run_start") < 1 {
		t.Errorf("S32: no run_start event found in events.jsonl; events: %v", recs)
	}
	if countKind(recs, "run_end") < 1 {
		t.Errorf("S32: no run_end event found in events.jsonl; events: %v", recs)
	}

	// 3. One project_result per workspace repo (producer + consumer = 2).
	// The topo iteration visits exactly the repos in the workspace config.
	projectResults := countKind(recs, "project_result")
	if projectResults != 2 {
		t.Errorf("S32: expected 2 project_result events (one per repo), got %d; events: %v", projectResults, recs)
	}
}

// --- S33 extra: worktree update integrated to primary + remote; no leftover ---

func assertS33WorktreeUpdate(t *testing.T, wsRoot string) {
	t.Helper()
	primary := filepath.Join(wsRoot, "solo")
	bare := filepath.Join(wsRoot, "remotes", "solo.git")

	logOut, err := exec.Command("git", "-C", primary, "log", "--oneline", "-n", "5").Output()
	if err != nil {
		t.Fatalf("S33: git log primary: %v", err)
	}
	if !strings.Contains(string(logOut), "update-locks: bump locked.txt") {
		t.Errorf("S33: primary main missing relock commit; log:\n%s", logOut)
	}

	remoteHead, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33: git rev-parse remote: %v", err)
	}
	primHead, err := exec.Command("git", "-C", primary, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33: git rev-parse primary: %v", err)
	}
	if strings.TrimSpace(string(remoteHead)) != strings.TrimSpace(string(primHead)) {
		t.Errorf("S33: remote main %s != primary main %s", strings.TrimSpace(string(remoteHead)), strings.TrimSpace(string(primHead)))
	}

	if entries, err := os.ReadDir(filepath.Join(wsRoot, ".workforests", ".pn-update")); err == nil && len(entries) > 0 {
		t.Errorf("S33: .pn-update worktree left behind: %v", entries)
	}
}

// --- S33b extra: dirty primary main fast-forwards via ff-first/autostash ---

// assertS33bWorktreeUpdateDirtyMain verifies the ff-first dirty-main integration:
// the relock reaches both primary and remote main (fast-forwarded), the dirty
// flake.nix modification survives the FIRST ff (no collision → no autostash
// round-trip is exercised), the stash list is empty, and no .pn-update worktree
// residue is left behind.
func assertS33bWorktreeUpdateDirtyMain(t *testing.T, wsRoot string) {
	t.Helper()
	primary := filepath.Join(wsRoot, "solo")
	bare := filepath.Join(wsRoot, "remotes", "solo.git")

	logOut, err := exec.Command("git", "-C", primary, "log", "--oneline", "-n", "5").Output()
	if err != nil {
		t.Fatalf("S33b: git log primary: %v", err)
	}
	if !strings.Contains(string(logOut), "update-locks: bump locked.txt") {
		t.Errorf("S33b: primary main missing relock commit (dirty main should still ff); log:\n%s", logOut)
	}

	remoteHead, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33b: git rev-parse remote: %v", err)
	}
	primHead, err := exec.Command("git", "-C", primary, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("S33b: git rev-parse primary: %v", err)
	}
	if strings.TrimSpace(string(remoteHead)) != strings.TrimSpace(string(primHead)) {
		t.Errorf("S33b: remote main %s != primary main %s", strings.TrimSpace(string(remoteHead)), strings.TrimSpace(string(primHead)))
	}

	// The uncommitted flake.nix change must survive: it does not collide with the
	// relocked path, so the direct ff-first keeps it in place (no autostash
	// round-trip happens in this scenario).
	flakeNix := filepath.Join(primary, "flake.nix")
	data, err := os.ReadFile(flakeNix)
	if err != nil {
		t.Errorf("S33b: read primary flake.nix after update: %v", err)
	} else if !strings.Contains(string(data), "dirty-content") {
		t.Errorf("S33b: primary flake.nix lost 'dirty-content' after dirty-main update;\nactual content: %s", string(data))
	}

	// No retained autostash.
	if stash := gitStashList(t, primary); len(stash) > 0 {
		t.Errorf("S33b: stash not empty after dirty-main update: %v", stash)
	}

	if entries, err := os.ReadDir(filepath.Join(wsRoot, ".workforests", ".pn-update")); err == nil && len(entries) > 0 {
		t.Errorf("S33b: .pn-update worktree left behind: %v", entries)
	}
}

// --- S34 extra: subset set contains only the chosen repos ---

func assertS34WorkforestSubset(t *testing.T, wsRoot string) {
	t.Helper()
	setDir := filepath.Join(wsRoot, ".workforests", "feature-x")

	// 1. Set dir must exist.
	if _, err := os.Stat(setDir); os.IsNotExist(err) {
		t.Errorf("S34: workforest set dir %s does not exist", setDir)
		return
	}

	// 2. producer + consumer worktrees must exist and be on feature-x.
	for _, repo := range []string{"producer", "consumer"} {
		repoDir := filepath.Join(setDir, repo)
		if _, err := os.Stat(repoDir); os.IsNotExist(err) {
			t.Errorf("S34: member repo dir %s does not exist", repoDir)
			continue
		}
		cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("S34: git rev-parse HEAD in %s: %v", repoDir, err)
			continue
		}
		if branch := strings.TrimSpace(string(out)); branch != "feature-x" {
			t.Errorf("S34: %s in set is on branch %q, want feature-x", repo, branch)
		}
	}

	// 3. extra must NOT have a worktree in the set.
	if _, err := os.Stat(filepath.Join(setDir, "extra")); err == nil {
		t.Errorf("S34: excluded repo 'extra' should not have a worktree in the subset set")
	}

	// 4. The set's pn-workspace.toml lists ONLY producer + consumer (not extra).
	cfgBytes, err := os.ReadFile(filepath.Join(setDir, "pn-workspace.toml"))
	if err != nil {
		t.Errorf("S34: read set pn-workspace.toml: %v", err)
		return
	}
	cfg := string(cfgBytes)
	if !strings.Contains(cfg, "[repos.producer]") || !strings.Contains(cfg, "[repos.consumer]") {
		t.Errorf("S34: set config must list producer + consumer; got:\n%s", cfg)
	}
	if strings.Contains(cfg, "[repos.extra]") {
		t.Errorf("S34: set config must NOT list excluded repo extra; got:\n%s", cfg)
	}

	// 5. Canonical pn-workspace.toml must be unchanged (still lists all three).
	canonBytes, err := os.ReadFile(filepath.Join(wsRoot, "pn-workspace.toml"))
	if err != nil {
		t.Errorf("S34: read canonical pn-workspace.toml: %v", err)
		return
	}
	if !strings.Contains(string(canonBytes), "[repos.extra]") {
		t.Errorf("S34: canonical config must be unchanged and still list extra; got:\n%s", string(canonBytes))
	}
}

// --- S35 extra: add-repo then remove-repo round trip + P1 unchanged ---

func assertS35WorkforestAddRemoveRepo(t *testing.T, wsRoot, pnBin string, env []string) {
	t.Helper()
	setDir := filepath.Join(wsRoot, ".workforests", "feature-x")

	// The subset set {producer, consumer} was created by command.txt.
	if _, err := os.Stat(setDir); os.IsNotExist(err) {
		t.Fatalf("S35: subset set dir %s does not exist (workforest add may have failed)", setDir)
	}

	// Snapshot the canonical extra repo (P1: must be unchanged at the end).
	extraCanon := filepath.Join(wsRoot, "extra")
	headBefore := gitHeadSHA(t, extraCanon)
	statusBefore := gitStatusPorcelain(t, extraCanon)

	// Step 1: add-repo extra to the live set.
	r := runCommand(t, pnBin, wsRoot, []string{"workspace", "workforest", "add-repo", "feature-x", "extra"}, env)
	if r.ExitCode != 0 {
		t.Fatalf("S35: add-repo failed (exit %d)\nstdout: %s\nstderr: %s", r.ExitCode, r.Stdout, r.Stderr)
	}
	// extra worktree must now exist and be on feature-x.
	extraSet := filepath.Join(setDir, "extra")
	if _, err := os.Stat(extraSet); os.IsNotExist(err) {
		t.Errorf("S35: after add-repo, extra worktree %s does not exist", extraSet)
	}
	// Set membership must now include extra.
	if cfg := readFileString(t, filepath.Join(setDir, "pn-workspace.toml")); !strings.Contains(cfg, "[repos.extra]") {
		t.Errorf("S35: after add-repo, set config must list extra; got:\n%s", cfg)
	}

	// Step 2: remove-repo extra from the live set.
	r2 := runCommand(t, pnBin, wsRoot, []string{"workspace", "workforest", "remove-repo", "feature-x", "extra"}, env)
	if r2.ExitCode != 0 {
		t.Fatalf("S35: remove-repo failed (exit %d)\nstdout: %s\nstderr: %s", r2.ExitCode, r2.Stdout, r2.Stderr)
	}
	// extra worktree must be gone.
	if _, err := os.Stat(extraSet); err == nil {
		t.Errorf("S35: after remove-repo, extra worktree %s still exists", extraSet)
	}
	// Set membership must no longer include extra.
	if cfg := readFileString(t, filepath.Join(setDir, "pn-workspace.toml")); strings.Contains(cfg, "[repos.extra]") {
		t.Errorf("S35: after remove-repo, set config must NOT list extra; got:\n%s", cfg)
	}

	// remove-repo must NOT delete the feature-x branch in canonical extra.
	cmd := exec.Command("git", "-C", extraCanon, "branch", "--list", "feature-x")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("S35: git branch --list feature-x in %s: %v", extraCanon, err)
	} else if strings.TrimSpace(string(out)) == "" {
		t.Errorf("S35: remove-repo deleted feature-x from canonical extra (must leave branch behind)")
	}

	// P1: the canonical extra checkout's HEAD + working state are unchanged.
	if h := gitHeadSHA(t, extraCanon); h != headBefore {
		t.Errorf("S35: canonical extra HEAD changed: %q -> %q (P1 violated)", headBefore, h)
	}
	if s := gitStatusPorcelain(t, extraCanon); s != statusBefore {
		t.Errorf("S35: canonical extra working state changed (P1 violated): %q -> %q", statusBefore, s)
	}
}

// readFileString reads a file and returns its contents as a string (test helper).
func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// assertS36WorkspaceInfoApplied parses the `pn workspace info --json` output
// (the last command in s36) and asserts the schema plus that at least one repo
// has a populated applied_ref (proving the preceding apply recorded state).
func assertS36WorkspaceInfoApplied(t *testing.T, lastResult scenarioResult) {
	t.Helper()
	var info struct {
		Wsid     string `json:"wsid"`
		Root     string `json:"root"`
		Terminal string `json:"terminal"`
		Repos    []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			AppliedRef string `json:"applied_ref"`
			Dirty      bool   `json:"dirty"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(lastResult.Stdout, &info); err != nil {
		t.Fatalf("S36: parse `workspace info --json`: %v\n%s", err, string(lastResult.Stdout))
	}
	if info.Wsid != "smoke-s36" {
		t.Fatalf("S36: wsid = %q, want %q", info.Wsid, "smoke-s36")
	}
	if info.Root == "" {
		t.Fatalf("S36: root must be non-empty")
	}
	if info.Terminal != "consumer" {
		t.Fatalf("S36: terminal = %q, want %q", info.Terminal, "consumer")
	}
	if len(info.Repos) == 0 {
		t.Fatalf("S36: repos must be non-empty")
	}
	applied := false
	for _, r := range info.Repos {
		if r.Path == "" {
			t.Fatalf("S36: repo %q has empty path", r.Name)
		}
		if r.AppliedRef != "" {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("S36: no repo has a populated applied_ref after apply; info=%+v", info)
	}
}
