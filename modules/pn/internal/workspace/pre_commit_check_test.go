package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPreCommitCheck_PerRepoInOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
[[repos.foo.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.bar]
url = "github:owner/bar"
[[repos.bar.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.PreCommitCheck(context.Background(), &out, &errOut, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("expected bar first; dir=%q", calls[0].Opts.Dir)
	}
	if calls[1].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("expected foo second; dir=%q", calls[1].Opts.Dir)
	}
	for i, c := range calls {
		if c.Opts.Stdout == nil {
			t.Errorf("call %d: pre-commit should stream output (Opts.Stdout set)", i)
		}
	}
}

// TestPreCommitCheck_NoWarningWhenFlagSet asserts that when opts.Terminal is
// set and config.Workspace.Terminal is empty, no warning is emitted to errOut.
func TestPreCommitCheck_NoWarningWhenFlagSet(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
[[repos.foo.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.PreCommitCheck(context.Background(), &out, &errOut, PreCommitCheckOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	if strings.Contains(errOut.String(), terminalWarningMessage) {
		t.Errorf("spurious warning emitted when --terminal flag is set; errOut=%q", errOut.String())
	}
}

// TestPreCommitCheck_NoWarningWhenConfigTerminalSet asserts that when config
// has a terminal set (and no flag), no warning is emitted.
func TestPreCommitCheck_NoWarningWhenConfigTerminalSet(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "term")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:owner/term"
[[repos.term.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.PreCommitCheck(context.Background(), &out, &errOut, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	if strings.Contains(errOut.String(), terminalWarningMessage) {
		t.Errorf("warning emitted even though config terminal is set; errOut=%q", errOut.String())
	}
}

func TestPreCommitCheck_ContinuesPastFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
[[repos.foo.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.bar]
url = "github:owner/bar"
[[repos.bar.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
`)

	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "pg-hooks", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PreCommitCheckOptions{}); err == nil {
		t.Fatal("expected combined error from per-repo failure")
	}
	if len(f.Calls()) != 2 {
		t.Errorf("expected both repos attempted; got %d calls", len(f.Calls()))
	}
}

// preCommitCheckRepo builds a workspace whose single repo "foo" is a real git
// repo, and returns the workspace plus the fake runner.
func preCommitCheckRepo(t *testing.T) (*Workspace, *exec.FakeRunner, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:owner/foo\"\n"+declareInstallHook("foo"))
	foo := filepath.Join(root, "foo")
	initRealRepo(t, foo)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, f, foo
}

func TestPreCommitCheck_OldConfigRepoRunsPgHooksNotPrek(t *testing.T) {
	// R4: a clone holding only an old .pre-commit-config.yaml has no bundle. It
	// goes to pg-hooks (which reports the missing bundle), never to prek.
	w, f, foo := preCommitCheckRepo(t)
	writeFile(t, filepath.Join(foo, ".pre-commit-config.yaml"), "repos: []\n")
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	if err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "pg-hooks" || calls[0].Opts.Dir != foo {
		t.Fatalf("an old-config repo must run `pg-hooks run pre-commit --all-files` in the clone; calls=%+v", calls)
	}
}

func TestPreCommitCheck_BundleRepoRunsPgHooks(t *testing.T) {
	w, f, foo := preCommitCheckRepo(t)
	writeFakeBundle(t, foo, hbBundleOpts{})
	// A leftover old config must not pull a bundle repo back to prek.
	writeFile(t, filepath.Join(foo, ".pre-commit-config.yaml"), "repos: []\n")
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)

	if err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "pg-hooks" {
		t.Fatalf("bundle repo must run pg-hooks; calls=%+v", calls)
	}
}

func TestPreCommitCheck_NoBundleNoConfigRunsPgHooks(t *testing.T) {
	// State missing: pg-hooks prints the spec 5.4 notice and exits 13, which
	// PreCommitCheck reports as a failure rather than silently passing.
	w, f, _ := preCommitCheckRepo(t)
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{ExitCode: 13},
		&exec.CommandError{Name: "pg-hooks", Result: exec.Result{ExitCode: 13}})

	err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PreCommitCheckOptions{})
	if err == nil || !strings.Contains(err.Error(), "pg-hooks in foo") {
		t.Fatalf("a missing bundle must surface pg-hooks' exit as an error naming the repo; got %v", err)
	}
}

func TestPreCommitCheck_NeverRunsPreCommit(t *testing.T) {
	// Every shape (bundle, old config only, nothing, not a git tree) avoids
	// `pre-commit` and `prek`.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.a]
url = "github:owner/a"
[[repos.a.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.b]
url = "github:owner/b"
[[repos.b.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.c]
url = "github:owner/c"
[[repos.c.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.d]
url = "github:owner/d"
[[repos.d.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
`)
	initRealRepo(t, filepath.Join(root, "a"))
	writeFakeBundle(t, filepath.Join(root, "a"), hbBundleOpts{})
	initRealRepo(t, filepath.Join(root, "b"))
	writeFile(t, filepath.Join(root, "b", ".pre-commit-config.yaml"), "repos: []\n")
	initRealRepo(t, filepath.Join(root, "c"))
	// d is not created at all.
	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.PreCommitCheck(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	var pgh int
	for _, c := range f.Calls() {
		switch c.Name {
		case "pg-hooks":
			pgh++
		default:
			t.Errorf("unexpected command %q", c.Name)
		}
	}
	if pgh != 4 {
		t.Errorf("want 4 pg-hooks calls (a, b, c, d); got %d", pgh)
	}
}

// declareInstallHook returns the toml that opts repo into the pg-hooks bundle
// (a per-repo hook running install-pre-commit-hooks), which is what makes
// pre-commit-check run `pg-hooks` there (see installHookDeclared).
func declareInstallHook(repo string) string {
	return "[[repos." + repo + ".hooks]]\nwhen = [\"post-clone\"]\nrun = [\"{nix_run install-pre-commit-hooks}\"]\n"
}

// TestPreCommitCheck_SkipsRepoThatDeclaresNoInstallHook: a repo with no
// install-pre-commit-hooks hook (the Go-only foundation repo, ADR-0033) has no
// bundle by design; running pg-hooks there would record exit 13 as a failure.
func TestPreCommitCheck_SkipsRepoThatDeclaresNoInstallHook(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.declared]
url = "github:owner/declared"
`+declareInstallHook("declared")+`
[repos.undeclared]
url = "github:owner/undeclared"
foundation = true
`)
	f := exec.NewFakeRunner()
	f.AddResponse("pg-hooks", []string{"run", "pre-commit", "--all-files"}, exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.PreCommitCheck(context.Background(), &out, &bytes.Buffer{}, PreCommitCheckOptions{}); err != nil {
		t.Fatalf("PreCommitCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Opts.Dir != filepath.Join(root, "declared") {
		t.Fatalf("only the declared repo may run pg-hooks; calls=%+v", calls)
	}
	if !strings.Contains(out.String(), "undeclared") || !strings.Contains(out.String(), "skipped") {
		t.Errorf("a one-line skip notice naming the repo is expected; out=%q", out.String())
	}
}
