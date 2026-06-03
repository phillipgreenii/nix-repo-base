package workspace

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TestBuildDAG_OrdersDepsBeforeDependents checks the pure graph builder: an
// edge A->B exists when A declares an input named B's input-name. overlay
// follows base; the terminal consumes both.
func TestBuildDAG_OrdersDepsBeforeDependents(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "ziprecruiter"},
		Repos: map[string]RepoConfig{
			"base":         {InputName: "phillipgreenii-nix-base"},
			"overlay":      {InputName: "ovl"},
			"ziprecruiter": {},
		},
	}
	declared := map[string][]string{
		"base":         {"nixpkgs"},
		"overlay":      {"nixpkgs", "phillipgreenii-nix-base"},
		"ziprecruiter": {"nixpkgs", "phillipgreenii-nix-base", "ovl"},
	}

	order, dependsOn := buildDAG(cfg, declared)

	wantOrder := []string{"base", "overlay", "ziprecruiter"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
	wantDeps := map[string][]string{
		"overlay":      {"base"},
		"ziprecruiter": {"base", "overlay"},
	}
	if !reflect.DeepEqual(dependsOn, wantDeps) {
		t.Errorf("dependsOn = %v, want %v", dependsOn, wantDeps)
	}
}

// TestBuildDAG_SiblingsAlphabeticalTiebreak verifies deterministic ordering of
// independent repos at the same depth.
func TestBuildDAG_SiblingsAlphabeticalTiebreak(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"base": {InputName: "base"},
			"bbb":  {InputName: "bbb"},
			"aaa":  {InputName: "aaa"},
			"term": {},
		},
	}
	declared := map[string][]string{
		"aaa":  {"base"},
		"bbb":  {"base"},
		"term": {"base", "aaa", "bbb"},
	}

	order, _ := buildDAG(cfg, declared)
	want := []string{"base", "aaa", "bbb", "term"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// TestBuildDAG_CycleFallback: a dependency cycle still yields every repo in the
// order (the topo-sort fallback appends the cyclic nodes alphabetically).
func TestBuildDAG_CycleFallback(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "z"},
		Repos: map[string]RepoConfig{
			"a": {InputName: "a"},
			"b": {InputName: "b"},
			"z": {},
		},
	}
	declared := map[string][]string{
		"a": {"b"}, // a <-> b cycle
		"b": {"a"},
		"z": {"a"},
	}
	order, _ := buildDAG(cfg, declared)
	if len(order) != 3 {
		t.Fatalf("order should contain all repos despite the cycle; got %v", order)
	}
	seen := map[string]bool{}
	for _, k := range order {
		seen[k] = true
	}
	for _, k := range []string{"a", "b", "z"} {
		if !seen[k] {
			t.Errorf("order missing %q: %v", k, order)
		}
	}
}

// TestGatherDeclaredInputs_ErrorsOnBadEvalOutput: non-JSON eval output is a
// hard error, not silently ignored.
func TestGatherDeclaredInputs_ErrorsOnBadEvalOutput(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	writeFile(t, filepath.Join(root, "term", "flake.nix"), "{ inputs = {}; }")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"
`)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "term", "flake.nix"), "inputs", "--apply", "builtins.attrNames"}, exec.Result{Stdout: []byte("not json")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.gatherDeclaredInputs(context.Background()); err == nil {
		t.Fatal("expected an error on non-JSON eval output")
	}
}

// TestDeriveDAG_ReadsDeclaredInputsFromFlakeNix exercises the IO path: each
// repo's declared inputs come from `nix eval --file flake.nix`, not the lock.
func TestDeriveDAG_ReadsDeclaredInputsFromFlakeNix(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base", "overlay"} {
		mkRepoDir(t, root, r)
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{ inputs = {}; }")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
input-name = "nb"

[repos.overlay]
url = "github:o/overlay"
input-name = "ovl"
`)

	f := exec.NewFakeRunner()
	evalArgs := func(repo string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", "builtins.attrNames"}
	}
	f.AddResponse("nix", evalArgs("base"), exec.Result{Stdout: []byte(`["nixpkgs"]`)}, nil)
	f.AddResponse("nix", evalArgs("overlay"), exec.Result{Stdout: []byte(`["nixpkgs","nb"]`)}, nil)
	f.AddResponse("nix", evalArgs("term"), exec.Result{Stdout: []byte(`["nb","ovl"]`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	order, dependsOn, err := w.deriveDAG(context.Background())
	if err != nil {
		t.Fatalf("deriveDAG: %v", err)
	}
	if want := []string{"base", "overlay", "term"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if got := dependsOn["term"]; !reflect.DeepEqual(got, []string{"base", "overlay"}) {
		t.Errorf("dependsOn[term] = %v, want [base overlay]", got)
	}
	if got := dependsOn["overlay"]; !reflect.DeepEqual(got, []string{"base"}) {
		t.Errorf("dependsOn[overlay] = %v, want [base]", got)
	}
}

// TestGatherDeclaredInputs_SkipsRepoWithoutFlakeNix verifies a declared repo
// that isn't cloned (no flake.nix on disk) is skipped, not evaluated.
func TestGatherDeclaredInputs_SkipsRepoWithoutFlakeNix(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	writeFile(t, filepath.Join(root, "term", "flake.nix"), "{ inputs = {}; }")
	mkRepoDir(t, root, "base") // dir exists but NO flake.nix -> skipped
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
input-name = "nb"
`)
	f := exec.NewFakeRunner()
	// Only term is evaluated; base has no flake.nix so no eval is scripted.
	f.AddResponse("nix", []string{"eval", "--json", "--file", filepath.Join(root, "term", "flake.nix"), "inputs", "--apply", "builtins.attrNames"}, exec.Result{Stdout: []byte(`["nb"]`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	declared, err := w.gatherDeclaredInputs(context.Background())
	if err != nil {
		t.Fatalf("gatherDeclaredInputs: %v", err)
	}
	if _, ok := declared["base"]; ok {
		t.Errorf("base has no flake.nix and must be skipped; got %v", declared)
	}
	if got := declared["term"]; !reflect.DeepEqual(got, []string{"nb"}) {
		t.Errorf("term declared inputs = %v, want [nb]", got)
	}
}

// TestRefreshLock_WritesDAGLock verifies `pn workspace lock`'s core: re-derive
// the DAG and write it to pn-workspace.lock (no clone/reconcile).
func TestRefreshLock_WritesDAGLock(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base"} {
		mkRepoDir(t, root, r)
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{ inputs = {}; }")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
input-name = "nb"
`)
	evalArgs := func(repo string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", "builtins.attrNames"}
	}
	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalArgs("base"), exec.Result{Stdout: []byte(`["nixpkgs"]`)}, nil)
	f.AddResponse("nix", evalArgs("term"), exec.Result{Stdout: []byte(`["nb"]`)}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.RefreshLock(context.Background()); err != nil {
		t.Fatalf("RefreshLock: %v", err)
	}

	lock, err := ReadLock(filepath.Join(root, "pn-workspace.lock"))
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if want := []string{"base", "term"}; !reflect.DeepEqual(lock.Order, want) {
		t.Errorf("lock.Order = %v, want %v", lock.Order, want)
	}
	if got := lock.DependsOn["term"]; !reflect.DeepEqual(got, []string{"base"}) {
		t.Errorf("lock.DependsOn[term] = %v, want [base]", got)
	}
}
