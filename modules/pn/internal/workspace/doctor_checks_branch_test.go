// internal/workspace/doctor_checks_branch_test.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckBranches_WrongBranchIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}},
	}
	env := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	fs := ws.checkBranches(context.Background(), env)
	if !hasFindingForRepo(fs, "branch-current", "dep", SevError) {
		t.Fatalf("wrong branch should be error: %+v", fs)
	}
}

func TestCheckBranches_DirtyIsErrorPrimaryWarningWorktree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	dirtyTrackedFile(t, dir, "README.md", "changed\n")
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}
	ws := &Workspace{root: root, runner: exec.NewRealRunner(), config: cfg}

	envP := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envP), "tree-clean", "dep", SevError) {
		t.Fatal("dirty primary should be error")
	}
	envW := &doctorEnv{ws: ws, mode: "worktree", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envW), "tree-clean", "dep", SevWarning) {
		t.Fatal("dirty worktree should be warning")
	}
}

func TestCheckBranches_AheadOfRemoteIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}},
	}
	// refRev (remote) differs from local HEAD => not synced.
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev:  map[string]string{"dep": "0000000000000000000000000000000000000000"},
		skipped: map[string]bool{},
	}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), env), "branch-synced", "dep", SevError) {
		t.Fatal("local != remote should be branch-synced error")
	}
}

func TestCheckBranches_AheadOnlyIsFixableByPush(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir)                       // origin = bare; main pushed; origin/main tracks it
	h0 := headRev(t, dir)                                      // the remote HEAD (what was pushed)
	want := addCommit(t, dir, "ahead.txt", "x", "local ahead") // local now ahead, behind 0

	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}},
	}
	// refRev = remote HEAD (h0); local HEAD (want) is strictly ahead of it.
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev: map[string]string{"dep": h0}, skipped: map[string]bool{},
	}

	fs := ws.checkBranches(context.Background(), env)
	var bs *Finding
	for i := range fs {
		if fs[i].CheckID == "branch-synced" && fs[i].Repo == "dep" {
			bs = &fs[i]
		}
	}
	if bs == nil || bs.Severity != SevError {
		t.Fatalf("expected branch-synced error for ahead-only: %+v", fs)
	}
	if !bs.Fixable || bs.fix == nil {
		t.Fatalf("ahead-only branch-synced should be fixable via push: %+v", *bs)
	}
	if !strings.Contains(bs.Manual, "push") {
		t.Fatalf("ahead-only manual hint should mention push, got %q", bs.Manual)
	}
	// Apply the fix: it should fast-forward-push local HEAD to the remote.
	if err := bs.fix(context.Background()); err != nil {
		t.Fatalf("push fix failed: %v", err)
	}
	remoteHead := strings.Fields(runGitT(t, dir, "ls-remote", bare, "refs/heads/main"))[0]
	if remoteHead != want {
		t.Fatalf("push fix did not advance remote: want %s got %s", want, remoteHead)
	}
}

// findBranchSynced returns the branch-synced finding for repo, or nil.
func findBranchSynced(fs []Finding, repo string) *Finding {
	for i := range fs {
		if fs[i].CheckID == "branch-synced" && fs[i].Repo == repo {
			return &fs[i]
		}
	}
	return nil
}

// Item 1: the repo's canonical remote is NOT "origin". An ahead-only repo must
// still be classified ahead-only and fixable via push, with a manual hint that
// names the resolved remote — not a hardcoded, nonexistent "origin".
func TestCheckBranches_AheadOnlyHonorsNonOriginRemote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemoteNamed(t, dir, "gitea") // canonical remote is "gitea", not "origin"
	h0 := headRev(t, dir)                              // remote HEAD
	want := addCommit(t, dir, "ahead.txt", "x", "local ahead")

	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}},
	}
	// refRev = remote HEAD (h0); local (want) is strictly ahead of it.
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev: map[string]string{"dep": h0}, skipped: map[string]bool{},
	}

	fs := ws.checkBranches(context.Background(), env)
	bs := findBranchSynced(fs, "dep")
	if bs == nil || bs.Severity != SevError {
		t.Fatalf("expected branch-synced error for ahead-only (non-origin): %+v", fs)
	}
	if !bs.Fixable || bs.fix == nil {
		t.Fatalf("ahead-only (non-origin) should be fixable via push, got diverged/manual: %+v", *bs)
	}
	if strings.Contains(bs.Manual, "origin") {
		t.Fatalf("manual hint must name the resolved remote, not hardcoded origin: %q", bs.Manual)
	}
	if !strings.Contains(bs.Manual, "gitea") {
		t.Fatalf("manual hint should reference the resolved remote 'gitea': %q", bs.Manual)
	}
	// Apply the fix: it must push to the resolved remote (gitea), advancing it.
	if err := bs.fix(context.Background()); err != nil {
		t.Fatalf("push fix (non-origin) failed: %v", err)
	}
	remoteHead := strings.Fields(runGitT(t, dir, "ls-remote", bare, "refs/heads/main"))[0]
	if remoteHead != want {
		t.Fatalf("push fix did not advance non-origin remote: want %s got %s", want, remoteHead)
	}
}

