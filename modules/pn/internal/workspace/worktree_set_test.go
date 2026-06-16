package workspace

// Tests that prove "a worktree set is just a workspace whose root is the set
// dir."  All paths derive from ws.root (filepath.Join(ws.root, repo)), so when
// ws.root == the set directory, every verb automatically operates on set-internal
// paths — no worktree-conditional code required.  These tests make that
// structural claim explicit and regression-proof.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestOverrideInputArgsFor_SetRootedWorkspace proves that when ws.root is a
// worktree-set directory, overrideInputArgsFor emits git+file:// paths that
// are strictly INSIDE the set root — never a canonical-checkout path.
//
// The setup mirrors what worktree add produces: a set dir containing one
// checkout per repo.  The workspace TOML lives inside the set dir and is
// opened with that dir as root; the lock carries a consumer→dep edge.
//
// Key assertion: the emitted git+file:// URL must have setRoot as a prefix
// and must NOT equal (or be a parent of) any path outside setRoot.
func TestOverrideInputArgsFor_SetRootedWorkspace(t *testing.T) {
	setRoot := t.TempDir() // simulates <worktrees_dir>/<branch>
	// Simulate two repos inside the set: "app" (consumer) and "lib" (dep).
	mkRepoDir(t, setRoot, "app")
	mkRepoDir(t, setRoot, "lib")

	// Workspace config lives inside the set root (copied from canonical on
	// worktree add; for this unit test we write it directly).
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"app": {FlakePath: "flake.nix", RemoteURL: "github:owner/app"},
			"lib": {FlakePath: "flake.nix", RemoteURL: "github:owner/lib"},
		},
		Edges: []LockEdge{
			{Consumer: "app", Alias: "lib-input", Target: "lib"},
		},
		Order: []string{"lib", "app"},
	}
	w := openWSWithLock(t, setRoot, `
[workspace]
terminal = "app"

[repos.app]
url = "github:owner/app"

[repos.lib]
url = "github:owner/lib"
`, lock)

	got := w.overrideInputArgsFor("app", overrideOpts{})

	// Must produce exactly one override: --override-input lib-input git+file://<setRoot>/lib
	wantURL := "git+file://" + filepath.Join(setRoot, "lib")
	if len(got) != 3 {
		t.Fatalf("expected 3 args (one override), got %d: %v", len(got), got)
	}
	if got[0] != "--override-input" {
		t.Errorf("got[0] = %q, want --override-input", got[0])
	}
	if got[1] != "lib-input" {
		t.Errorf("got[1] = %q, want lib-input", got[1])
	}
	if got[2] != wantURL {
		t.Errorf("got[2] = %q, want %q", got[2], wantURL)
	}

	// Structural assertion: the override URL must be rooted inside setRoot.
	if !strings.HasPrefix(got[2], "git+file://"+setRoot) {
		t.Errorf("override URL %q does not start with set root %q; override points outside the set", got[2], setRoot)
	}
}

