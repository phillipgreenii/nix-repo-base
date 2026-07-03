package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestBuildEdges_HeterogeneousAliases: consumer A uses alias "X-base" for dep B;
// consumer C uses alias "totally-different" for dep B. Both edges recorded.
func TestBuildEdges_HeterogeneousAliases(t *testing.T) {
	repos := map[string]RepoConfig{
		"a": {URL: "github:o/a"},
		"b": {URL: "github:o/b"},
		"c": {URL: "github:o/c"},
	}
	// B's canonical URL is "github.com/o/b"
	// A's input "X-base" has URL "github:o/b" which matches B.
	// C's input "totally-different" also has URL "github:o/b" which matches B.
	inputURLs := map[string]map[string]InputSpec{
		"a": {
			"X-base":  {URL: "github:o/b", Flake: true},
			"nixpkgs": {URL: "https://github.com/NixOS/nixpkgs.git", Flake: true},
		},
		"b": {},
		"c": {
			"totally-different": {URL: "github:o/b", Flake: true},
		},
	}

	edges, order, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}

	// Sort edges for deterministic assertion.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Consumer != edges[j].Consumer {
			return edges[i].Consumer < edges[j].Consumer
		}
		return edges[i].Alias < edges[j].Alias
	})

	wantEdges := []LockEdge{
		{Consumer: "a", Alias: "X-base", Target: "b"},
		{Consumer: "c", Alias: "totally-different", Target: "b"},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v, want %v", edges, wantEdges)
	}

	// B must appear before A and C in the order (it's a dependency).
	bIdx, aIdx, cIdx := -1, -1, -1
	for i, o := range order {
		switch o {
		case "b":
			bIdx = i
		case "a":
			aIdx = i
		case "c":
			cIdx = i
		}
	}
	if bIdx >= aIdx || bIdx >= cIdx {
		t.Errorf("b must come before a and c in order; got %v", order)
	}
}

// TestBuildEdges_SameAliasDifferentConsumers: consumer A binds "shared-name" to
// target X; consumer C binds "shared-name" to target Y. Both edges are valid
// because per-consumer uniqueness holds (same alias is fine across consumers).
func TestBuildEdges_SameAliasDifferentConsumers(t *testing.T) {
	repos := map[string]RepoConfig{
		"a": {URL: "github:o/a"},
		"x": {URL: "github:o/x"},
		"y": {URL: "github:o/y"},
		"c": {URL: "github:o/c"},
	}
	inputURLs := map[string]map[string]InputSpec{
		"a": {"shared-name": {URL: "github:o/x", Flake: true}},
		"c": {"shared-name": {URL: "github:o/y", Flake: true}},
		"x": {},
		"y": {},
	}

	edges, _, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}

	sort.Slice(edges, func(i, j int) bool {
		return edges[i].Consumer < edges[j].Consumer
	})

	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d: %v", len(edges), edges)
	}
	// a -> x, c -> y
	if edges[0].Consumer != "a" || edges[0].Target != "x" {
		t.Errorf("edge[0] = %v, want {a,shared-name,x}", edges[0])
	}
	if edges[1].Consumer != "c" || edges[1].Target != "y" {
		t.Errorf("edge[1] = %v, want {c,shared-name,y}", edges[1])
	}
}

// TestBuildEdges_FlakeFalseSkipped: inputs with Flake=false produce no edges.
func TestBuildEdges_FlakeFalseSkipped(t *testing.T) {
	repos := map[string]RepoConfig{
		"a": {URL: "github:o/a"},
		"b": {URL: "github:o/b"},
	}
	inputURLs := map[string]map[string]InputSpec{
		"a": {"b-inp": {URL: "github:o/b", Flake: false}}, // flake=false: should NOT generate an edge
		"b": {},
	}

	edges, _, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected no edges for flake=false input, got %v", edges)
	}
}

// TestBuildEdges_ExternalInputNoEdge: a consumer's input URL does not match any
// workspace repo — no edge generated.
func TestBuildEdges_ExternalInputNoEdge(t *testing.T) {
	repos := map[string]RepoConfig{
		"a": {URL: "github:o/a"},
	}
	inputURLs := map[string]map[string]InputSpec{
		"a": {"nixpkgs": {URL: "https://github.com/NixOS/nixpkgs.git", Flake: true}},
	}

	edges, _, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected no edges for external input, got %v", edges)
	}
}

