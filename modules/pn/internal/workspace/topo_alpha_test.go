package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestTopoAlpha_UsesLockOrderWhenCurrent: when disk lock matches config repos,
// topoAlpha returns lock.Order (topological order).
func TestTopoAlpha_UsesLockOrderWhenCurrent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:o/aaa"

[repos.zzz]
url = "github:o/zzz"
`)
	// Lock says zzz before aaa (topo: aaa depends on zzz)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["zzz", "aaa"],
  "repos": {
    "aaa": {"flake_path": "flake.nix", "remote_url": "github:o/aaa"},
    "zzz": {"flake_path": "flake.nix", "remote_url": "github:o/zzz"}
  },
  "edges": [{"consumer": "aaa", "alias": "zzz-input", "target": "zzz"}]
}`)

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	order := w.topoAlpha(context.Background())
	if len(order) != 2 {
		t.Fatalf("got %d items, want 2", len(order))
	}
	if order[0] != "zzz" || order[1] != "aaa" {
		t.Errorf("order = %v, want [zzz aaa]", order)
	}
}

// TestTopoAlpha_DerivesOrderWhenLockStale: when disk lock doesn't match config,
// topoAlpha derives order via effectiveLock (requires nix eval scripting).
func TestTopoAlpha_DerivesOrderWhenLockStale(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "aaa")
	mkRepoDir(t, root, "zzz")
	writeFile(t, filepath.Join(root, "aaa", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "zzz", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:o/aaa"

[repos.zzz]
url = "github:o/zzz"
`)
	// Stale lock: only "aaa" — does not match 2-repo config
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["aaa"],
  "repos": {"aaa": {"flake_path": "flake.nix", "remote_url": "github:o/aaa"}},
  "edges": []
}`)

	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	f := exec.NewFakeRunner()
	// aaa depends on zzz via URL
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "aaa", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{"zzz-input":{"url":"github:o/zzz","flake":true}}`)}, nil)
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "zzz", "flake.nix"), "inputs", "--apply", fullExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	order := ws.topoAlpha(context.Background())
	if len(order) != 2 {
		t.Fatalf("got %d items, want 2", len(order))
	}
	// Derived order: zzz before aaa (zzz is dep of aaa)
	if order[0] != "zzz" || order[1] != "aaa" {
		t.Errorf("derived order = %v, want [zzz aaa]", order)
	}
}

// TestTopoAlpha_FallsBackToAlphabetical: when deriveLock fails (no flakes on disk),
// topoAlpha returns alphabetical order.
func TestTopoAlpha_FallsBackToAlphabetical(t *testing.T) {
	root := t.TempDir()
	// Repos exist in config but no flake.nix files, so gatherInputURLs skips them
	// and buildEdges produces empty order (but topoAlpha falls back to orderedRepoNames)
	mkRepoDir(t, root, "bbb")
	mkRepoDir(t, root, "aaa")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:o/aaa"

[repos.bbb]
url = "github:o/bbb"
`)
	// No lock file; repos have no flake.nix → gatherInputURLs returns empty → deriveLock OK
	// but no topo edges → order is alphabetical

	// gatherInputURLs: repos have no flake.nix, so they're skipped
	ws, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	order := ws.topoAlpha(context.Background())
	if len(order) != 2 {
		t.Fatalf("got %d items, want 2", len(order))
	}
	if order[0] != "aaa" || order[1] != "bbb" {
		t.Errorf("alphabetical fallback order = %v, want [aaa bbb]", order)
	}
}

// TestRebase_TopoOrder: Rebase processes repos in topo order (zzz before aaa when aaa→zzz).
func TestRebase_TopoOrder(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "aaa")
	mkRepoDir(t, root, "zzz")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:o/aaa"

[repos.zzz]
url = "github:o/zzz"
`)
	// Lock with topo order zzz, aaa
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["zzz", "aaa"],
  "repos": {
    "aaa": {"flake_path": "flake.nix", "remote_url": "github:o/aaa"},
    "zzz": {"flake_path": "flake.nix", "remote_url": "github:o/zzz"}
  },
  "edges": [{"consumer": "aaa", "alias": "zzz-input", "target": "zzz"}]
}`)

	aDir := filepath.Join(root, "aaa")
	zDir := filepath.Join(root, "zzz")
	// upstream check for each (hasUpstream, migrated onto x/gitclient's
	// RefReader.HasUpstream — bead pg2-oxle0).
	stubGitOpener(t, map[string]*fakeGitReader{
		zDir: {hasUpstreamVal: true},
		aDir: {hasUpstreamVal: true},
	})

	// fetch + pull --rebase --autostash migrated onto Fetcher.Fetch +
	// Syncer.Sync (bead pg2-8bfb5); order is asserted via the shared log.
	var order []string
	stubGitMutatorOpener(t, map[string]*fakeGitMutator{
		zDir: {name: "zzz", log: &order},
		aDir: {name: "aaa", log: &order},
	})

	f := exec.NewFakeRunner()
	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := ws.Rebase(context.Background(), &out, &errOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	// Verify zzz was rebased before aaa (check fetch calls for ordering)
	var gotOrder []string
	for _, entry := range order {
		if entry == "zzz:fetch" || entry == "aaa:fetch" {
			gotOrder = append(gotOrder, strings.TrimSuffix(entry, ":fetch"))
		}
	}
	if len(gotOrder) != 2 || gotOrder[0] != "zzz" || gotOrder[1] != "aaa" {
		t.Errorf("rebase order: got %v, want [zzz aaa]", gotOrder)
	}
}