// TestUpdate_SetRootedWorkspace_RewritesSetRevs proves that when Update runs
// against a set-rooted workspace:
//
//  1. All git and update-locks subprocess calls use paths INSIDE the set root
//     (Dir == setRoot/<repo>, -C args == setRoot/<repo>).
//  2. pn-workspace.revs.json is written INSIDE the set root (not a canonical path).
//  3. The rev-lock contains the expected SHA captured from the set-internal repo dir.
//
// This mirrors TestUpdate_PullLocksPushPerRepo but roots the workspace in a
// "set" temp dir to confirm no call escapes the set boundary.
func TestUpdate_SetRootedWorkspace_RewritesSetRevs(t *testing.T) {
	setRoot := t.TempDir() // simulates <worktrees_dir>/<branch>

	writeFile(t, filepath.Join(setRoot, "pn-workspace.toml"), `
[workspace]
terminal = "app"

[repos.app]
url = "github:owner/app"
`)

	f := exec.NewFakeRunner()
	appDir := filepath.Join(setRoot, "app")

	// Standard per-repo Update sequence (no upstream so pull/push are skipped).
	f.AddResponse("git", []string{"-C", appDir, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", appDir, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", appDir, "rev-parse", "--abbrev-ref", "@{u}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	sha := "setabc00000000000000000000000000000000000"
	f.AddResponse("git", []string{"-C", appDir, "rev-parse", "HEAD"},
		exec.Result{Stdout: []byte(sha + "\n")}, nil)

	w, err := Open(setRoot, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// 1. Confirm all -C args reference setRoot, not any other path.
	for _, c := range f.Calls() {
		for i, a := range c.Args {
			if a == "-C" && i+1 < len(c.Args) {
				dir := c.Args[i+1]
				if !strings.HasPrefix(dir, setRoot) {
					t.Errorf("git -C points outside set root: %q (setRoot=%q)", dir, setRoot)
				}
			}
		}
		// update-locks.sh Dir must also be inside setRoot.
		if c.Name == "./update-locks.sh" {
			if !strings.HasPrefix(c.Opts.Dir, setRoot) {
				t.Errorf("update-locks.sh Dir %q is outside set root %q", c.Opts.Dir, setRoot)
			}
		}
	}

	// 2. pn-workspace.revs.json written INSIDE the set root.
	revsPath := filepath.Join(setRoot, RevLockFileName)
	lockBytes, err := os.ReadFile(revsPath)
	if err != nil {
		t.Fatalf("expected %s inside set root, but read failed: %v", RevLockFileName, err)
	}

	// 3. Rev-lock must carry the set-internal SHA.
	var revLock RevLock
	if err := json.Unmarshal(lockBytes, &revLock); err != nil {
		t.Fatalf("parse %s: %v", RevLockFileName, err)
	}
	if revLock.Repos["app"].Rev != sha {
		t.Errorf("revs.json rev = %q, want %q", revLock.Repos["app"].Rev, sha)
	}
}

// TestStatus_SetRootedWorkspace_ResolvesSetInternalPaths proves that Status
// resolves each repo dir as filepath.Join(setRoot, repo), i.e. the git -C arg
// points into the set root for every repo.
//
// This is the foundational status path richer per-repo reporting (pg2-sc4h)
// will build on; here we only assert that the set-internal directory is
// addressed — consistent with TestStatus_WritesPerRepoSections but with
// setRoot as the workspace root.
func TestStatus_SetRootedWorkspace_ResolvesSetInternalPaths(t *testing.T) {
	setRoot := t.TempDir() // simulates <worktrees_dir>/<branch>

	writeFile(t, filepath.Join(setRoot, "pn-workspace.toml"), `
[repos.alpha]
url = "github:owner/alpha"

[repos.beta]
url = "github:owner/beta"
`)

	f := exec.NewFakeRunner()
	alphaDir := filepath.Join(setRoot, "alpha")
	betaDir := filepath.Join(setRoot, "beta")
	f.AddResponse("git", []string{"-C", alphaDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", betaDir, "status", "--short"}, exec.Result{Stdout: []byte(" M worktree-file.go\n")}, nil)

	w, err := Open(setRoot, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "alpha"}); err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Confirm the FakeRunner saw exactly the set-internal -C paths.
	sawAlpha, sawBeta := false, false
	for _, c := range f.Calls() {
		for i, a := range c.Args {
			if a == "-C" && i+1 < len(c.Args) {
				switch c.Args[i+1] {
				case alphaDir:
					sawAlpha = true
				case betaDir:
					sawBeta = true
				default:
					t.Errorf("unexpected -C path %q (not inside set root %q)", c.Args[i+1], setRoot)
				}
			}
		}
	}
	if !sawAlpha {
		t.Errorf("Status did not address alpha at %q", alphaDir)
	}
	if !sawBeta {
		t.Errorf("Status did not address beta at %q", betaDir)
	}

	// Output sanity: repo sections are present.
	out := buf.String()
	if !strings.Contains(out, "=== alpha ===") {
		t.Errorf("missing alpha section in status output:\n%s", out)
	}
	if !strings.Contains(out, "=== beta ===") {
		t.Errorf("missing beta section in status output:\n%s", out)
	}
}
