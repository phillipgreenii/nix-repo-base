//go:build smoke

package smoke

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
)

// setupBareRemote initializes a bare git repo at <dir>/<name>.git seeded with
// the given files (executable scripts get +x), and returns its file:// URL.
//
// Both repositories come from x/gitfixture (hermetic by construction: an
// allowlisted child environment, a fixture HOME, no system config, hooks
// redirected to an empty directory), so no GIT_DIR-family variable in the test
// process can reach them. The bare repository is built with Root=<dir> and
// Name=<name>.git, which is what places it at <dir>/<name>.git; gitfixture
// also creates its "home" and "hooks" infrastructure directories under <dir>.
// The seed work tree lives in its own fixture root under t.TempDir(), is
// pushed to the bare repository's file:// URL, and is removed at test end.
func setupBareRemote(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	ctx := t.Context()

	bare, err := gitfixture.NewRepo(ctx, dir, gitfixture.RepoOptions{
		Suite: "pn-smoke-bare-remote",
		Name:  name + ".git",
		Bare:  true,
	})
	if err != nil {
		t.Fatalf("setupBareRemote %s: bare repo: %v", name, err)
	}
	bareURL := "file://" + bare.Dir

	seed, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{
		Suite: "pn-smoke-bare-remote-seed",
		Name:  "seed",
	})
	if err != nil {
		t.Fatalf("setupBareRemote %s: seed repo: %v", name, err)
	}

	// Write and stage seed files (executable scripts get +x).
	for relPath, content := range files {
		if err := seed.WriteFile(relPath, content); err != nil {
			t.Fatalf("setupBareRemote %s: write %s: %v", name, relPath, err)
		}
		if strings.HasSuffix(relPath, ".sh") {
			if err := os.Chmod(filepath.Join(seed.Dir, relPath), 0o755); err != nil {
				t.Fatalf("setupBareRemote %s: chmod %s: %v", name, relPath, err)
			}
		}
		if _, err := seed.Client.Run(ctx, "add", "--", relPath); err != nil {
			t.Fatalf("setupBareRemote %s: git add %s: %v", name, relPath, err)
		}
	}

	// Commit and push.
	if _, err := seed.Client.Run(ctx, "commit", "-m", "init"); err != nil {
		t.Fatalf("setupBareRemote %s: git commit: %v", name, err)
	}
	if _, err := seed.Client.Run(ctx, "remote", "add", "origin", bareURL); err != nil {
		t.Fatalf("setupBareRemote %s: git remote add: %v", name, err)
	}
	if _, err := seed.Client.Run(ctx, "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("setupBareRemote %s: git push: %v", name, err)
	}

	return bareURL
}

// gitRead runs a read-only git command in dir through x/gitclient and returns
// its stdout. The child environment is gitclient's allowlist (PATH only, plus a
// throwaway HOME and no system or global config), so nothing from the test
// process's GIT_* environment can redirect it to another repository, even if a
// test reintroduced one with t.Setenv after TestMain. Callers trim as needed.
func gitRead(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx := t.Context()
	c, err := gitclient.New(
		ctx, dir,
		gitclient.WithHome(t.TempDir()),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		gitclient.WithEnv("GIT_CONFIG_GLOBAL", "/dev/null"),
	)
	if err != nil {
		return "", err
	}
	out, err := c.Run(ctx, args...)
	return string(out), err
}

// bareRemoteHead returns the short SHA of HEAD in a bare git repo.
func bareRemoteHead(t *testing.T, bareDir string) string {
	t.Helper()
	out, err := gitRead(t, bareDir, "rev-parse", "HEAD")
	if err != nil {
		t.Errorf("bareRemoteHead %s: git rev-parse HEAD: %v", bareDir, err)
		return ""
	}
	return strings.TrimSpace(out)
}

