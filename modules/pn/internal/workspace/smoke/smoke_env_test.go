//go:build smoke

package smoke

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestSmoke_ScrubbedEnvStripsGitDirFamilyAfterTestMain proves the per-call
// strip in buildScrubbedEnv (pg2-9ctm4, moved there by pg2-wsvch): a GIT_DIR
// family variable set with t.Setenv AFTER TestMain (which only unsets what was
// present before the binary started) does not reach a scenario's setup.sh.
//
// The 44 scenarios/*/setup.sh scripts are exempt from the hermetic git fixture
// rule (they run bare `git init`/`git clone`), so this strip is the only thing
// keeping them off the ambient repository. The script below runs a real
// `git init`, the exact operation that wrote core.worktree into a canonical
// clone's .git/config in pg2-7byxb, and the decoy repository stands in for that
// clone: its .git/config and HEAD must be byte-identical afterward.
func TestSmoke_ScrubbedEnvStripsGitDirFamilyAfterTestMain(t *testing.T) {
	decoy := gittest.New(t, gitfixture.RepoOptions{Suite: "smoke-env-decoy", InitialCommit: true})
	decoyGitDir := filepath.Join(decoy.Dir, ".git")
	configBefore := readFileString(t, filepath.Join(decoyGitDir, "config"))
	headBefore := readFileString(t, filepath.Join(decoyGitDir, "HEAD"))

	// Reintroduce every member of the family, pointing at the decoy, after
	// TestMain has already run its process-wide unset.
	for _, k := range gitDirEnvVars {
		v := decoyGitDir
		if k == "GIT_WORK_TREE" {
			v = decoy.Dir
		}
		t.Setenv(k, v)
	}

	wsRoot := t.TempDir()
	env := buildScrubbedEnv(t, wsRoot)

	scriptPath := filepath.Join(t.TempDir(), "setup.sh")
	script := "#!/bin/bash\nset -euo pipefail\n" +
		"env | grep '^GIT_' | sort > git-env.txt\n" +
		"git init -q -b main stray\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write setup.sh: %v", err)
	}
	if err := runSetupScript(t, scriptPath, wsRoot, env); err != nil {
		t.Fatal(err)
	}

	gitEnv := readFileString(t, filepath.Join(wsRoot, "git-env.txt"))
	if !strings.Contains(gitEnv, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("setup.sh did not see the scrubbed env (GIT_CONFIG_GLOBAL missing); got:\n%s", gitEnv)
	}
	for _, k := range gitDirEnvVars {
		if strings.Contains("\n"+gitEnv, "\n"+k+"=") {
			t.Errorf("setup.sh inherited %s set via t.Setenv after TestMain; env:\n%s", k, gitEnv)
		}
	}

	if _, err := os.Stat(filepath.Join(wsRoot, "stray", ".git")); err != nil {
		t.Errorf("setup.sh's git init did not create wsRoot/stray/.git (it ran against another repo): %v", err)
	}
	if got := readFileString(t, filepath.Join(decoyGitDir, "config")); got != configBefore {
		t.Errorf("decoy .git/config changed:\nbefore:\n%s\nafter:\n%s", configBefore, got)
	}
	if got := readFileString(t, filepath.Join(decoyGitDir, "HEAD")); got != headBefore {
		t.Errorf("decoy HEAD changed: %q -> %q", headBefore, got)
	}
}

// TestSmoke_ScrubbedEnvIdentityReachesGitclientCommits proves the identity
// buildScrubbedEnv writes to $HOME/.gitconfig reaches a commit made through
// x/gitclient, whose child environment passes only PATH, HOME and
// SSH_AUTH_SOCK. The clone has no local identity (a gittest repo carries one),
// so only the throwaway home can supply it.
func TestSmoke_ScrubbedEnvIdentityReachesGitclientCommits(t *testing.T) {
	ctx := context.Background()
	src := gittest.New(t, gitfixture.RepoOptions{Suite: "smoke-env-identity", InitialCommit: true})

	env := buildScrubbedEnv(t, t.TempDir())
	home := envValue(env, "HOME")

	dir := filepath.Join(t.TempDir(), "clone")
	client, h, err := gitclient.Clone(ctx, src.Dir, dir, gitclient.CloneOptions{}, gitclient.WithHome(home))
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("clone wait: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.Add(ctx, "x.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	ch, err := client.Commit(ctx, "chore: identity probe")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := ch.Wait(); err != nil {
		t.Fatalf("commit failed (identity did not reach gitclient): %v", err)
	}
	commits, err := client.Commits(ctx, gitclient.LogOptions{Limit: 1})
	if err != nil || len(commits) != 1 {
		t.Fatalf("log: %v, %d commits", err, len(commits))
	}
	if got := commits[0].Author.Email; got != "pn-smoke@test.invalid" {
		t.Errorf("author email = %q, want pn-smoke@test.invalid", got)
	}
}
