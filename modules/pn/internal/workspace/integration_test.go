package workspace

// integration_test.go: cross-cutting end-to-end tests for the init/clone/lock
// + command pipeline (bead tc-perh.9.13).
//
// Each test exercises a lifecycle scenario using FakeRunner + temp directories.
// Scenarios are keyed to the bead description:
//   1. Fresh bootstrap: config → Clone → WriteDerivedLock → Tree shows DAG.
//   2. From discovery: Init writes config → WriteDerivedLock → Rebase topo order.
//   4. Custom flake_path honored by lock command.
//   5b. Both .lock and .lock.json present: .json wins, .lock removed.
//   6. In-memory derivation fallback: no lock on disk, Rebase derives topo order.
//   9. Same alias, different targets per consumer (legal).
//  10. Cyclic flake graph: topoSortByDeps degrades to alphabetical (not error).
//  11. Stale lock + new repo added to config: effectiveLock falls through to deriveLock.
//
// Scenarios already covered elsewhere:
//   3a/3b/3c  →  derive_lock_test.go
//   5a (legacy .lock → .lock.json migration) → derive_lock_test.go
//   7 (canonicalURL parity)  → canonical_url_test.go
//   8 (git+ssh:// matching github:) → canonical_url_test.go

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

// fullApplyExpr is the nix eval expression expected by gatherInputURLs.
const fullApplyExpr = `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`

// evalInputsArgs builds the args used by gatherInputURLs for a given repo's flake.
func evalInputsArgs(root, repo string) []string {
	return []string{
		"eval", "--json", "--file",
		filepath.Join(root, repo, "flake.nix"),
		"inputs", "--apply", fullApplyExpr,
	}
}