// Item 2: two-source classification. The finding triggers on ref=lsRemoteHead
// (live), but classification must use that SAME ref SHA — not a stale local
// remote-tracking ref. Simulate a stale tracking ref: origin/main still points
// at the old remote HEAD, while ref (the live ls-remote SHA) equals the old
// remote HEAD too, and local is genuinely ahead. Classifying against ref keeps
// it correctly ahead-only. (Regression guard: the classification must not read
// origin/<branch>, which a failed/stale fetch could leave inconsistent.)
func TestCheckBranches_ClassifiesAgainstTriggerRef(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir) // origin/main = h0
	h0 := headRev(t, dir)
	_ = addCommit(t, dir, "ahead.txt", "x", "local ahead") // local ahead of h0

	// Delete the local remote-tracking ref to simulate a failed/absent fetch.
	// Classification against a stale/missing origin/main would mis-fire; against
	// ref (h0, whose object is local since it is an ancestor of HEAD) it is correct.
	runGitT(t, dir, "update-ref", "-d", "refs/remotes/origin/main")

	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}},
	}
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev: map[string]string{"dep": h0}, skipped: map[string]bool{},
	}

	fs := ws.checkBranches(context.Background(), env)
	bs := findBranchSynced(fs, "dep")
	if bs == nil || bs.Severity != SevError {
		t.Fatalf("expected branch-synced error: %+v", fs)
	}
	if !bs.Fixable || bs.fix == nil {
		t.Fatalf("ahead-only must classify against the trigger ref (h0), staying fixable even with a missing origin/main tracking ref: %+v", *bs)
	}
}

// Item 3(a): a genuinely diverged repo (ahead AND behind) must stay Fixable=false
// with a rebase hint. Guards against a regression that adds --force or collapses
// the switch so a diverged repo is treated as ahead-only (fast-forward push).
func TestCheckBranches_DivergedStaysManualRebase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir)

	// Advance the remote on a separate clone so origin/main diverges.
	other := filepath.Join(root, "other")
	runGitT(t, root, "clone", "-q", bare, other)
	remoteAdvanced := addCommit(t, other, "remote.txt", "r", "remote-only commit")
	runGitT(t, other, "push", "-q", "origin", "main")

	// Advance local on a DIFFERENT commit so HEAD and origin/main diverge.
	addCommit(t, dir, "local.txt", "l", "local-only commit")
	// Make the local remote-tracking ref reflect the advanced remote.
	runGitT(t, dir, "fetch", "-q", "origin", "main")

	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}},
	}
	// ref = the (diverged) remote HEAD.
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev: map[string]string{"dep": remoteAdvanced}, skipped: map[string]bool{},
	}

	fs := ws.checkBranches(context.Background(), env)
	bs := findBranchSynced(fs, "dep")
	if bs == nil || bs.Severity != SevError {
		t.Fatalf("expected branch-synced error for diverged: %+v", fs)
	}
	if bs.Fixable || bs.fix != nil {
		t.Fatalf("diverged repo must NOT be auto-fixable: %+v", *bs)
	}
	if !strings.Contains(bs.Manual, "rebase") {
		t.Fatalf("diverged manual hint should mention rebase: %q", bs.Manual)
	}
}

// Item 3(b): a rejected (non-fast-forward) push must surface as a fix-failed
// SevError — never a forced overwrite. Ahead-only classification (against a
// stale ref) can still lead the fix to attempt a push the server rejects; the
// safety property is that pushBranch returns an error (no --force).
func TestCheckBranches_RejectedPushSurfacesAsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir)
	h0 := headRev(t, dir)

	// Local advances (looks ahead-only against the stale ref h0)...
	addCommit(t, dir, "local.txt", "l", "local-only commit")

	// ...but the remote ALSO advances on an incompatible line via another clone,
	// so a plain push is a non-fast-forward and must be rejected.
	other := filepath.Join(root, "other")
	runGitT(t, root, "clone", "-q", bare, other)
	addCommit(t, other, "remote.txt", "r", "remote-only commit")
	runGitT(t, other, "push", "-q", "origin", "main")

	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}},
	}
	// ref = the stale h0 so the classification lands on ahead-only and the fix
	// attempts a push (which the server rejects).
	env := &doctorEnv{
		ws: ws, mode: "primary",
		refRev: map[string]string{"dep": h0}, skipped: map[string]bool{},
	}

	fs := ws.checkBranches(context.Background(), env)
	bs := findBranchSynced(fs, "dep")
	if bs == nil {
		t.Fatalf("expected branch-synced finding: %+v", fs)
	}
	if bs.fix == nil {
		t.Skip("ahead-only classification produced no fix; nothing to reject")
	}
	// The push MUST be rejected (non-ff) and returned as an error — not forced.
	if err := bs.fix(context.Background()); err == nil {
		t.Fatalf("rejected non-ff push must return an error (no --force); got nil")
	}
	// The remote must NOT have been overwritten.
	remoteHead := strings.Fields(runGitT(t, dir, "ls-remote", bare, "refs/heads/main"))[0]
	if remoteHead == headRev(t, dir) {
		t.Fatalf("remote was overwritten by a forced push: %s", remoteHead)
	}
}
