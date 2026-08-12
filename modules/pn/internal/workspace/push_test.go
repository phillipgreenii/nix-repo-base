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

func TestPush_AllReposWithUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// upstream check + push, alphabetical order (bar, foo).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "push"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Errorf("expected 4 calls (check+push per repo), got %d", len(calls))
	}
	// The push streams; the upstream probe stays captured (silent).
	for _, c := range calls {
		last := c.Args[len(c.Args)-1]
		if last == "push" && c.Opts.Stdout == nil {
			t.Errorf("git push should stream output (Opts.Stdout set); got %v", c.Args)
		}
		if last == "@{u}" && c.Opts.Stdout != nil {
			t.Errorf("upstream probe should stay captured (Opts.Stdout nil); got %v", c.Args)
		}
	}
}

// TestPush_TerminalFlagSuppressesWarning verifies that passing Terminal via
// PushOptions suppresses the no-terminal warning even when config has no terminal.
func TestPush_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails — no push (we just care about the warning, not push behavior).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

func TestPush_SkipsWithoutUpstream(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails — no push should happen.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	for _, c := range f.Calls() {
		if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "push" {
			t.Errorf("expected no push call when upstream missing; got %v", c.Args)
		}
	}
}

// ---------------------------------------------------------------------------
// Push with SetUpstream flag
// ---------------------------------------------------------------------------

// TestPush_NoUpstreamNoFlag verifies that a repo with no upstream is skipped
// (no-op) when SetUpstream is false.
func TestPush_NoUpstreamNoFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{SetUpstream: false}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "push" || a == "-u" {
				t.Errorf("no push expected when no upstream and SetUpstream is false; got %v", c.Args)
			}
		}
	}
}

// TestPush_NoUpstreamWithFlag verifies that a repo with no upstream gets
// `git push -u <remote> <branch>` when SetUpstream is true.
// The single-remote shortcut (step 2) is used here: git remote returns exactly "origin".
func TestPush_NoUpstreamWithFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	// current branch lookup.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("my-feature\n")}, nil)
	// resolvePushRemote: git remote → single remote "origin" (step 2 shortcut).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "remote"}, exec.Result{Stdout: []byte("origin\n")}, nil)
	// push -u origin <branch>.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push", "-u", "origin", "my-feature"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{SetUpstream: true}); err != nil {
		t.Fatalf("Push --set-upstream: %v", err)
	}
	// Verify push -u origin <branch> was called.
	var foundSetUpstream bool
	for _, c := range f.Calls() {
		args := c.Args
		if len(args) >= 6 && args[len(args)-4] == "push" && args[len(args)-3] == "-u" && args[len(args)-2] == "origin" && args[len(args)-1] == "my-feature" {
			foundSetUpstream = true
		}
	}
	if !foundSetUpstream {
		t.Errorf("expected git push -u origin my-feature; calls: %v", f.Calls())
	}
}

// TestPush_ExistingUpstreamPlainPush verifies that a repo that already has an
// upstream always gets a plain `git push`, even when SetUpstream is true.
func TestPush_ExistingUpstreamPlainPush(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Push(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PushOptions{SetUpstream: true}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Verify a plain push (no -u) was issued.
	var foundPlainPush bool
	for _, c := range f.Calls() {
		if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "push" {
			// Args should be exactly ["-C", repoDir, "push"] — no -u.
			foundPlainPush = true
			for _, a := range c.Args {
				if a == "-u" {
					t.Errorf("existing-upstream push must NOT have -u; got %v", c.Args)
				}
			}
		}
	}
	if !foundPlainPush {
		t.Error("expected a plain git push for repo with existing upstream")
	}
}

// ---------------------------------------------------------------------------
// resolvePushRemote unit tests
// ---------------------------------------------------------------------------

// addNoConfigResponse adds a scripted error response for a git config --get command,
// simulating the case where the config key is not set (exit code 1).
func addNoConfigResponse(f *exec.FakeRunner, repoDir string, configArgs []string) {
	f.AddResponse("git", append([]string{"-C", repoDir}, configArgs...),
		exec.Result{ExitCode: 1},
		&exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
}

// TestResolvePushRemote_SingleRemote verifies that when exactly one remote
// exists, the resolver returns it without consulting git config (step 2).
func TestResolvePushRemote_SingleRemote(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("upstream\n")}, nil)

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "upstream" {
		t.Errorf("got %q, want %q", got, "upstream")
	}
}