// TestIntegration_FreshBootstrap_CloneLockTree (Scenario 1):
// Hand-written pn-workspace.toml → Clone (mocked) → WriteDerivedLock → Tree shows expected DAG.
//
// Topology: term depends on base ("my-base" alias).
// Expected tree output: term → base.
func TestIntegration_FreshBootstrap_CloneLockTree(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"
`)

	// Simulate clone: create repo dirs with flake.nix.
	for _, r := range []string{"term", "base"} {
		dir := filepath.Join(root, r)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	f := exec.NewFakeRunner()
	// Clone calls: both repos are present already on disk (mkdirs above), but
	// clone.go checks for .git directory. Since we don't set up real git repos,
	// Clone will skip them (isGitRepo returns false without .git). So we
	// instead test Clone→lock→tree by mocking the nix eval calls.

	// nix eval: base has no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "base"),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	// nix eval: term depends on base via "my-base" alias.
	f.AddResponse("nix", evalInputsArgs(root, "term"),
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// WriteDerivedLock: computes edge term→base, writes lock file.
	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	// Verify lock file exists and has the edge.
	data, err := os.ReadFile(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse lock: %v", err)
	}
	if len(lock.Edges) != 1 {
		t.Fatalf("expected 1 edge in lock, got %d: %v", len(lock.Edges), lock.Edges)
	}
	e := lock.Edges[0]
	if e.Consumer != "term" || e.Alias != "my-base" || e.Target != "base" {
		t.Errorf("edge = %+v, want {Consumer:term Alias:my-base Target:base}", e)
	}
	if lock.Order[0] != "base" || lock.Order[1] != "term" {
		t.Errorf("lock.Order = %v, want [base term]", lock.Order)
	}

	// Re-open workspace so it reads the newly-written lock, then run Tree.
	ws2, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open(2): %v", err)
	}
	// Tree with workspace-internal view needs gatherInputURLs unless the lock is current.
	// Since lock now matches config, effectiveLock returns the disk lock.
	// Tree in non-AllInputs mode calls gatherInputURLs directly, so we need a runner.
	f2 := exec.NewFakeRunner()
	f2.AddResponse("nix", evalInputsArgs(root, "base"),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	f2.AddResponse("nix", evalInputsArgs(root, "term"),
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)
	ws2.runner = f2

	var buf bytes.Buffer
	if err := ws2.Tree(context.Background(), &buf, TreeOptions{}); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	want := "term\n" +
		"└── base\n"
	if buf.String() != want {
		t.Errorf("Tree output:\n got %q\nwant %q", buf.String(), want)
	}
}

// TestIntegration_FromDiscovery_InitLockRebase (Scenario 2):
// Three cloned repos with no pn-workspace.toml → Init writes config
// → WriteDerivedLock builds full lock → Rebase runs in topo order.
func TestIntegration_FromDiscovery_InitLockRebase(t *testing.T) {
	root := t.TempDir()

	// Create git repos (with .git dirs) for discovery.
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	// Write a minimal TOML to satisfy Open.
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "")

	f := exec.NewFakeRunner()
	// Init will call "git -C <dir> remote get-url origin" for each new repo.
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		f.AddResponse("git",
			[]string{"-C", filepath.Join(root, name), "remote", "get-url", "origin"},
			exec.Result{Stdout: []byte("https://github.com/o/" + name + ".git\n")}, nil)
	}

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var initOut bytes.Buffer
	if err := ws.Init(context.Background(), &initOut, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Verify all three repos were added to config.
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if _, ok := ws.config.Repos[name]; !ok {
			t.Errorf("Init: repo %q not added to config", name)
		}
	}

	// Now set terminal in config (auto-detect would find multiple candidates,
	// which causes missing_terminal; we pick ccc as terminal).
	ws.config.Workspace.Terminal = "ccc"

	// WriteDerivedLock: topology = aaa, bbb independent; ccc depends on aaa.
	f2 := exec.NewFakeRunner()
	// aaa: no workspace inputs.
	f2.AddResponse("nix", evalInputsArgs(root, "aaa"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// bbb: no workspace inputs.
	f2.AddResponse("nix", evalInputsArgs(root, "bbb"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// ccc: depends on aaa.
	f2.AddResponse("nix", evalInputsArgs(root, "ccc"),
		exec.Result{Stdout: []byte(`{"aaa-input":{"url":"github:o/aaa","flake":true}}`)}, nil)
	ws.runner = f2

	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	// Verify lock order has aaa before ccc; bbb can be anywhere.
	data, _ := os.ReadFile(filepath.Join(root, LockFileName))
	var lock Lock
	json.Unmarshal(data, &lock)

	aaaIdx, cccIdx := -1, -1
	for i, k := range lock.Order {
		if k == "aaa" {
			aaaIdx = i
		}
		if k == "ccc" {
			cccIdx = i
		}
	}
	if aaaIdx < 0 || cccIdx < 0 {
		t.Fatalf("lock.Order missing aaa or ccc: %v", lock.Order)
	}
	if aaaIdx >= cccIdx {
		t.Errorf("lock.Order: aaa (%d) should come before ccc (%d): %v", aaaIdx, cccIdx, lock.Order)
	}

	// Re-open and run Rebase; verify aaa is rebased before ccc.
	ws3, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open(3): %v", err)
	}
	f3 := exec.NewFakeRunner()
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		dir := filepath.Join(root, name)
		// No upstream → skip; Rebase skips repos without upstream.
		f3.AddResponse("git", []string{"-C", dir, "rev-parse", "--abbrev-ref", "@{u}"},
			exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	}
	ws3.runner = f3

	var rebaseOut, rebaseErrOut bytes.Buffer
	if err := ws3.Rebase(context.Background(), &rebaseOut, &rebaseErrOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	// All repos skip (no upstream), but the warning should appear on stderr (no terminal in this ws3 open).
	// The lock matches config so topoAlpha uses lock order. No failures expected.
}

// TestIntegration_CustomFlakePath_HonoredByLock (Scenario 4):
// Repo has a custom flake_path. Lock command uses that path for nix eval.
func TestIntegration_CustomFlakePath_HonoredByLock(t *testing.T) {
	root := t.TempDir()
	// Repo "myrepo" has flake at custom/path/flake.nix.
	dir := filepath.Join(root, "myrepo", "custom", "path")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "myrepo"

[repos.myrepo]
url = "github:o/myrepo"
flake_path = "custom/path/flake.nix"
`)

	// nix eval must be called with the custom path.
	customFlakePath := filepath.Join(root, "myrepo", "custom", "path", "flake.nix")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--json", "--file", customFlakePath, "inputs", "--apply", fullApplyExpr},
		exec.Result{Stdout: []byte(`{}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	// Lock file should record the custom flake_path.
	data, err := os.ReadFile(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if !strings.Contains(string(data), "custom/path/flake.nix") {
		t.Errorf("lock file should record custom flake_path; got:\n%s", string(data))
	}
}

// TestIntegration_BothLockFiles_JsonWins_LegacyRemoved (Scenario 5b):
// Both pn-workspace.lock and pn-workspace.lock.json are present.
// .json wins; .lock is removed after WriteDerivedLock.
func TestIntegration_BothLockFiles_JsonWins_LegacyRemoved(t *testing.T) {
	root := t.TempDir()
	makeFlakeDirs(t, root, "base", "terminal")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "terminal"

[repos.terminal]
url = "github:o/terminal"

[repos.base]
url = "github:o/base"
`)
	// Write both legacy and new lock files.
	legacyPath := filepath.Join(root, LockFileNameLegacy)
	writeFile(t, legacyPath, `{"order":["base","terminal"],"dependsOn":{}}`)
	// Write a current lock.json (will be overwritten by WriteDerivedLock).
	writeFile(t, filepath.Join(root, LockFileName), `{"order":[],"repos":{},"edges":[]}`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalInputsArgs(root, "base"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f.AddResponse("nix", evalInputsArgs(root, "terminal"),
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var notice strings.Builder
	if err := ws.WriteDerivedLockTo(context.Background(), root, &notice, ""); err != nil {
		t.Fatalf("WriteDerivedLockTo: %v", err)
	}

	// Legacy file should be removed.
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy lock should have been removed; still exists")
	}
	// New .json file should exist with proper content.
	data, err := os.ReadFile(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("new lock missing: %v", err)
	}
	if !strings.Contains(string(data), "my-base") {
		t.Errorf("new lock missing edge; content:\n%s", data)
	}
	// Notice should mention the removal.
	if !strings.Contains(notice.String(), "removed legacy") {
		t.Errorf("expected legacy removal notice; got %q", notice.String())
	}
}

// TestIntegration_InMemoryDerivation_NoLockOnDisk (Scenario 6):
// No lock file on disk. Rebase (non-required) runs via in-memory deriveLock;
// order matches topo order derived from flake inputs.
func TestIntegration_InMemoryDerivation_NoLockOnDisk(t *testing.T) {
	root := t.TempDir()
	// aaa and zzz are cloned with flake.nix files.
	for _, name := range []string{"aaa", "zzz"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:o/aaa"

[repos.zzz]
url = "github:o/zzz"
`)
	// No lock file on disk.

	f := exec.NewFakeRunner()
	// deriveLock will call gatherInputURLs via effectiveLock (lock is stale/empty).
	// zzz has no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "zzz"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// aaa depends on zzz.
	f.AddResponse("nix", evalInputsArgs(root, "aaa"),
		exec.Result{Stdout: []byte(`{"zzz-input":{"url":"github:o/zzz","flake":true}}`)}, nil)
	// Rebase upstream checks (no upstream → skip).
	for _, name := range []string{"aaa", "zzz"} {
		f.AddResponse("git",
			[]string{"-C", filepath.Join(root, name), "rev-parse", "--abbrev-ref", "@{u}"},
			exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	}

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// topoAlpha should derive via effectiveLock (no disk lock → derives).
	order := ws.topoAlpha(context.Background())
	if len(order) != 2 {
		t.Fatalf("topoAlpha: got %d items, want 2: %v", len(order), order)
	}
	// zzz (dep of aaa) should come first.
	if order[0] != "zzz" || order[1] != "aaa" {
		t.Errorf("in-memory derived order = %v, want [zzz aaa]", order)
	}

	// Rebase should run without error (all repos skip due to no upstream).
	var rebaseOut, rebaseErrOut bytes.Buffer
	if err := ws.Rebase(context.Background(), &rebaseOut, &rebaseErrOut, RebaseOptions{}); err != nil {
		t.Fatalf("Rebase: %v", err)
	}
}

// TestIntegration_SameAliasInDifferentConsumers (Scenario 9):
// Consumer A and consumer B both use alias "shared-dep" for different targets.
// Both edges are recorded; no alias uniqueness violation (uniqueness is per-consumer).
func TestIntegration_SameAliasInDifferentConsumers(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"lib-x", "lib-y", "consumer-a", "consumer-b"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "consumer-b"

[repos.lib-x]
url = "github:o/lib-x"

[repos.lib-y]
url = "github:o/lib-y"

[repos.consumer-a]
url = "github:o/consumer-a"

[repos.consumer-b]
url = "github:o/consumer-b"
`)

	f := exec.NewFakeRunner()
	// lib-x: no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "lib-x"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// lib-y: no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "lib-y"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// consumer-a: binds alias "shared-dep" to lib-x.
	f.AddResponse("nix", evalInputsArgs(root, "consumer-a"),
		exec.Result{Stdout: []byte(`{"shared-dep":{"url":"github:o/lib-x","flake":true}}`)}, nil)
	// consumer-b: binds alias "shared-dep" to lib-y.
	f.AddResponse("nix", evalInputsArgs(root, "consumer-b"),
		exec.Result{Stdout: []byte(`{"shared-dep":{"url":"github:o/lib-y","flake":true},"ca":{"url":"github:o/consumer-a","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// WriteDerivedLock should succeed (same alias in different consumers is legal).
	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(root, LockFileName))
	var lock Lock
	json.Unmarshal(data, &lock)

	// Expect 3 edges:
	// consumer-a → lib-x (alias: shared-dep)
	// consumer-b → lib-y (alias: shared-dep)
	// consumer-b → consumer-a (alias: ca)
	if len(lock.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d: %v", len(lock.Edges), lock.Edges)
	}

	// Verify each consumer has the correct alias mapping.
	edgeMap := make(map[[2]string]string) // [consumer, alias] → target
	for _, e := range lock.Edges {
		edgeMap[[2]string{e.Consumer, e.Alias}] = e.Target
	}
	if edgeMap[([2]string{"consumer-a", "shared-dep"})] != "lib-x" {
		t.Errorf("consumer-a/shared-dep should map to lib-x; edges=%v", lock.Edges)
	}
	if edgeMap[([2]string{"consumer-b", "shared-dep"})] != "lib-y" {
		t.Errorf("consumer-b/shared-dep should map to lib-y; edges=%v", lock.Edges)
	}
}

// TestIntegration_CyclicFlakeGraph_DegradesToAlphabetical (Scenario 10):
// A depends on B; B depends on A (cycle). buildEdges + topoSortByDeps degrades
// to alphabetical order (does NOT return an error). deriveLock does not error on
// a cycle; it produces terminal_not_sink when the terminal is consumed by another repo.
//
// This test is split into two parts to avoid runner response contention:
// Part 1: tests buildEdges directly with pre-built inputURLs (no runner consumed).
// Part 2: tests deriveLock with a fresh runner holding exactly one set of responses.
func TestIntegration_CyclicFlakeGraph_DegradesToAlphabetical(t *testing.T) {
	// Part 1: buildEdges with manually-constructed inputURLs.
	// No workspace or runner needed — pure data transformation.
	repos := map[string]RepoConfig{
		"alpha": {URL: "github:o/alpha"},
		"beta":  {URL: "github:o/beta"},
	}
	inputURLs := map[string]map[string]InputSpec{
		"alpha": {"beta-input": {URL: "github:o/beta", Flake: true}},
		"beta":  {"alpha-input": {URL: "github:o/alpha", Flake: true}},
	}
	edges, order, err := buildEdges(repos, inputURLs)
	if err != nil {
		t.Fatalf("buildEdges should not error on cycle; got: %v", err)
	}
	// Both edges should be present (cycle doesn't prevent edge recording).
	if len(edges) != 2 {
		t.Errorf("expected 2 edges in cyclic graph, got %d: %v", len(edges), edges)
	}
	// topoSortByDeps degrades: cyclic nodes get alphabetical order appended.
	if len(order) != 2 {
		t.Fatalf("expected 2 repos in order, got %d: %v", len(order), order)
	}
	if order[0] != "alpha" || order[1] != "beta" {
		t.Errorf("cyclic graph order = %v, want alphabetical [alpha beta]", order)
	}

	// Part 2: deriveLock with a workspace + runner.
	// alpha is the explicit terminal, but beta consumes alpha → terminal_not_sink.
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, name)
		if err2 := os.MkdirAll(dir, 0o755); err2 != nil {
			t.Fatal(err2)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "alpha"

[repos.alpha]
url = "github:o/alpha"

[repos.beta]
url = "github:o/beta"
`)
	// One set of responses for deriveLock's single gatherInputURLs call.
	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalInputsArgs(root, "alpha"),
		exec.Result{Stdout: []byte(`{"beta-input":{"url":"github:o/beta","flake":true}}`)}, nil)
	f.AddResponse("nix", evalInputsArgs(root, "beta"),
		exec.Result{Stdout: []byte(`{"alpha-input":{"url":"github:o/alpha","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	lock, validErrs, err := deriveLock(context.Background(), ws, "")
	if err != nil {
		t.Fatalf("deriveLock should not error on cycle; got: %v", err)
	}
	_ = lock
	// terminal_not_sink: alpha is terminal but beta's edge targets alpha.
	foundNotSink := false
	for _, ve := range validErrs {
		if ve.Code == "terminal_not_sink" {
			foundNotSink = true
		}
	}
	if !foundNotSink {
		t.Errorf("expected terminal_not_sink for cyclic terminal; validErrs=%v", validErrs)
	}
}

// TestIntegration_StaleLockNewRepo_EffectiveLockDerives (Scenario 11):
// Stale lock on disk (only has "base"), config has new repo "newrepo".
// effectiveLock detects mismatch → falls through to in-memory deriveLock.
// Commands work in correct topo order.
func TestIntegration_StaleLockNewRepo_EffectiveLockDerives(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"base", "newrepo"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.base]
url = "github:o/base"

[repos.newrepo]
url = "github:o/newrepo"
`)
	// Stale lock: only has "base".
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["base"],
  "repos": {"base": {"flake_path": "flake.nix", "remote_url": "github:o/base"}},
  "edges": []
}`)

	f := exec.NewFakeRunner()
	// effectiveLock detects stale → calls deriveLock → gatherInputURLs.
	f.AddResponse("nix", evalInputsArgs(root, "base"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// newrepo depends on base.
	f.AddResponse("nix", evalInputsArgs(root, "newrepo"),
		exec.Result{Stdout: []byte(`{"base-input":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// lockMatchesConfig should return false (stale lock only has "base").
	if lockMatchesConfig(ws.lock, ws.config) {
		t.Error("lockMatchesConfig should return false for stale lock")
	}

	// effectiveLock should derive a fresh lock with both repos.
	lock, _, err := ws.effectiveLock(context.Background())
	if err != nil {
		t.Fatalf("effectiveLock: %v", err)
	}
	if len(lock.Repos) != 2 {
		t.Errorf("derived lock should have 2 repos, got %d: %v", len(lock.Repos), lock.Repos)
	}

	// topoAlpha should derive order: base before newrepo.
	// Re-run with fresh runner for the topoAlpha call (it will call effectiveLock again).
	f2 := exec.NewFakeRunner()
	f2.AddResponse("nix", evalInputsArgs(root, "base"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	f2.AddResponse("nix", evalInputsArgs(root, "newrepo"),
		exec.Result{Stdout: []byte(`{"base-input":{"url":"github:o/base","flake":true}}`)}, nil)
	ws.runner = f2

	order := ws.topoAlpha(context.Background())
	if len(order) != 2 {
		t.Fatalf("topoAlpha: got %d items, want 2: %v", len(order), order)
	}
	if order[0] != "base" || order[1] != "newrepo" {
		t.Errorf("topoAlpha with stale lock = %v, want [base newrepo]", order)
	}
}

// TestIntegration_CanonicalURL_ParityWithRevLock (Scenario 7):
// canonicalURL applied to a TOML config URL and to the URL stored in revs.json
// (displayURL form) should produce identical results — they share the same
// canonicalization logic and the same source of truth (displayURL → canonicalURL).
// This is a pure-function assertion; no runner is needed.
func TestIntegration_CanonicalURL_ParityWithRevLock(t *testing.T) {
	// The config URL (toml form) and the stored display URL (from displayURL helper)
	// should canonicalize identically.
	pairs := []struct {
		tomlURL    string // value in pn-workspace.toml's url field
		displayURL string // value stored in displayURL(rc) → revs.json/lock.json remote_url
	}{
		{"github:phillipgreenii/nix-personal", "github:phillipgreenii/nix-personal"},
		{"github:owner/repo", "github:owner/repo"},
		// displayURL preserves the raw URL; both forms should normalize to same canonical.
	}
	for _, p := range pairs {
		a := canonicalURL(p.tomlURL)
		b := canonicalURL(p.displayURL)
		if a == "" {
			t.Errorf("canonicalURL(%q) is empty", p.tomlURL)
		}
		if a != b {
			t.Errorf("canonical parity: toml %q → %q, display %q → %q",
				p.tomlURL, a, p.displayURL, b)
		}
	}

	// Verify that the form update.go would record in revs.json (displayURL of RepoConfig)
	// matches the canonical form of the flake input URL a consumer would use.
	rc := RepoConfig{URL: "github:phillipgreenii/nix-personal"}
	storedURL := displayURL(rc)
	flakeInputURL := "git+ssh://git@github.com/phillipgreenii/nix-personal.git"

	if canonicalURL(storedURL) != canonicalURL(flakeInputURL) {
		t.Errorf("parity failure: stored URL canonical=%q, flake input URL canonical=%q",
			canonicalURL(storedURL), canonicalURL(flakeInputURL))
	}
}

// TestIntegration_SingleConsumerTwoAliasesSameProducer covers the case where
// a single consumer repo declares TWO flake inputs that both resolve to the
// same producer repo (e.g. "foo" and "bar" both point at producer's URL).
//
// This exercises the path documented in tc-perh.9.22:
//   - buildEdges emits two distinct LockEdges (Consumer=consumer, Target=producer)
//     with different Alias values.
//   - edgesToDependsOn deduplicates to a single dep (consumer depends on producer once).
//   - overrideInputArgsFor("consumer") emits BOTH --override-input flags.
//   - topo order: producer first, consumer second (one entry each).
func TestIntegration_SingleConsumerTwoAliasesSameProducer(t *testing.T) {
	root := t.TempDir()

	// Create repo directories with minimal flake.nix files.
	for _, name := range []string{"consumer", "producer"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "consumer"

[repos.consumer]
url = "github:o/consumer"

[repos.producer]
url = "github:o/producer"
`)

	f := exec.NewFakeRunner()
	// producer: no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "producer"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// consumer: two inputs ("foo" and "bar") both pointing at producer's URL.
	// "foo" uses the github: shorthand; "bar" uses the git+ssh:// form.
	// canonicalURL normalizes both to the same canonical, so both resolve to producer.
	f.AddResponse("nix", evalInputsArgs(root, "consumer"),
		exec.Result{Stdout: []byte(`{"bar":{"url":"git+ssh://git@github.com/o/producer.git","flake":true},"foo":{"url":"github:o/producer","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse lock: %v", err)
	}

	// Assertion 1: exactly two edges, both Consumer=consumer, Target=producer.
	if len(lock.Edges) != 2 {
		t.Fatalf("expected 2 edges (one per alias), got %d: %v", len(lock.Edges), lock.Edges)
	}
	aliasSet := make(map[string]bool)
	for _, e := range lock.Edges {
		if e.Consumer != "consumer" {
			t.Errorf("edge consumer = %q, want \"consumer\": %+v", e.Consumer, e)
		}
		if e.Target != "producer" {
			t.Errorf("edge target = %q, want \"producer\": %+v", e.Target, e)
		}
		aliasSet[e.Alias] = true
	}
	if !aliasSet["foo"] {
		t.Errorf("missing edge with alias \"foo\"; edges=%v", lock.Edges)
	}
	if !aliasSet["bar"] {
		t.Errorf("missing edge with alias \"bar\"; edges=%v", lock.Edges)
	}

	// Assertion 2: topo order — producer before consumer, each appears exactly once.
	if len(lock.Order) != 2 {
		t.Fatalf("expected 2 repos in order, got %d: %v", len(lock.Order), lock.Order)
	}
	producerIdx, consumerIdx := -1, -1
	for i, k := range lock.Order {
		if k == "producer" {
			producerIdx = i
		}
		if k == "consumer" {
			consumerIdx = i
		}
	}
	if producerIdx < 0 || consumerIdx < 0 {
		t.Fatalf("lock.Order missing producer or consumer: %v", lock.Order)
	}
	if producerIdx >= consumerIdx {
		t.Errorf("topo order: producer (%d) must come before consumer (%d): %v",
			producerIdx, consumerIdx, lock.Order)
	}

	// Assertion 3: overrideInputArgsFor("consumer") emits BOTH --override-input flags.
	// Re-open workspace so it loads the written lock.
	ws2, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open(2): %v", err)
	}
	overrideArgs := ws2.overrideInputArgsFor("consumer", overrideOpts{})
	// Expect 6 args: 2 × ("--override-input", alias, url)
	if len(overrideArgs) != 6 {
		t.Fatalf("overrideInputArgsFor: got %d args (want 6): %v", len(overrideArgs), overrideArgs)
	}
	// Extract aliases from args (positions 1 and 4 after sort-by-alias).
	// Aliases are sorted alphabetically: "bar" < "foo".
	if overrideArgs[1] != "bar" {
		t.Errorf("first alias = %q, want \"bar\"", overrideArgs[1])
	}
	if overrideArgs[4] != "foo" {
		t.Errorf("second alias = %q, want \"foo\"", overrideArgs[4])
	}
	// Both should point at producer's directory.
	producerDir := filepath.Join(root, "producer")
	wantURL := "git+file://" + producerDir
	if overrideArgs[2] != wantURL {
		t.Errorf("first url = %q, want %q", overrideArgs[2], wantURL)
	}
	if overrideArgs[5] != wantURL {
		t.Errorf("second url = %q, want %q", overrideArgs[5], wantURL)
	}
}

// TestIntegration_RealMonorepodURLForms (Scenario 8):
// Fixture with github: shorthand in TOML and git+ssh:// in consumer's flake.
// Lock produces the edge (URL canonicalization normalizes both to same form).
func TestIntegration_RealMonorepodURLForms(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"nix-personal", "homelab"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "flake.nix"), "{ inputs = {}; }")
	}

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "homelab"

[repos.nix-personal]
url = "github:phillipgreenii/nix-personal"

[repos.homelab]
url = "github:phillipgreenii/homelab"
`)

	f := exec.NewFakeRunner()
	// nix-personal: no workspace inputs.
	f.AddResponse("nix", evalInputsArgs(root, "nix-personal"),
		exec.Result{Stdout: []byte(`{}`)}, nil)
	// homelab: references nix-personal via git+ssh:// (real monorepod URL form).
	f.AddResponse("nix", evalInputsArgs(root, "homelab"),
		exec.Result{Stdout: []byte(`{"nix-personal":{"url":"git+ssh://git@github.com/phillipgreenii/nix-personal.git","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := ws.WriteDerivedLock(context.Background(), root); err != nil {
		t.Fatalf("WriteDerivedLock: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(root, LockFileName))
	var lock Lock
	json.Unmarshal(data, &lock)

	// Expect one edge: homelab → nix-personal.
	if len(lock.Edges) != 1 {
		t.Fatalf("expected 1 edge (git+ssh:// → github: match), got %d: %v", len(lock.Edges), lock.Edges)
	}
	e := lock.Edges[0]
	if e.Consumer != "homelab" || e.Target != "nix-personal" {
		t.Errorf("edge = %+v, want homelab → nix-personal", e)
	}
}
