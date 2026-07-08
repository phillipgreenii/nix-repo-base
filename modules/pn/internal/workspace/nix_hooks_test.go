package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestNixHookVars_InjectsConsumerOverrides verifies that a {nix_run} expansion
// for a consumer repo carries that repo's --override-input flags (from the lock)
// and an absolute flakeref, single-quoted — exercising the production path
// (nixHookVarsForLock + expandNixRunTokens) RunEventHooks uses.
func TestNixHookVars_InjectsConsumerOverrides(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.repo-base]\nurl=\"github:o/repo-base\"\n[repos.consumer]\nurl=\"github:o/consumer\"\n")
	for _, r := range []string{"repo-base", "consumer"} {
		mustMkdir(t, filepath.Join(root, r))
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{}")
	}
	// A lock matching the config exactly ⇒ effectiveLock returns it without
	// deriving (no nix eval in unit tests).
	lk := &Lock{
		Repos: map[string]LockRepoEntry{
			"repo-base": {FlakePath: "flake.nix", RemoteURL: "github:o/repo-base"},
			"consumer":  {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"},
		},
		Edges: []LockEdge{{Consumer: "consumer", Alias: "base", Target: "repo-base"}},
	}
	if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil {
		t.Fatal(err)
	}

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	vars := w.nixHookVarsForLock("consumer", lk)
	got, _, err := expandNixRunTokens("{nix_run install-pre-commit-hooks}", vars)
	if err != nil {
		t.Fatalf("expandNixRunTokens: %v", err)
	}
	wantOverride := "--override-input base 'git+file://" + filepath.Join(root, "repo-base") + "'"
	if !strings.Contains(got, wantOverride) {
		t.Errorf("missing override in %q", got)
	}
	if !strings.HasSuffix(got, "'"+filepath.Join(root, "consumer")+"#install-pre-commit-hooks'") {
		t.Errorf("bad flakeref suffix in %q", got)
	}
}

// TestRunEventHooks_RepoScopedFiresForProcessedRepoOnly verifies a repo-scoped
// hook fires only for the repo that declares it (a), runs in that repo's dir,
// and is skipped for a repo (b) that has no matching hook.
func TestRunEventHooks_RepoScopedFiresForProcessedRepoOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n[repos.b]\nurl=\"github:o/b\"\n")
	for _, r := range []string{"a", "b"} {
		mustMkdir(t, filepath.Join(root, r))
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{}")
	}
	lk := &Lock{Repos: map[string]LockRepoEntry{
		"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
		"b": {FlakePath: "flake.nix", RemoteURL: "github:o/b"},
	}}
	if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil {
		t.Fatal(err)
	}
	wantCmd := "nix run '" + filepath.Join(root, "a") + "#install-pre-commit-hooks'"
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", wantCmd}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatal(err)
	}
	// processed = both repos; only "a" declares the post-rebase hook.
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "rebase", []string{"a", "b"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var sh []exec.Call
	for _, c := range f.Calls() {
		if c.Name == "sh" {
			sh = append(sh, c)
		}
	}
	if len(sh) != 1 {
		t.Fatalf("want 1 sh call (repo a only), got %d", len(sh))
	}
	if sh[0].Opts.Dir != filepath.Join(root, "a") {
		t.Errorf("cwd = %q, want repo a", sh[0].Opts.Dir)
	}
}

// openHookWS writes a minimal workspace with the given toml body + a flake.nix in
// each named repo, optionally writes a matching lock, and opens it on runner f.
func openHookWS(t *testing.T, tomlBody string, repos []string, lk *Lock) *Workspace {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), tomlBody)
	for _, r := range repos {
		mustMkdir(t, filepath.Join(root, r))
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{}")
	}
	if lk != nil {
		if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil {
			t.Fatal(err)
		}
	}
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w
}

