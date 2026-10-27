package workspace

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/trust"
)

func TestBuild_TerminalOnlyWithOverrides(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // applied-state store read by the apply-would gate
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	// Write lock file so overrideInputArgsFor has edges to work with.
	// leaf depends on dep via alias "dep-input".
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "leaf"],
  "repos": {
    "dep":  {"flake_path": "flake.nix", "remote_url": "github:owner/dep"},
    "leaf": {"flake_path": "flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [{"consumer": "leaf", "alias": "dep-input", "target": "dep"}]
}`)
	trustWS(t, root) // build now gates on workspace trust (bead pg2-x2q6o)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check (shared prelude)
	f.AddResponse("darwin-rebuild", []string{
		"build", "--flake", leafDir,
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Build(context.Background(), &out, BuildOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out.String(), "leaf") {
		t.Errorf("build output should name the terminal project %q; got:\n%s", "leaf", out.String())
	}
	// Calls: the shared nix-daemon probe, the read-only apply-would gate's git
	// probes (unscripted in this test, so they fail and are reported, not fatal),
	// and exactly one build command.
	var builds []exec.Call
	for _, c := range f.Calls() {
		if c.Name == "darwin-rebuild" {
			builds = append(builds, c)
		}
	}
	if len(builds) != 1 {
		t.Fatalf("expected 1 build call (no fmt), got %d", len(builds))
	}
	if builds[0].Opts.Dir != leafDir {
		t.Errorf("build command must run in terminal dir; got %q", builds[0].Opts.Dir)
	}
	// Build command streams its output live (Opts.Stdout set).
	if builds[0].Opts.Stdout == nil {
		t.Errorf("build should stream subprocess output (Opts.Stdout set on build)")
	}
}

func TestBuild_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Build(context.Background(), out, BuildOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "darwin-rebuild build --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing build command:\n%s", out.String())
	}
	if strings.Contains(out.String(), "nix fmt") {
		t.Errorf("dry-run output should not contain 'nix fmt' (fmt is now a separate command):\n%s", out.String())
	}
}

// TestBuild_ForeignOSErrorsWithoutBuilder verifies the loud-fail path: with the
// default template (which references {builder}) and no builder for this OS,
// Build returns an actionable error and never invokes the runner. Skipped on a
// host that HAS a built-in builder (darwin / NixOS), where the default resolves.
func TestBuild_ForeignOSErrorsWithoutBuilder(t *testing.T) {
	if defaultBuilder() != "" {
		t.Skip("host has a built-in builder; foreign-OS path not exercised here")
	}
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{})
	if err == nil {
		t.Fatal("expected loud error when no builder is available for the default template")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("runner must not be invoked on the builder-empty error; got %d calls", len(f.Calls()))
	}
}

// TestBuild_CheckFollowsRunsOnNixDir is the regression test for §3.7: for a
// subdir-flake terminal the follows check must read <repo>/nix/flake.lock, not
// <repo>/flake.lock. Before the re-wire it silently no-op'd (repo-root lock
// absent); now a broken follow in the nix-subdir lock fails Build. checkFollows
// runs before builder resolution, so this is deterministic on any host.
func TestBuild_CheckFollowsRunsOnNixDir(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, filepath.Join("leaf", "nix"))
	mkRepoDir(t, root, "a_dep")
	mkRepoDir(t, root, "b_dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.a_dep]
url = "github:owner/a"

[repos.b_dep]
url = "github:owner/b"
`)
	// Lock: leaf is a subdir flake (nix/flake.nix) with two workspace inputs.
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["a_dep", "b_dep", "leaf"],
  "repos": {
    "a_dep": {"flake_path": "flake.nix", "remote_url": "github:owner/a"},
    "b_dep": {"flake_path": "flake.nix", "remote_url": "github:owner/b"},
    "leaf":  {"flake_path": "nix/flake.nix", "remote_url": "github:owner/leaf"}
  },
  "edges": [
    {"consumer": "leaf", "alias": "a", "target": "a_dep"},
    {"consumer": "leaf", "alias": "b", "target": "b_dep"}
  ]
}`)
	// Broken follow lives in the nix SUBDIR lock: input "a" carries its own
	// unfollowed copy of "b". A repo-root flake.lock is intentionally absent.
	writeFile(t, filepath.Join(root, "leaf", "nix", "flake.lock"), `{
  "nodes": {
    "root": {"inputs": {"a": "a", "b": "b"}},
    "a": {"inputs": {"b": "b_2"}},
    "b": {"inputs": {}},
    "b_2": {"inputs": {}}
  }
}`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// ShowNixCommandsOnly still triggers checkFollows (it runs first); Builder is
	// injected but irrelevant because the follows error precedes builder use.
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{ShowNixCommandsOnly: true, Builder: "darwin-rebuild"})
	if err == nil {
		t.Fatal("expected follows error from the nix-subdir flake.lock")
	}
	if !strings.Contains(err.Error(), "does not follow") {
		t.Errorf("expected a follows-violation error; got: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("runner must not be invoked when follows check fails; got %d calls", len(f.Calls()))
	}
}

func TestBuild_ErrorsWhenTerminalUnset(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{}); err == nil {
		t.Fatal("expected error when terminal unset")
	}
}

// TestBuild_UntrustedAborts is the regression test for bead pg2-x2q6o: build_command
// is arbitrary argv taken from the discovered pn-workspace.toml and executed by Build,
// so an untrusted workspace root MUST abort before the command runs — closing the
// residual the ADR-0019 hook trust gate left open. Mirrors RunEventHooks' pre-phase
// abort. The isolated, empty XDG_STATE_HOME trust store makes the workspace untrusted.
func TestBuild_UntrustedAborts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"
build_command = "touch pwned.txt"

[repos.leaf]
url = "github:owner/leaf"
`)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Build(context.Background(), &bytes.Buffer{}, BuildOptions{Builder: "darwin-rebuild"})
	if !errors.Is(err, trust.ErrUntrusted) {
		t.Fatalf("Build of an untrusted workspace must abort with ErrUntrusted; got %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("untrusted build must not exec any command; got %d calls: %+v", len(f.Calls()), f.Calls())
	}
}

