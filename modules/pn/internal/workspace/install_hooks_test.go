package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// installArgs derives the expected argv from the same expression the
// implementation uses, so the test and impl cannot drift.
func installArgs(output string) []string { return installHooksRunArgs(output) }

// TestInstallHooks_RunsOptInOutputInRepoDir verifies a repo that opts in with a
// single output gets `nix run .#install-pre-commit-hooks` invoked with Dir set
// to that repo's directory and output streamed (Stdout set).
func TestInstallHooks_RunsOptInOutputInRepoDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
install-hooks = ["install-pre-commit-hooks"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.InstallHooks(context.Background(), &out, &errOut); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("expected Dir=%q, got %q", filepath.Join(root, "foo"), calls[0].Opts.Dir)
	}
	if calls[0].Opts.Stdout == nil {
		t.Error("install-hooks should stream output (Opts.Stdout set)")
	}
}

// TestInstallHooks_SkipsRepoWithoutOptIn verifies a repo that does not declare
// install-hooks produces no run call.
func TestInstallHooks_SkipsRepoWithoutOptIn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.InstallHooks(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	if got := len(f.Calls()); got != 0 {
		t.Errorf("expected no run calls for a non-opted-in repo; got %d", got)
	}
}

// TestInstallHooks_MixedWorkspaceRunsOnlyOptedIn verifies that when repo A opts
// in and repo B does not, exactly one run call is made (for A).
func TestInstallHooks_MixedWorkspaceRunsOnlyOptedIn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:owner/aaa"
install-hooks = ["install-pre-commit-hooks"]

[repos.bbb]
url = "github:owner/bbb"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.InstallHooks(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 call (only opted-in repo), got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "aaa") {
		t.Errorf("expected the opted-in repo aaa to run; Dir=%q", calls[0].Opts.Dir)
	}
}

// TestInstallHooks_MultipleOutputsRunInListOrder verifies a repo declaring two
// outputs runs both, in the order they appear in the list.
func TestInstallHooks_MultipleOutputsRunInListOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
install-hooks = ["first-hook", "second-hook"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", installArgs("first-hook"), exec.Result{}, nil)
	f.AddResponse("nix", installArgs("second-hook"), exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.InstallHooks(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got, want := strings.Join(calls[0].Args, " "), strings.Join(installArgs("first-hook"), " "); got != want {
		t.Errorf("first call args: got %q want %q", got, want)
	}
	if got, want := strings.Join(calls[1].Args, " "), strings.Join(installArgs("second-hook"), " "); got != want {
		t.Errorf("second call args: got %q want %q", got, want)
	}
}

// TestInstallHooks_ContinuesPastFailure verifies that one failing output/repo
// does not abort the sweep: the other repo still runs and a combined error
// naming the failed repo is returned.
func TestInstallHooks_ContinuesPastFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.aaa]
url = "github:owner/aaa"
install-hooks = ["install-pre-commit-hooks"]

[repos.bbb]
url = "github:owner/bbb"
install-hooks = ["install-pre-commit-hooks"]
`)

	f := exec.NewFakeRunner()
	// aaa sorts first (topo/alpha) -> fails; bbb -> succeeds.
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.InstallHooks(context.Background(), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected combined error from per-repo failure")
	}
	if !strings.Contains(err.Error(), "aaa") {
		t.Errorf("combined error should name the failed repo aaa; got %q", err.Error())
	}
	if len(f.Calls()) != 2 {
		t.Errorf("expected both repos attempted; got %d calls", len(f.Calls()))
	}
}

// TestInstallHooks_TopoAlphaOrderAcrossParticipatingRepos verifies participating
// repos run in topological/alphabetical order.
func TestInstallHooks_TopoAlphaOrderAcrossParticipatingRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
install-hooks = ["install-pre-commit-hooks"]

[repos.bar]
url = "github:owner/bar"
install-hooks = ["install-pre-commit-hooks"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.InstallHooks(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	// Alphabetical fallback order: bar before foo.
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("expected bar first; Dir=%q", calls[0].Opts.Dir)
	}
	if calls[1].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("expected foo second; Dir=%q", calls[1].Opts.Dir)
	}
}
