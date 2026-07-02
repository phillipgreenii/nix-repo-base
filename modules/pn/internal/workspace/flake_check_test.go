package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestFlakeCheck_AllPass(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("first dir: got %q", calls[0].Opts.Dir)
	}
	if calls[1].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("second dir: got %q", calls[1].Opts.Dir)
	}
	// Each check streams its output live (Opts.Stdout set).
	for i, c := range calls {
		if c.Opts.Stdout == nil {
			t.Errorf("call %d: flake check should stream output (Opts.Stdout set)", i)
		}
	}
}

// TestFlakeCheck_InjectsLocalOverrides verifies that each repo is checked with
// --override-input flags derived from the lock edges for that consumer.
//
// Topology (lock edges):
//
//	base    has edge: base→overlay (alias=overlay)
//	overlay has edge: overlay→base (alias=base)
//	term    has edges: term→base (alias=base), term→overlay (alias=overlay)
//
// So each check gets the per-consumer overrides from the lock.
func TestFlakeCheck_InjectsLocalOverrides(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base", "overlay"} {
		mkRepoDir(t, root, r)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"

[repos.overlay]
url = "github:o/overlay"
`)
	// Lock with per-consumer edges:
	//   base    → overlay (alias "overlay")
	//   overlay → base    (alias "base")
	//   term    → base    (alias "base")
	//   term    → overlay (alias "overlay")
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["base", "overlay", "term"],
  "repos": {
    "base":    {"flake_path": "flake.nix", "remote_url": "github:o/base"},
    "overlay": {"flake_path": "flake.nix", "remote_url": "github:o/overlay"},
    "term":    {"flake_path": "flake.nix", "remote_url": "github:o/term"}
  },
  "edges": [
    {"consumer": "base",    "alias": "overlay", "target": "overlay"},
    {"consumer": "overlay", "alias": "base",    "target": "base"},
    {"consumer": "term",    "alias": "base",    "target": "base"},
    {"consumer": "term",    "alias": "overlay", "target": "overlay"}
  ]
}`)
	base := filepath.Join(root, "base")
	overlay := filepath.Join(root, "overlay")

	// Iteration is alphabetical: base, overlay, term.
	// base    → edges Consumer=base: alias=overlay,target=overlay
	//           overrideInputArgsFor("base", {ExcludeRepo:"base"}) → overlay edge (not excluded)
	// overlay → edges Consumer=overlay: alias=base,target=base
	//           overrideInputArgsFor("overlay", {ExcludeRepo:"overlay"}) → base edge (not excluded)
	// term    → edges Consumer=term: alias=base,target=base and alias=overlay,target=overlay
	//           overrideInputArgsFor("term", {ExcludeRepo:"term"}) → base + overlay
	wantArgs := [][]string{
		{"flake", "check", "--override-input", "overlay", "git+file://" + overlay},
		{"flake", "check", "--override-input", "base", "git+file://" + base},
		{
			"flake", "check",
			"--override-input", "base", "git+file://" + base,
			"--override-input", "overlay", "git+file://" + overlay,
		},
	}

	f := exec.NewFakeRunner()
	for _, a := range wantArgs {
		f.AddResponse("nix", a, exec.Result{}, nil)
	}

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.FlakeCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	wantDirs := []string{base, overlay, filepath.Join(root, "term")}
	for i, c := range calls {
		if !reflect.DeepEqual(c.Args, wantArgs[i]) {
			t.Errorf("call %d args:\n got %#v\nwant %#v", i, c.Args, wantArgs[i])
		}
		if c.Opts.Dir != wantDirs[i] {
			t.Errorf("call %d dir: got %q want %q", i, c.Opts.Dir, wantDirs[i])
		}
	}
}

// TestFlakeCheck_NoWarningWhenFlagSet asserts that when opts.Terminal is set
// and config.Workspace.Terminal is empty, no warning is emitted to errOut.
func TestFlakeCheck_NoWarningWhenFlagSet(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}
	if strings.Contains(errOut.String(), terminalWarningMessage) {
		t.Errorf("spurious warning emitted when --terminal flag is set; errOut=%q", errOut.String())
	}
}

// TestFlakeCheck_NoWarningWhenConfigTerminalSet asserts that when config has a
// terminal set (and no flag), no warning is emitted.
func TestFlakeCheck_NoWarningWhenConfigTerminalSet(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:owner/term"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.FlakeCheck(context.Background(), &out, &errOut, FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}
	if strings.Contains(errOut.String(), terminalWarningMessage) {
		t.Errorf("warning emitted even though config terminal is set; errOut=%q", errOut.String())
	}
}

func TestFlakeCheck_ContinuesPastFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// bar fails, foo passes — but BOTH must run.
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.FlakeCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, FlakeCheckOptions{})
	if err == nil {
		t.Fatal("expected error reporting failures, got nil")
	}
	if !strings.Contains(err.Error(), "bar") {
		t.Errorf("error should name the failing repo (bar); got %q", err.Error())
	}
	if len(f.Calls()) != 2 {
		t.Errorf("expected both repos to be attempted; got %d calls", len(f.Calls()))
	}
}