// TestResolvePushRemote_BranchPushRemote verifies step 3: branch.<branch>.pushRemote
// is consulted when multiple remotes exist and no flag is set.
func TestResolvePushRemote_BranchPushRemote(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes → not the single-remote shortcut.
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)
	// Step 3: branch.main.pushRemote = gitea
	f.AddResponse("git", []string{"-C", repoDir, "config", "--get", "branch.main.pushRemote"},
		exec.Result{Stdout: []byte("gitea\n")}, nil)

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "gitea" {
		t.Errorf("got %q, want %q", got, "gitea")
	}
}

// TestResolvePushRemote_LocalPushDefault verifies step 4: remote.pushDefault
// (local) is consulted after branch.pushRemote misses.
func TestResolvePushRemote_LocalPushDefault(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes.
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)
	// Step 3: branch.pushRemote not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--get", "branch.main.pushRemote"})
	// Step 4: remote.pushDefault (local) = gitea
	f.AddResponse("git", []string{"-C", repoDir, "config", "--local", "--get", "remote.pushDefault"},
		exec.Result{Stdout: []byte("gitea\n")}, nil)

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "gitea" {
		t.Errorf("got %q, want %q", got, "gitea")
	}
}

// TestResolvePushRemote_GlobalPushDefault verifies step 5: remote.pushDefault
// (global) is consulted after local config misses.
func TestResolvePushRemote_GlobalPushDefault(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes.
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)
	// Step 3: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--get", "branch.main.pushRemote"})
	// Step 4: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--local", "--get", "remote.pushDefault"})
	// Step 5: global remote.pushDefault = gitea
	f.AddResponse("git", []string{"-C", repoDir, "config", "--global", "--get", "remote.pushDefault"},
		exec.Result{Stdout: []byte("gitea\n")}, nil)

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "gitea" {
		t.Errorf("got %q, want %q", got, "gitea")
	}
}

// TestResolvePushRemote_OriginFallback verifies step 6: "origin" is used when
// no config is set but "origin" is among the remotes.
func TestResolvePushRemote_OriginFallback(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes including origin.
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)
	// Step 3: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--get", "branch.main.pushRemote"})
	// Step 4: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--local", "--get", "remote.pushDefault"})
	// Step 5: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--global", "--get", "remote.pushDefault"})

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "origin" {
		t.Errorf("got %q, want %q", got, "origin")
	}
}

// TestResolvePushRemote_NoOriginNoConfig verifies step 7: when multiple remotes
// exist, none is "origin", and no config is set, an error is returned.
func TestResolvePushRemote_NoOriginNoConfig(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes, neither is origin.
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("upstream\ngitea\n")}, nil)
	// Step 3: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--get", "branch.main.pushRemote"})
	// Step 4: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--local", "--get", "remote.pushDefault"})
	// Step 5: not set.
	addNoConfigResponse(f, repoDir, []string{"config", "--global", "--get", "remote.pushDefault"})

	_, err := resolvePushRemote(context.Background(), f, repoDir, "main", "")
	if err == nil {
		t.Fatal("expected error when no remote can be resolved; got nil")
	}
	// Error must name available remotes and hint at config commands.
	if !strings.Contains(err.Error(), "upstream") || !strings.Contains(err.Error(), "gitea") {
		t.Errorf("error should name available remotes; got: %v", err)
	}
	if !strings.Contains(err.Error(), "remote.pushDefault") {
		t.Errorf("error should hint at remote.pushDefault; got: %v", err)
	}
}

