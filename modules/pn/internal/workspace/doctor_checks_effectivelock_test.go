// internal/workspace/doctor_checks_effectivelock_test.go
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestEffectiveLockDerivationFinding_ShapeAndMessage pins the Finding shape
// this bd tc-b3wxl helper constructs directly against an error, independent
// of how Doctor() wires it in.
func TestEffectiveLockDerivationFinding_ShapeAndMessage(t *testing.T) {
	f := effectiveLockDerivationFinding(errors.New("duplicate_remote_url: repos \"aaa\" and \"bbb\" both resolve to \"same\""))

	if f.CheckID != "effective-lock-derivable" {
		t.Errorf("CheckID = %q, want effective-lock-derivable", f.CheckID)
	}
	if f.Repo != "" {
		t.Errorf("Repo = %q, want \"\" (workspace-level finding)", f.Repo)
	}
	if f.Severity != SevError {
		t.Errorf("Severity = %v, want SevError", f.Severity)
	}
	if !f.Skipped {
		t.Error("Skipped must be true, per the tc-atsmj GAP 3 convention: visible, never a hard failure")
	}
	if !contains([]byte(f.Message), "duplicate_remote_url") {
		t.Errorf("message must surface the underlying error verbatim; got %q", f.Message)
	}
}

// TestDoctor_EffectiveLockDerivationFailureIsSurfaced covers the end-to-end
// wiring (bd tc-b3wxl): two repos declaring the same remote URL make
// buildEdges reject with duplicate_remote_url, so ws.effectiveLock(ctx)
// returns a non-nil error. Doctor() must surface an explicit
// effective-lock-derivable Finding for that, distinct from (and in addition
// to) whatever downstream checks then do with a nil/zero-value lock.
func TestDoctor_EffectiveLockDerivationFailureIsSurfaced(t *testing.T) {
	root := t.TempDir()
	aaa := filepath.Join(root, "aaa")
	bbb := filepath.Join(root, "bbb")
	initRealRepo(t, aaa)
	initRealRepo(t, bbb)
	bare := setupLocalBareRemote(t, aaa)
	// bbb points at the SAME bare remote as aaa -> duplicate_remote_url.
	//
	// bbb is an INDEPENDENT initRealRepo, not a clone of aaa/bare, so its
	// "init" commit shares no ancestry with the one aaa already pushed to
	// bare's main. A plain push is only a fast-forward when the two commits
	// happen to hash identically (same-second author/committer timestamp,
	// same tree/message) -- true on a fast, idle machine but NOT guaranteed,
	// and increasingly unlikely to hold under CPU load, since a slower
	// initRealRepo(bbb) is more likely to straddle a wall-clock second
	// boundary from aaa's commit. When it doesn't hold, git rejects the push
	// as non-fast-forward ("[rejected] main -> main (fetch first)") -- a
	// deterministic outcome of the hash mismatch, not a transient lock
	// contention, so retrying the same push would not help (bd pg2-z1l3a:
	// reproduced 7/60 failures under artificial load, all this exact
	// rejection). --force sidesteps the coincidence entirely: the test only
	// needs bbb's config-level URL to match aaa's (duplicate_remote_url is
	// derived purely from WorkspaceConfig.Repos[*].URL, never from git ref
	// state), so which commit ends up on bare's main is irrelevant here.
	runGitT(t, bbb, "remote", "add", "origin", bare)
	runGitT(t, bbb, "push", "-qf", "origin", currentBranch(t, bbb))

	cfg := &WorkspaceConfig{
		Repos: map[string]RepoConfig{
			"aaa": {URL: bare, Branch: "main"},
			"bbb": {URL: bare, Branch: "main"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Doctor(context.Background(), root, exec.NewRealRunner(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}

	found := findingByID(t, r.Findings, "effective-lock-derivable")
	if !found.Skipped {
		t.Errorf("effective-lock-derivable must be Skipped: true; got %+v", found)
	}
	if found.Severity != SevError {
		t.Errorf("effective-lock-derivable severity = %v, want SevError", found.Severity)
	}
	if !contains([]byte(found.Message), "duplicate_remote_url") {
		t.Errorf("message must name the underlying derivation error; got %q", found.Message)
	}
}

// TestDoctor_EffectiveLockDerivationSucceeds_NoFinding is the negative space:
// a workspace whose lock derives cleanly must produce no
// effective-lock-derivable finding at all.
func TestDoctor_EffectiveLockDerivationSucceeds_NoFinding(t *testing.T) {
	root := t.TempDir()
	term := filepath.Join(root, "term")
	initRealRepo(t, term)
	bare := setupLocalBareRemote(t, term)

	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"term": {URL: bare, Branch: "main"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Doctor(context.Background(), root, exec.NewRealRunner(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Findings {
		if f.CheckID == "effective-lock-derivable" {
			t.Fatalf("a cleanly-derivable lock must produce no effective-lock-derivable finding; got %+v", f)
		}
	}
}