// workspaceHead returns the short SHA of HEAD in a workspace repo clone dir.
func workspaceHead(t *testing.T, repoDir string) string {
	t.Helper()
	out, err := gitRead(t, repoDir, "rev-parse", "HEAD")
	if err != nil {
		t.Errorf("workspaceHead %s: git rev-parse HEAD: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(out)
}

// gitStashList returns the lines of git stash list in a repo dir.
func gitStashList(t *testing.T, repoDir string) []string {
	t.Helper()
	out, err := gitRead(t, repoDir, "stash", "list")
	if err != nil {
		t.Errorf("gitStashList %s: %v", repoDir, err)
		return nil
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
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
		out, err := gitRead(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("S24: git rev-parse HEAD in %s: %v", repoDir, err)
			continue
		}
		branch := strings.TrimSpace(out)
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
		out, err := gitRead(t, repoDir, "branch", "--list", "feature-x")
		if err != nil {
			t.Errorf("S27: git branch --list feature-x in %s: %v", repoDir, err)
			continue
		}
		if strings.TrimSpace(out) == "" {
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
		out, err := gitRead(t, repoDir, "worktree", "list")
		if err != nil {
			t.Errorf("S28: git worktree list in %s: %v", repoDir, err)
			continue
		}
		if strings.Contains(out, "feature-z") {
			t.Errorf("S28: git worktree list in %s still shows feature-z after prune:\n%s", repoDir, out)
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
	out, err := gitRead(t, repoDir, "rev-parse", "HEAD")
	if err != nil {
		t.Errorf("S29: git rev-parse HEAD in %s: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(out)
}

// gitStatusPorcelain returns the output of git status --porcelain in a repo dir.
func gitStatusPorcelain(t *testing.T, repoDir string) string {
	t.Helper()
	out, err := gitRead(t, repoDir, "status", "--porcelain")
	if err != nil {
		t.Errorf("S29: git status --porcelain in %s: %v", repoDir, err)
		return ""
	}
	return strings.TrimSpace(out)
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
		branchOut, err := gitRead(t, cloneDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("S30: %s cannot determine current branch: %v", repo, err)
			continue
		}
		branch := strings.TrimSpace(branchOut)

		// 1. Bare remote's ref for this branch must match workspace clone HEAD.
		// (bare remote HEAD points to default branch; we check the feature branch ref)
		bareRef := fmt.Sprintf("refs/heads/%s", branch)
		bareOut, err := gitRead(t, bareDir, "rev-parse", bareRef)
		if err != nil {
			t.Errorf("S30: %s bare remote has no ref %s after push --set-upstream: %v", repo, bareRef, err)
			continue
		}
		bareHead := strings.TrimSpace(bareOut)
		cloneHead := workspaceHead(t, cloneDir)
		if bareHead == "" || cloneHead == "" {
			return
		}
		if bareHead != cloneHead {
			t.Errorf("S30: %s bare remote %s = %s, workspace clone HEAD = %s (push did not advance remote branch)",
				repo, branch, bareHead, cloneHead)
		}

		// 2. The workspace clone's branch must now track origin/<branch>.
		upOut, err := gitRead(t, cloneDir, "rev-parse", "--abbrev-ref", "@{u}")
		if err != nil {
			t.Errorf("S30: %s branch %s has no upstream after push --set-upstream (git rev-parse @{u}: %v)", repo, branch, err)
			continue
		}
		upstream := strings.TrimSpace(upOut)
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
		afterOut, afterErr := gitRead(t, bareDir, "reflog", "--all")
		afterN := 0
		if afterErr == nil {
			afterN = len(strings.Split(strings.TrimSpace(afterOut), "\n"))
			if strings.TrimSpace(afterOut) == "" {
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

// --- S33 extra: worktree update integrated to primary ONLY; no leftover ---

// assertRelockLandedLocallyOnly is the end-to-end form of the ADR 0023 contract,
// against a real bare remote: `pn workspace update` must land the relock on the
// PRIMARY and leave the REMOTE untouched. The three assertions are deliberately
// stronger than "the revs differ" — that alone would also hold if update had
// pushed something else, or had diverged the primary:
//
//  1. the primary's main carries the relock commit (the update did land), and
//  2. the remote's main does NOT carry it (nothing was published), and
//  3. the remote's main is still an ANCESTOR of the primary's main — so the
//     relock is ordinary unpushed landing debt, not a divergence, and a later
//     `pn workspace push` fast-forwards the remote to it.
//
// Before ADR 0023 the flow pushed here, so (2) is the assertion that fails if the
// push ever comes back.
func assertRelockLandedLocallyOnly(t *testing.T, tag, primary, bare string) {
	t.Helper()
	const relockSubject = "update-locks: bump locked.txt"

	primLog, err := gitRead(t, primary, "log", "--oneline", "-n", "5")
	if err != nil {
		t.Fatalf("%s: git log primary: %v", tag, err)
	}
	if !strings.Contains(primLog, relockSubject) {
		t.Errorf("%s: primary main missing relock commit; log:\n%s", tag, primLog)
	}

	bareLog, err := gitRead(t, bare, "log", "--oneline", "-n", "5", "main")
	if err != nil {
		t.Fatalf("%s: git log remote: %v", tag, err)
	}
	if strings.Contains(bareLog, relockSubject) {
		t.Errorf("%s: relock commit reached the REMOTE — update must not push (ADR 0023); remote log:\n%s", tag, bareLog)
	}

	remoteHead, err := gitRead(t, bare, "rev-parse", "main")
	if err != nil {
		t.Fatalf("%s: git rev-parse remote: %v", tag, err)
	}
	if _, err := gitRead(t, primary, "merge-base", "--is-ancestor", strings.TrimSpace(remoteHead), "main"); err != nil {
		t.Errorf("%s: remote main %s is not an ancestor of primary main — the relock should be plain unpushed landing debt: %v",
			tag, strings.TrimSpace(remoteHead), err)
	}
}

func assertS33WorktreeUpdate(t *testing.T, wsRoot string) {
	t.Helper()
	primary := filepath.Join(wsRoot, "solo")
	bare := filepath.Join(wsRoot, "remotes", "solo.git")

	assertRelockLandedLocallyOnly(t, "S33", primary, bare)

	if entries, err := os.ReadDir(filepath.Join(wsRoot, ".workforests", ".pn-update")); err == nil && len(entries) > 0 {
		t.Errorf("S33: .pn-update worktree left behind: %v", entries)
	}
}

// --- S33b extra: dirty primary main fast-forwards via ff-first/autostash ---

// assertS33bWorktreeUpdateDirtyMain verifies the ff-first dirty-main integration:
// the relock lands on the primary main (fast-forwarded) and NOT on the remote
// (update never pushes — ADR 0023), the dirty flake.nix modification survives the
// FIRST ff (no collision → no autostash round-trip is exercised), the stash list
// is empty, and no .pn-update worktree residue is left behind.
func assertS33bWorktreeUpdateDirtyMain(t *testing.T, wsRoot string) {
	t.Helper()
	primary := filepath.Join(wsRoot, "solo")
	bare := filepath.Join(wsRoot, "remotes", "solo.git")

	assertRelockLandedLocallyOnly(t, "S33b", primary, bare)

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
		out, err := gitRead(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("S34: git rev-parse HEAD in %s: %v", repoDir, err)
			continue
		}
		if branch := strings.TrimSpace(out); branch != "feature-x" {
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
	out, err := gitRead(t, extraCanon, "branch", "--list", "feature-x")
	if err != nil {
		t.Errorf("S35: git branch --list feature-x in %s: %v", extraCanon, err)
	} else if strings.TrimSpace(out) == "" {
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