func TestApplyDecisionLine(t *testing.T) {
	cases := []struct {
		reason rebuildReason
		err    error
		want   string
	}{
		{reasonForced, nil, "apply would: rebuild (forced)"},
		{reasonDirty, nil, "apply would: rebuild (dirty)"},
		{reasonHeadChanged, nil, "apply would: rebuild (head_changed)"},
		{reasonNoRecord, nil, "apply would: rebuild (no_record)"},
		{reasonUnchanged, nil, "apply would: skip (unchanged)"},
		{"", errors.New("boom"), "apply would: fail (boom)"},
	}
	for _, c := range cases {
		if got := applyDecisionLine(c.reason, c.err); got != c.want {
			t.Errorf("applyDecisionLine(%q, %v) = %q, want %q", c.reason, c.err, got, c.want)
		}
	}
}

// buildGateFixture opens a trusted one-repo workspace whose build is scripted to
// succeed, with the read-only apply-would gate's git probes scripted per the
// arguments. status is `git status --porcelain` output; head is `git rev-parse
// HEAD`. It returns the workspace, the runner, and the repo dir.
func buildGateFixture(t *testing.T, status, head string) (*Workspace, *exec.FakeRunner, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	trustWS(t, root)
	leafDir := filepath.Join(root, "leaf")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte(status)}, nil)
	if status == "" {
		f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte(head + "\n")}, nil)
	}
	f.AddResponse("darwin-rebuild", []string{"build", "--flake", leafDir}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, f, leafDir
}

// storeFiles lists every file under the applied-state store (XDG_DATA_HOME).
func storeFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(os.Getenv("XDG_DATA_HOME"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// TestBuild_ReportsApplyDecision: build prints apply's skip decision for each
// reason the gate can give (forced is not reachable from build: it has no
// --force), still runs the build every time, and never writes applied state.
func TestBuild_ReportsApplyDecision(t *testing.T) {
	cases := []struct {
		name         string
		status, head string
		seed         string // applied_ref to seed ("" = no record)
		want         string
	}{
		{"dirty", " M f\n", "", "", "apply would: rebuild (dirty)"},
		{"no_record", "", "h1", "", "apply would: rebuild (no_record)"},
		{"head_changed", "", "h2", "h1", "apply would: rebuild (head_changed)"},
		{"unchanged", "", "h1", "h1", "apply would: skip (unchanged)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, f, leafDir := buildGateFixture(t, c.status, c.head)
			if c.seed != "" {
				if err := writeAppliedState(leafDir, AppliedState{AppliedRef: c.seed}); err != nil {
					t.Fatal(err)
				}
			}
			before := storeFiles(t)
			var out bytes.Buffer
			if err := w.Build(context.Background(), &out, BuildOptions{Builder: "darwin-rebuild"}); err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !strings.Contains(out.String(), c.want+"\n") {
				t.Errorf("output missing %q:\n%s", c.want, out.String())
			}
			// Build NEVER skips: the build command ran even for "unchanged".
			var built bool
			for _, call := range f.Calls() {
				built = built || call.Name == "darwin-rebuild"
			}
			if !built {
				t.Errorf("build must run the build command even when apply would skip")
			}
			if after := storeFiles(t); !reflect.DeepEqual(before, after) {
				t.Errorf("build must not write applied state: before %v, after %v", before, after)
			}
		})
	}
}

// TestBuild_ApplyDecisionFailureDoesNotFailBuild: an unreadable gate is reported
// as what apply would do (fail) but the build itself still runs and succeeds.
func TestBuild_ApplyDecisionFailureDoesNotFailBuild(t *testing.T) {
	w, _, leafDir := buildGateFixture(t, "", "h1")
	// Corrupt the store record: apply's gate fails closed on it.
	if err := writeAppliedState(leafDir, AppliedState{AppliedRef: "h1"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range storeFiles(t) {
		if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := w.Build(context.Background(), &out, BuildOptions{Builder: "darwin-rebuild"}); err != nil {
		t.Fatalf("Build must still succeed: %v", err)
	}
	if !strings.Contains(out.String(), "apply would: fail (") {
		t.Errorf("expected an `apply would: fail (...)` line:\n%s", out.String())
	}
}