// TestBuildEdges_DuplicateRemoteURL: two workspace repos share the same canonical
// URL — buildEdges returns a duplicate_remote_url error.
func TestBuildEdges_DuplicateRemoteURL(t *testing.T) {
	repos := map[string]RepoConfig{
		"a": {URL: "github:owner/repo"},
		"b": {URL: "https://github.com/owner/repo.git"}, // same canonical URL as "a"
	}
	inputURLs := map[string]map[string]InputSpec{}

	_, _, err := buildEdges(repos, inputURLs)
	if err == nil {
		t.Fatal("expected duplicate_remote_url error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate_remote_url") {
		t.Errorf("expected 'duplicate_remote_url' in error, got: %v", err)
	}
}

// TestBuildEdges_RealMonorepodShape verifies the monorepod workspace shape:
// nix-personal, nix-overlay, nix-repo-base, homelab. homelab references
// nix-personal via git+ssh:// and uses alias "phillipgreenii-nix-personal".
func TestBuildEdges_RealMonorepodShape(t *testing.T) {
	repos := map[string]RepoConfig{
		"nix-repo-base": {URL: "github:phillipgreenii/nix-repo-base"},
		"nix-overlay":   {URL: "github:phillipgreenii/nix-overlay"},
		"nix-personal":  {URL: "github:phillipgreenii/nix-personal"},
		"homelab":       {URL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"},
	}
	// homelab's flake.nix (at nix/flake.nix) references nix-personal via git+ssh://
	inputURLs := map[string]map[string]InputSpec{
		"nix-repo-base": {},
		"nix-overlay": {
			"phillipgreenii-nix-base": {URL: "github:phillipgreenii/nix-repo-base", Flake: true},
		},
		"nix-personal": {
			"phillipgreenii-nix-base": {URL: "github:phillipgreenii/nix-repo-base", Flake: true},
		},
		"homelab": {
			"phillipgreenii-nix-base":     {URL: "github:phillipgreenii/nix-repo-base", Flake: true},
			"phillipgreenii-nix-overlay":  {URL: "github:phillipgreenii/nix-overlay", Flake: true},
			"phillipgreenii-nix-personal": {URL: "git+ssh://git@github.com/phillipgreenii/nix-personal.git", Flake: true},
		},
	}

	edges, order, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}

	// Check that nix-repo-base comes before nix-overlay, nix-personal, homelab.
	posOf := func(key string) int {
		for i, o := range order {
			if o == key {
				return i
			}
		}
		return -1
	}
	if posOf("nix-repo-base") >= posOf("nix-overlay") {
		t.Errorf("nix-repo-base must precede nix-overlay in order; got %v", order)
	}
	if posOf("nix-repo-base") >= posOf("nix-personal") {
		t.Errorf("nix-repo-base must precede nix-personal in order; got %v", order)
	}
	if posOf("nix-personal") >= posOf("homelab") {
		t.Errorf("nix-personal must precede homelab in order; got %v", order)
	}

	// Find the homelab -> nix-personal edge (using git+ssh:// URL).
	var foundGitSSHEdge bool
	for _, e := range edges {
		if e.Consumer == "homelab" && e.Target == "nix-personal" && e.Alias == "phillipgreenii-nix-personal" {
			foundGitSSHEdge = true
		}
	}
	if !foundGitSSHEdge {
		t.Errorf("expected homelab→nix-personal edge (git+ssh:// form); edges=%v", edges)
	}
}

// TestGatherInputURLs_FallbackPath: when the full expression fails, the fallback
// (without flake field) is tried and a log message is emitted.
func TestGatherInputURLs_FallbackPath(t *testing.T) {
	root := t.TempDir()
	makeRepoWithFlakeAt(t, root, "myrepo", "flake.nix")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)

	flakeAbs := filepath.Join(root, "myrepo", "flake.nix")
	fullApplyExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	fallbackApplyExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = true; }) is`

	f := exec.NewFakeRunner()
	// Full expression fails.
	fullErr := &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}}
	f.AddResponse("nix", []string{"eval", "--json", "--file", flakeAbs, "inputs", "--apply", fullApplyExpr},
		exec.Result{ExitCode: 1}, fullErr)
	// Fallback expression succeeds.
	f.AddResponse("nix", []string{"eval", "--json", "--file", flakeAbs, "inputs", "--apply", fallbackApplyExpr},
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)

	// Capture stderr.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := ws.gatherInputURLs(context.Background())

	_ = w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	stderrOut := string(buf[:n])

	if err != nil {
		t.Fatalf("gatherInputURLs: %v", err)
	}
	if result["myrepo"] == nil {
		t.Errorf("expected non-nil specs for myrepo; got nil")
	}
	// Should have emitted a log about fallback.
	if !strings.Contains(stderrOut, "fallback") {
		t.Errorf("expected 'fallback' in stderr; got: %q", stderrOut)
	}
}

// TestRefreshLock_WritesEdgesAndOrder: RefreshLock writes the new verbose lock
// with edges derived from URL matching.
func TestRefreshLock_WritesEdgesAndOrder(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base"} {
		if err := os.MkdirAll(filepath.Join(root, r), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{ inputs = {}; }")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
`)

	flakeBase := filepath.Join(root, "base", "flake.nix")
	flakeTerm := filepath.Join(root, "term", "flake.nix")
	fullApplyExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`

	f := exec.NewFakeRunner()
	// base: no workspace inputs.
	f.AddResponse("nix", []string{"eval", "--json", "--file", flakeBase, "inputs", "--apply", fullApplyExpr},
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	// term: depends on base via URL.
	f.AddResponse("nix", []string{"eval", "--json", "--file", flakeTerm, "inputs", "--apply", fullApplyExpr},
		exec.Result{Stdout: []byte(`{"nb":{"url":"github:o/base","flake":true}}`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.RefreshLock(context.Background()); err != nil {
		t.Fatalf("RefreshLock: %v", err)
	}

	lock, err := ReadLock(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if want := []string{"base", "term"}; !reflect.DeepEqual(lock.Order, want) {
		t.Errorf("lock.Order = %v, want %v", lock.Order, want)
	}
	if len(lock.Edges) != 1 || lock.Edges[0].Consumer != "term" || lock.Edges[0].Target != "base" {
		t.Errorf("lock.Edges = %v, want [{term nb base}]", lock.Edges)
	}
}
