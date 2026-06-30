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

// makeThreeRepoWorkspace sets up a temp workspace with three repos (app, lib,
// other) and a lock with an app→lib edge. Returns root + fake runner.
func makeThreeRepoWorkspace(t *testing.T) (root string, f *exec.FakeRunner) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[workspace]
terminal = "app"

[repos.app]
url = "github:owner/app"

[repos.lib]
url = "github:owner/lib"

[repos.other]
url = "github:owner/other"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "terminal": "app",
  "order": ["lib","other","app"],
  "repos": {
    "app": {"remote_url": "github:owner/app", "flake_path": "flake.nix"},
    "lib": {"remote_url": "github:owner/lib", "flake_path": "flake.nix"},
    "other": {"remote_url": "github:owner/other", "flake_path": "flake.nix"}
  },
  "edges": [
    {"consumer": "app", "alias": "lib-input", "target": "lib"}
  ]
}`)
	f = exec.NewFakeRunner()
	return root, f
}

// ============================================================
// filterLock — restricts order/repos/edges to a member set
// ============================================================

func TestFilterLock_RestrictsToMembers(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	members := map[string]bool{"app": true, "lib": true}
	got := filterLock(w.Lock(), members)

	// Order keeps only members, preserving relative order.
	if want := []string{"lib", "app"}; !strSliceEqual(got.Order, want) {
		t.Errorf("order = %v, want %v", got.Order, want)
	}
	// Repos restricted to members.
	if len(got.Repos) != 2 || got.Repos["other"].RemoteURL != "" {
		t.Errorf("repos should only contain app+lib, got %v", got.Repos)
	}
	// Edge app→lib is fully inside the member set → kept.
	if len(got.Edges) != 1 || got.Edges[0].Target != "lib" {
		t.Errorf("app→lib edge should be kept, got %v", got.Edges)
	}
	// Terminal "app" is a member → kept.
	if got.Terminal != "app" {
		t.Errorf("terminal = %q, want app", got.Terminal)
	}
}

func TestFilterLock_DropsEdgesToExcludedTargets(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Member set excludes "lib" (the target of app's only edge).
	members := map[string]bool{"app": true, "other": true}
	got := filterLock(w.Lock(), members)

	// The app→lib edge must be dropped (lib excluded).
	if len(got.Edges) != 0 {
		t.Errorf("app→lib edge should be dropped when lib excluded, got %v", got.Edges)
	}
	if _, ok := got.Repos["lib"]; ok {
		t.Errorf("lib must not be in filtered repos")
	}
}

func TestFilterLock_ClearsTerminalWhenExcluded(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Member set excludes the terminal "app".
	members := map[string]bool{"lib": true, "other": true}
	got := filterLock(w.Lock(), members)

	if got.Terminal != "" {
		t.Errorf("terminal should be cleared when excluded from members, got %q", got.Terminal)
	}
}

// ============================================================
// filterConfig — restricts repos to a member set
// ============================================================

func TestFilterConfig_RestrictsReposAndTerminal(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	members := map[string]bool{"lib": true, "other": true}
	got := filterConfig(w.Config(), members)

	if len(got.Repos) != 2 {
		t.Errorf("filtered config should have 2 repos, got %d", len(got.Repos))
	}
	if _, ok := got.Repos["app"]; ok {
		t.Errorf("app must be excluded from filtered config")
	}
	// Terminal "app" excluded → must be cleared so the subset config is valid.
	if got.Workspace.Terminal != "" {
		t.Errorf("terminal should be cleared when excluded, got %q", got.Workspace.Terminal)
	}
}

// ============================================================
// memberRepos — selection + validation
// ============================================================

func TestMemberRepos_EmptyRequestReturnsAllInOrder(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := w.memberRepos(context.Background(), nil)
	if err != nil {
		t.Fatalf("memberRepos: %v", err)
	}
	if want := []string{"lib", "other", "app"}; !strSliceEqual(got, want) {
		t.Errorf("all-repos order = %v, want %v (topo)", got, want)
	}
}

func TestMemberRepos_SubsetReturnsFilteredInTopoOrder(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Request app + lib (out of order); expect topo order lib,app.
	got, err := w.memberRepos(context.Background(), []string{"app", "lib"})
	if err != nil {
		t.Fatalf("memberRepos: %v", err)
	}
	if want := []string{"lib", "app"}; !strSliceEqual(got, want) {
		t.Errorf("subset order = %v, want %v", got, want)
	}
}

func TestMemberRepos_UnknownRepoErrors(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = w.memberRepos(context.Background(), []string{"app", "ghost"})
	if err == nil {
		t.Fatal("expected error for unknown repo, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name unknown repo 'ghost'; got: %v", err)
	}
}

// ============================================================
// WorkforestAdd — subset create
// ============================================================

func TestWorkforestAdd_Subset_CreatesOnlyChosenRepos(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	appCanonical := filepath.Join(root, "app")
	libCanonical := filepath.Join(root, "lib")
	setDir := filepath.Join(w.WorkforestsDir(), "feature")
	appSet := filepath.Join(setDir, "app")
	libSet := filepath.Join(setDir, "lib")

	// Only app + lib are members; "other" must not be touched.
	addWorktreeListClean(f, libCanonical, "lib")
	addWorktreeListClean(f, appCanonical, "app")
	addBranchNotExists(f, libCanonical, "feature")
	addBranchNotExists(f, appCanonical, "feature")
	f.AddResponse("git", []string{"-C", libCanonical, "worktree", "add", "-b", "feature", libSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", appCanonical, "worktree", "add", "-b", "feature", appSet}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature", Repos: []string{"app", "lib"}}); err != nil {
		t.Fatalf("WorkforestAdd subset: %v", err)
	}

	// "other" must have NO git calls.
	otherCanonical := filepath.Join(root, "other")
	for _, c := range f.Calls() {
		for i, a := range c.Args {
			if a == "-C" && i+1 < len(c.Args) && c.Args[i+1] == otherCanonical {
				t.Errorf("excluded repo 'other' should not be touched; got call: %v", c.Args)
			}
		}
	}

	// The set's pn-workspace.toml must list ONLY app + lib.
	setCfgData, err := os.ReadFile(filepath.Join(setDir, ConfigFileName))
	if err != nil {
		t.Fatalf("read set config: %v", err)
	}
	setCfg, err := ParseConfig(setCfgData)
	if err != nil {
		t.Fatalf("parse set config: %v", err)
	}
	if len(setCfg.Repos) != 2 {
		t.Errorf("set config should list 2 repos, got %d: %v", len(setCfg.Repos), setCfg.Repos)
	}
	if _, ok := setCfg.Repos["other"]; ok {
		t.Errorf("set config must not list excluded repo 'other'")
	}

	// Canonical pn-workspace.toml must be UNCHANGED (still 3 repos).
	canonData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatalf("read canonical config: %v", err)
	}
	canonCfg, err := ParseConfig(canonData)
	if err != nil {
		t.Fatalf("parse canonical config: %v", err)
	}
	if len(canonCfg.Repos) != 3 {
		t.Errorf("canonical config must be unchanged (3 repos), got %d", len(canonCfg.Repos))
	}

	// The set's lock must validate (Open the set as a workspace).
	setW, err := Open(setDir, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open set as workspace: %v (subset lock invalid?)", err)
	}
	if !lockMatchesConfig(setW.Lock(), setW.Config()) {
		t.Errorf("subset set's lock does not match its config")
	}
}

// ============================================================
// WorkforestAdd — subset where a member's dep is excluded → notice
// ============================================================

func TestWorkforestAdd_Subset_ExcludedDepNotice(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	appCanonical := filepath.Join(root, "app")
	otherCanonical := filepath.Join(root, "other")
	setDir := filepath.Join(w.WorkforestsDir(), "feature")
	appSet := filepath.Join(setDir, "app")
	otherSet := filepath.Join(setDir, "other")

	// Members: app + other (excludes lib, which app depends on).
	addWorktreeListClean(f, appCanonical, "app")
	addWorktreeListClean(f, otherCanonical, "other")
	addBranchNotExists(f, appCanonical, "feature")
	addBranchNotExists(f, otherCanonical, "feature")
	f.AddResponse("git", []string{"-C", appCanonical, "worktree", "add", "-b", "feature", appSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", otherCanonical, "worktree", "add", "-b", "feature", otherSet}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature", Repos: []string{"app", "other"}}); err != nil {
		t.Fatalf("WorkforestAdd subset w/ excluded dep: %v", err)
	}

	// A notice must name the consumer (app) and the excluded dep (lib).
	notice := errOut.String()
	if !strings.Contains(notice, "app") || !strings.Contains(notice, "lib") {
		t.Errorf("expected excluded-dep notice naming app→lib; got stderr: %q", notice)
	}

	// The set's lock must NOT carry the app→lib edge.
	setW, err := Open(setDir, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open set: %v", err)
	}
	for _, e := range setW.Lock().Edges {
		if e.Target == "lib" {
			t.Errorf("set lock must not carry edge to excluded dep 'lib'; got %v", e)
		}
	}
}

// strSliceEqual reports whether two string slices are equal.
func strSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
