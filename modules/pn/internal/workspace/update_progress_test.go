package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func progressWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	return root
}

func requireMatch(t *testing.T, out, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(out) {
		t.Errorf("output missing %q; got:\n%s", pattern, out)
	}
}

// bar (1/2) fails in update-locks, foo (2/2) succeeds.
func TestUpdate_ProgressLines_SuccessAndFailure(t *testing.T) {
	root := progressWorkspace(t)
	bar, foo := filepath.Join(root, "bar"), filepath.Join(root, "foo")
	stubGitOpener(t, map[string]*fakeGitReader{bar: {hasUpstreamVal: true}, foo: {hasUpstreamVal: true}})
	f := exec.NewFakeRunner()
	mkUpdateLocks(t, bar)
	mkUpdateLocks(t, foo)
	for _, d := range []string{bar, foo} {
		f.AddResponse("git", []string{"-C", d, "diff", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
		f.AddResponse("git", []string{"-C", d, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	}
	f.AddResponse("./update-locks.sh", nil, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "./update-locks.sh", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := w.Update(context.Background(), &buf, UpdateOptions{InPlace: true}); err == nil {
		t.Fatal("expected failure error")
	}
	out := buf.String()
	requireMatch(t, out, `\[1/2\] bar: starting`)
	requireMatch(t, out, `\[1/2\] bar: failed \(\d+\.\ds\)`)
	requireMatch(t, out, `✗ bar: update FAILED at step update-locks`)
	requireMatch(t, out, `\[bar\] update-locks\.sh \(runs nix flake update\): started`)
	requireMatch(t, out, `\[bar\] update-locks\.sh \(runs nix flake update\): finished, failed \(\d+\.\ds\)`)
	requireMatch(t, out, `\[2/2\] foo: starting`)
	requireMatch(t, out, `\[2/2\] foo: ok \(\d+\.\ds\)`)
	requireMatch(t, out, `\[foo\] update-locks\.sh \(runs nix flake update\): finished, ok \(\d+\.\ds\)`)
	if strings.Contains(out, "✗ foo") {
		t.Errorf("passing repo must not print a failure line:\n%s", out)
	}
	requireMatch(t, out, `--== update bar ==--`)
	requireMatch(t, out, `--== update foo ==--`)
}

func TestUpdate_ProgressLines_Skipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	w, err := Open(root, f)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := w.Update(context.Background(), &buf, UpdateOptions{InPlace: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	requireMatch(t, out, `\[1/1\] foo: starting`)
	requireMatch(t, out, `⊘ skipping foo — working tree has uncommitted changes`)
	requireMatch(t, out, `\[1/1\] foo: skipped \(\d+\.\ds\)`)
}