// TestResolvePushRemote_FlagOverride verifies step 1: --remote flag overrides
// all convention-based resolution.
func TestResolvePushRemote_FlagOverride(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	// Two remotes; convention would pick "origin" but flag says "gitea".
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)

	got, err := resolvePushRemote(context.Background(), f, repoDir, "main", "gitea")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "gitea" {
		t.Errorf("got %q, want %q", got, "gitea")
	}
}

// TestResolvePushRemote_FlagOverrideMissingRemote verifies that --remote errors
// when the named remote doesn't exist in the repo.
func TestResolvePushRemote_FlagOverrideMissingRemote(t *testing.T) {
	f := exec.NewFakeRunner()
	repoDir := "/fake/repo"
	f.AddResponse("git", []string{"-C", repoDir, "remote"}, exec.Result{Stdout: []byte("origin\n")}, nil)

	_, err := resolvePushRemote(context.Background(), f, repoDir, "main", "nonexistent")
	if err == nil {
		t.Fatal("expected error when flagged remote doesn't exist; got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing remote; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Push with SetUpstream + Remote flag
// ---------------------------------------------------------------------------

// TestPush_RemoteFlagOverride verifies that PushOptions.Remote is passed
// through to resolvePushRemote, overriding convention-based resolution.
func TestPush_RemoteFlagOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// upstream check fails.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	// current branch lookup.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("my-feature\n")}, nil)
	// resolvePushRemote: git remote → "origin" and "gitea"; flag says "gitea".
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "remote"}, exec.Result{Stdout: []byte("origin\ngitea\n")}, nil)
	// push -u gitea my-feature (not origin).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push", "-u", "gitea", "my-feature"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{SetUpstream: true, Remote: "gitea"}); err != nil {
		t.Fatalf("Push --set-upstream --remote gitea: %v", err)
	}
	var foundGiteaPush bool
	for _, c := range f.Calls() {
		args := c.Args
		if len(args) >= 6 && args[len(args)-4] == "push" && args[len(args)-3] == "-u" && args[len(args)-2] == "gitea" {
			foundGiteaPush = true
		}
	}
	if !foundGiteaPush {
		t.Errorf("expected git push -u gitea my-feature; calls: %v", f.Calls())
	}
}

// TestPush_RemoteResolutionFailureSkipsRepo verifies that when remote resolution
// fails for one repo, the repo is skipped (error to stderr) and iteration continues.
func TestPush_RemoteResolutionFailureSkipsRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.bar]
url = "github:owner/bar"

[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// bar: upstream check fails, branch lookup ok, no remotes → resolution error → skip.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	// git remote returns empty → 0 remotes → resolution will fail with structured error.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "remote"}, exec.Result{Stdout: []byte("")}, nil)

	// foo: upstream check fails, branch lookup ok, single remote "origin" → push succeeds.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "remote"}, exec.Result{Stdout: []byte("origin\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "push", "-u", "origin", "main"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	// The whole Push call must succeed (skip-not-fail semantics).
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{SetUpstream: true}); err != nil {
		t.Fatalf("Push should not fail when one repo's remote resolution fails; got %v", err)
	}
	// bar's error must appear on stderr.
	if !strings.Contains(errOut.String(), "bar") {
		t.Errorf("expected bar skip message on stderr; got %q", errOut.String())
	}
	// foo must still have been pushed.
	var foundFooPush bool
	for _, c := range f.Calls() {
		if len(c.Args) >= 2 && c.Args[len(c.Args)-1] == "main" && c.Args[len(c.Args)-2] == "origin" {
			foundFooPush = true
		}
	}
	if !foundFooPush {
		t.Errorf("expected foo to be pushed after bar's skip; calls: %v", f.Calls())
	}
}

