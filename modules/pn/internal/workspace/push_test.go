package workspace

import (
	"bytes"
	"context"
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
