// internal/workspace/doctor_checks_extraremotes_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

// extraRemotesWorkspace builds a one-repo workspace whose repo is a real git
// repo with a real local bare "origin" remote (the TOML-declared one). It
// uses the real git runner throughout -- per this bead's constraint, these
// tests must never `git ls-remote` a real external host, so every "remote" is
// a local bare repo (or a deliberately-bogus local path for the unresolvable
// case), matching the pattern doctor_refrev_test.go already established with
// setupLocalBareRemote/setupLocalBareRemoteNamed.
func extraRemotesWorkspace(t *testing.T) (ws *Workspace, dir, originURL string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "apps")
	initRealRepo(t, dir)
	originURL = setupLocalBareRemote(t, dir)
	ws = newWS(t, root, map[string]RepoConfig{
		"apps": {URL: originURL, Branch: "main"},
	})
	return ws, dir, originURL
}

// TestCheckExtraRemotesSynced_OnlyTomlRemoteProducesNoFindings covers the
// unchanged-from-today baseline this bead must not regress: a repo whose only
// git remote is the one pn-workspace.toml declares produces nothing -- the
// same silence branch-synced already gives it, since that remote is excluded
// from "extra" by construction (it canonicalURL-matches the declared URL).
func TestCheckExtraRemotesSynced_OnlyTomlRemoteProducesNoFindings(t *testing.T) {
	ws, _, _ := extraRemotesWorkspace(t)

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a repo with only the TOML-declared remote must produce no finding; got %+v", fs)
	}
}

// TestCheckExtraRemotesSynced_ExtraRemoteInSyncProducesNoFinding covers the
// positive case for the new visibility this bead adds: an extra (non-TOML)
// remote whose HEAD matches local HEAD is genuinely fine and must not be
// flagged.
func TestCheckExtraRemotesSynced_ExtraRemoteInSyncProducesNoFinding(t *testing.T) {
	ws, dir, _ := extraRemotesWorkspace(t)
	setupLocalBareRemoteNamed(t, dir, "synfra") // pushes current HEAD to synfra too

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("an extra remote already in sync must produce no finding; got %+v", fs)
	}
}

// TestCheckExtraRemotesSynced_ExtraRemoteBehindIsSkippedFindingNamingIt is the
// bead's core live scenario: an extra remote (e.g. a dedicated synfra remote,
// or a bitbucket mirror) that has fallen behind local HEAD because nothing
// pushes to it via pn's own resolved push remote. Must surface a Skipped
// (never hard-failing) finding that names the remote.
func TestCheckExtraRemotesSynced_ExtraRemoteBehindIsSkippedFindingNamingIt(t *testing.T) {
	ws, dir, _ := extraRemotesWorkspace(t)
	setupLocalBareRemoteNamed(t, dir, "synfra") // in sync at this point
	addCommit(t, dir, "extra.txt", "more\n", "advance past synfra")
	// synfra deliberately NOT pushed to again -- it is now behind local HEAD.

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "extra-remotes-synced", "apps") {
		t.Fatalf("expected a Skipped extra-remotes-synced finding for a behind extra remote; got %+v", fs)
	}
	f := findingByID(t, fs, "extra-remotes-synced")
	if !contains([]byte(f.Message), "synfra") {
		t.Errorf("message should name the behind remote %q: %q", "synfra", f.Message)
	}
}

// TestCheckExtraRemotesSynced_UnresolvableExtraRemoteIsSkippedFindingNoCrash
// covers a remote whose URL cannot be resolved at all (e.g. a stale/
// unreachable mirror) -- must degrade to a Skipped finding, not a crash or a
// false "in sync".
func TestCheckExtraRemotesSynced_UnresolvableExtraRemoteIsSkippedFindingNoCrash(t *testing.T) {
	ws, dir, _ := extraRemotesWorkspace(t)
	runGitT(t, dir, "remote", "add", "bitbucket", filepath.Join(t.TempDir(), "does-not-exist.git"))

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "extra-remotes-synced", "apps") {
		t.Fatalf("expected a Skipped extra-remotes-synced finding for an unresolvable extra remote; got %+v", fs)
	}
	f := findingByID(t, fs, "extra-remotes-synced")
	if !contains([]byte(f.Message), "bitbucket") {
		t.Errorf("message should name the unresolvable remote %q: %q", "bitbucket", f.Message)
	}
	if !contains([]byte(f.Message), "could not be resolved") {
		t.Errorf("message should say the remote could not be resolved: %q", f.Message)
	}
}

// TestCheckExtraRemotesSynced_OfflineSkipsEntirely mirrors resolveRefRevs'
// own offline behavior: no network call (not even a local-path ls-remote) is
// attempted offline, so a behind extra remote produces nothing while offline.
func TestCheckExtraRemotesSynced_OfflineSkipsEntirely(t *testing.T) {
	ws, dir, _ := extraRemotesWorkspace(t)
	setupLocalBareRemoteNamed(t, dir, "synfra")
	addCommit(t, dir, "extra.txt", "more\n", "advance past synfra")

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "primary", offline: true})
	if len(fs) != 0 {
		t.Fatalf("offline must skip this check entirely; got %+v", fs)
	}
}

// TestCheckExtraRemotesSynced_WorktreeModeSkipsEntirely mirrors branch-synced
// itself, which checkBranches' doc comment says is "dropped" in worktree
// mode -- this check follows the same primary-only applicability.
func TestCheckExtraRemotesSynced_WorktreeModeSkipsEntirely(t *testing.T) {
	ws, dir, _ := extraRemotesWorkspace(t)
	setupLocalBareRemoteNamed(t, dir, "synfra")
	addCommit(t, dir, "extra.txt", "more\n", "advance past synfra")

	fs := ws.checkExtraRemotesSynced(context.Background(), &doctorEnv{ws: ws, mode: "worktree"})
	if len(fs) != 0 {
		t.Fatalf("worktree mode must skip this check entirely; got %+v", fs)
	}
}