// pushEdgeFixture builds a two-repo workspace where `consumer` declares the
// flake-input alias "dep" against `dep`, with a committed pn-workspace.lock.json
// (so no nix eval is needed to learn the edge or the topological order) and a
// flake.lock on disk for the consumer (without one, propagateWorkspaceEdges
// returns before running anything and a propagation assertion would be vacuous).
// Topological order is dep → consumer.
func pushEdgeFixture(t *testing.T) (root, dep, consumer string, f *exec.FakeRunner) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.dep]
url = "github:owner/dep"

[repos.consumer]
url = "github:owner/consumer"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "consumer"],
  "repos": {
    "dep":      {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "consumer": {"flake_path": "flake.nix", "remote_url": "github:owner/consumer"}
  },
  "edges": [{"consumer": "consumer", "alias": "dep", "target": "dep"}]
}`)
	dep = mkGitRepoDir(t, root, "dep")
	consumer = mkGitRepoDir(t, root, "consumer")
	writeFile(t, filepath.Join(consumer, "flake.lock"),
		`{"nodes":{"root":{"inputs":{"dep":"dep"}},"dep":{"locked":{"rev":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},"root":"root"}`)
	f = exec.NewFakeRunner()
	return root, dep, consumer, f
}

// mkGitRepoDir is mkRepoDir plus an (empty) .git directory, so isGitRepo reports
// true. Push skips the sibling relock for a configured repo that is not cloned;
// without the marker these fixtures would exercise that skip instead of the relock.
func mkGitRepoDir(t *testing.T, root, name string) string {
	t.Helper()
	d := mkRepoDir(t, root, name)
	if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git in %s: %v", d, err)
	}
	return d
}

// TestPush_PropagatesSiblingsInterleavedWithPushes is the positive half of
// ADR 0023 item 2 (beads pg2-x42j3 / pg2-j2f8f): `pn workspace push` owns the
// interleaved loop that `update` no longer performs.
//
// The ORDER is the contract, not merely that both happen. C1 says a consumer can
// only relock to a rev already on the remote, so the dep's push MUST precede the
// consumer's relock, and the consumer's relock MUST precede the consumer's push
// (otherwise the bump commit is not in the pushed history). All three assertions
// are made against call indices in the log.
func TestPush_PropagatesSiblingsInterleavedWithPushes(t *testing.T) {
	root, dep, consumer, f := pushEdgeFixture(t)
	// dep has no workspace inputs → no relock; it is pushed first (topo order).
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", dep, "push"}, exec.Result{}, nil)
	// consumer: clean-tree probes, relock against dep's (now pushed) remote tip,
	// C2 clean check, then push.
	f.AddResponse("git", []string{"-C", consumer, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", consumer, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"flake", "update", "--refresh", "dep"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", consumer, "diff", "--quiet", "--", "flake.lock"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", consumer, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", consumer, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "dep"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	depPush, relock, consumerPush := -1, -1, -1
	for i, c := range f.Calls() {
		switch {
		case c.Name == "git" && len(c.Args) == 3 && c.Args[1] == dep && c.Args[2] == "push":
			depPush = i
		case c.Name == "nix" && len(c.Args) == 4 && c.Args[0] == "flake" && c.Args[1] == "update":
			relock = i
		case c.Name == "git" && len(c.Args) == 3 && c.Args[1] == consumer && c.Args[2] == "push":
			consumerPush = i
		}
	}
	if depPush < 0 || relock < 0 || consumerPush < 0 {
		t.Fatalf("expected dep push, consumer relock and consumer push; got depPush=%d relock=%d consumerPush=%d calls=%v",
			depPush, relock, consumerPush, f.Calls())
	}
	if depPush >= relock {
		t.Errorf("the dependency MUST be pushed before its consumer relocks (C1): depPush=%d relock=%d", depPush, relock)
	}
	if relock >= consumerPush {
		t.Errorf("the consumer MUST relock before it is pushed, or the bump is not published: relock=%d consumerPush=%d", relock, consumerPush)
	}
}

// TestPush_NoSiblings_SkipsPropagation covers the ADR 0023 item 3 opt-out: the
// pushes still happen, and no relock is attempted — not even the clean-tree probe
// that guards it, so --no-siblings stays a pure git command with no nix eval.
func TestPush_NoSiblings_SkipsPropagation(t *testing.T) {
	root, dep, consumer, f := pushEdgeFixture(t)
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", dep, "push"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", consumer, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", consumer, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "dep", NoSiblings: true}); err != nil {
		t.Fatalf("Push --no-siblings: %v", err)
	}
	pushes := 0
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			t.Errorf("--no-siblings must NOT relock (no nix call); got nix %v", c.Args)
		}
		if c.Name == "git" && len(c.Args) == 3 && c.Args[2] == "push" {
			pushes++
		}
	}
	if pushes != 2 {
		t.Errorf("--no-siblings must still push every repo; got %d pushes, calls=%v", pushes, f.Calls())
	}
}

// TestPush_RefusesToRelockDirtyRepo: the relock ends in a commit, so a repo with
// uncommitted changes must abort the run BEFORE that repo is pushed, and the error
// must name --no-siblings. A silent skip is the failure this refuses to become: it
// would publish while quietly leaving the locks unconverged.
func TestPush_RefusesToRelockDirtyRepo(t *testing.T) {
	root, dep, consumer, f := pushEdgeFixture(t)
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", dep, "push"}, exec.Result{}, nil)
	// consumer is dirty: `git diff --quiet` exits 1.
	f.AddResponse("git", []string{"-C", consumer, "diff", "--quiet"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	err = w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "dep"})
	if err == nil {
		t.Fatalf("expected Push to refuse relocking a dirty repo")
	}
	if !strings.Contains(err.Error(), "consumer") || !strings.Contains(err.Error(), "--no-siblings") {
		t.Errorf("error must name the dirty repo and the --no-siblings escape; got %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) == 3 && c.Args[1] == consumer && c.Args[2] == "push" {
			t.Errorf("the dirty repo must NOT be pushed after the refusal; calls=%v", f.Calls())
		}
	}
}

// TestPush_InWorkforestSet_SkipsPropagation: inside a coordinated set, push
// publishes the set's feature branch and does NOT relock — propagation is a
// canonical-clone operation (ADR 0023 item 4), and the set deliberately resolves
// siblings through --override-input rather than through its flake.lock. The skip
// must be announced on stderr so it is not mistaken for a converged run.
func TestPush_InWorkforestSet_SkipsPropagation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".workforests", "feature-x")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.dep]
url = "github:owner/dep"

[repos.consumer]
url = "github:owner/consumer"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "consumer"],
  "repos": {
    "dep":      {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "consumer": {"flake_path": "flake.nix", "remote_url": "github:owner/consumer"}
  },
  "edges": [{"consumer": "consumer", "alias": "dep", "target": "dep"}]
}`)
	dep := mkGitRepoDir(t, root, "dep")
	consumer := mkGitRepoDir(t, root, "consumer")
	writeFile(t, filepath.Join(consumer, "flake.lock"),
		`{"nodes":{"root":{"inputs":{"dep":"dep"}},"dep":{"locked":{"rev":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},"root":"root"}`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", dep, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", dep, "push"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", consumer, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", consumer, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Push(context.Background(), &out, &errOut, PushOptions{Terminal: "consumer"}); err != nil {
		t.Fatalf("Push inside a set: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			t.Errorf("push inside a workforest set must NOT relock siblings; got nix %v", c.Args)
		}
	}
	if !strings.Contains(errOut.String(), "workforest set") {
		t.Errorf("the skipped relock must be announced on stderr; got %q", errOut.String())
	}
}