// TestRunEventHooks_RoutesStdoutStderrToSeparateWriters verifies the per-repo
// hook subprocess is wired with Stdout=out and Stderr=errOut (not merged onto a
// single writer) — the writer-discipline fix (bd pg2-4g2h).
func TestRunEventHooks_RoutesStdoutStderrToSeparateWriters(t *testing.T) {
	lk := &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}}}
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"echo hi\"]\n",
		[]string{"a"}, lk)
	f := w.runner.(*exec.FakeRunner)
	f.AddResponse("sh", []string{"-c", "echo hi"}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "rebase", []string{"a"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var sh *exec.Call
	for i := range f.Calls() {
		if f.Calls()[i].Name == "sh" {
			c := f.Calls()[i]
			sh = &c
		}
	}
	if sh == nil {
		t.Fatal("no sh call recorded")
	}
	if sh.Opts.Stdout != io.Writer(&out) {
		t.Errorf("subprocess Stdout not wired to out")
	}
	if sh.Opts.Stderr != io.Writer(&errOut) {
		t.Errorf("subprocess Stderr not wired to errOut (want separate from out)")
	}
}

// TestRunEventHooks_PostHookFailureWarnsToErrOut verifies a failing post-hook
// warns to errOut (not out, not os.Stderr) and does not propagate (bd pg2-4g2h).
func TestRunEventHooks_PostHookFailureWarnsToErrOut(t *testing.T) {
	lk := &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}}}
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"boom\"]\n",
		[]string{"a"}, lk)
	f := w.runner.(*exec.FakeRunner)
	f.AddResponse("sh", []string{"-c", "boom"}, exec.Result{Stderr: []byte("kaboom")}, errBoom)

	var out, errOut bytes.Buffer
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "rebase", []string{"a"}, &out, &errOut); err != nil {
		t.Fatalf("post-hook failure must not propagate; got %v", err)
	}
	if !strings.Contains(errOut.String(), "post-hook") {
		t.Errorf("errOut missing post-hook warning; got %q", errOut.String())
	}
	if strings.Contains(out.String(), "post-hook") {
		t.Errorf("warning leaked to out: %q", out.String())
	}
}

// TestRunEventHooks_SkipsLockDerivationWithoutNixRunToken verifies the effective
// lock is NOT derived (and no "effective lock unavailable" warning is emitted)
// when a matched hook has no {nix_run} token — the laziness fix that avoids
// O(N^2) nix evals and spurious warnings on token-free hooks (bd pg2-4g2h).
func TestRunEventHooks_SkipsLockDerivationWithoutNixRunToken(t *testing.T) {
	// No lock on disk ⇒ eager code would derive (nix eval) and warn; lazy must not.
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-rebase\"]\nrun=[\"echo hi\"]\n",
		[]string{"a"}, nil)
	f := w.runner.(*exec.FakeRunner)
	f.AddResponse("sh", []string{"-c", "echo hi"}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "rebase", []string{"a"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "effective lock unavailable") {
		t.Errorf("token-free hook should not derive/warn about the lock; got %q", errOut.String())
	}
}

// TestRunEventHooks_PerRepoPreHookFailureAborts covers the load-bearing
// pre-hook abort branch: a failing per-repo pre-* hook must abort the command
// (return the error), not warn-and-continue (bd pg2-eo09).
func TestRunEventHooks_PerRepoPreHookFailureAborts(t *testing.T) {
	lk := &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}}}
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"pre-rebase\"]\nrun=[\"gate\"]\n",
		[]string{"a"}, lk)
	f := w.runner.(*exec.FakeRunner)
	f.AddResponse("sh", []string{"-c", "gate"}, exec.Result{Stderr: []byte("nope")}, errBoom)

	var out, errOut bytes.Buffer
	err := w.RunEventHooks(context.Background(), HookPhasePre, "rebase", []string{"a"}, &out, &errOut)
	if err == nil {
		t.Fatal("a failing per-repo pre-hook must abort (return error), not continue")
	}
	if !strings.Contains(err.Error(), "pre-hook") || !strings.Contains(err.Error(), "a") {
		t.Errorf("error should name the failing pre-hook and its repo; got %v", err)
	}
}

// TestProcessedReposFor covers the per-command repo-set mapping (bd pg2-eo09):
// repo-iterating commands and upgrade process every repo; build/apply process
// only the terminal; everything else processes none.
func TestProcessedReposFor(t *testing.T) {
	lk := &Lock{Repos: map[string]LockRepoEntry{
		"term": {FlakePath: "flake.nix", RemoteURL: "github:o/term"},
		"base": {FlakePath: "flake.nix", RemoteURL: "github:o/base"},
	}}
	w := openHookWS(t,
		"[workspace]\nterminal=\"term\"\n[repos.term]\nurl=\"github:o/term\"\n[repos.base]\nurl=\"github:o/base\"\n",
		[]string{"term", "base"}, lk)
	ctx := context.Background()
	all := []string{"base", "term"}
	cases := []struct {
		cmd  string
		want []string
	}{
		{"clone", all},
		{"rebase", all},
		{"update", all},
		{"status", all},
		{"flake-check", all},
		{"format", all},
		{"push", all},
		{"pre-commit-check", all},
		{"upgrade", all},
		{"build", []string{"term"}},
		{"apply", []string{"term"}},
		{"lock", nil},
		{"tree", nil},
		{"init", nil},
	}
	for _, tc := range cases {
		got := w.ProcessedReposFor(ctx, tc.cmd)
		sorted := append([]string(nil), got...)
		sort.Strings(sorted)
		if !slices.Equal(sorted, tc.want) {
			t.Errorf("processedReposFor(%q) = %v; want %v", tc.cmd, sorted, tc.want)
		}
	}
}

var errBoom = errors.New("boom")
