package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestStatus_WritesPerRepoSections(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// bar comes first alphabetically.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte(" M file.txt\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "bar\n") {
		t.Errorf("missing bar header in output:\n%s", out)
	}
	if !strings.Contains(out, "(clean)") {
		t.Errorf("expected clean marker for empty status; got:\n%s", out)
	}
	if !strings.Contains(out, "foo\n") {
		t.Errorf("missing foo header in output:\n%s", out)
	}
	if !strings.Contains(out, " M file.txt") {
		t.Errorf("expected foo's git status output to be included; got:\n%s", out)
	}
	// A blank line separates the two repo blocks.
	if !strings.Contains(out, "\n\nfoo\n") {
		t.Errorf("expected a blank line between repo blocks; got:\n%s", out)
	}
	// Ordering: bar header should precede foo header (alphabetical).
	barIdx := strings.Index(out, "bar")
	fooIdx := strings.Index(out, "foo")
	if barIdx > fooIdx {
		t.Errorf("expected bar to appear before foo, got bar@%d foo@%d", barIdx, fooIdx)
	}
}

func TestStatus_ErrorIsNotFatal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128, Stderr: []byte("not a repo")}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{}); err != nil {
		t.Fatalf("Status should not return error on per-repo failure, got %v", err)
	}
	// Error output goes to errOut (stderr).
	if !strings.Contains(errBuf.String(), "(error)") {
		t.Errorf("expected error marker on stderr; got stdout:\n%s\nstderr:\n%s", buf.String(), errBuf.String())
	}
}

// TestStatus_TerminalFlagSuppressesWarning verifies that passing Terminal via
// StatusOptions suppresses the no-terminal warning even when config has no terminal.
func TestStatus_TerminalFlagSuppressesWarning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Status(context.Background(), &out, &errOut, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("--terminal flag should suppress warning; got stderr:\n%s", errOut.String())
	}
}

// TestStatus_ReportsBranchDeltaAndWorktrees verifies the enriched per-repo
// block: current branch name, ahead/behind delta, other local branches, and
// the worktree paths.
func TestStatus_ReportsBranchDeltaAndWorktrees(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte(" M f.txt\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}, exec.Result{Stdout: []byte("2\t1\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("feature-x\nmain\nold\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte("worktree " + repoDir + "\nHEAD abc\nbranch refs/heads/feature-x\n\nworktree /ws/wt-feature\nHEAD def\nbranch refs/heads/other\n")}, nil)
	// Deltas vs the default branch (main) for the linked worktree and the loose branches.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "refs/heads/other...refs/heads/main"}, exec.Result{Stdout: []byte("5\t3\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "refs/heads/main...refs/heads/main"}, exec.Result{Stdout: []byte("0\t0\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "refs/heads/old...refs/heads/main"}, exec.Result{Stdout: []byte("0\t7\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"feature-x  ↑2 ↓1", // primary line: branch + ahead/behind vs upstream
		" M f.txt",         // porcelain under the primary line
		"worktrees:",
		"wt-feature (other)  ↑5 ↓3", // linked worktree: basename (branch) + delta vs default
		"branches:",
		"old  ↑0 ↓7",  // loose branch: delta vs default
		"main  ↑0 ↓0", // main is loose here (checked out in no worktree) + delta vs default
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q; got:\n%s", want, out)
		}
	}
	// The primary worktree is not repeated in the worktrees section.
	if strings.Contains(out, filepath.Base(repoDir)+" (feature-x)") {
		t.Errorf("primary worktree should be excluded from worktrees section; got:\n%s", out)
	}
	// A branch checked out in a worktree (other) or the primary (feature-x)
	// must not appear as a loose branch (4-space indent).
	if strings.Contains(out, "    feature-x") {
		t.Errorf("current branch should not be listed as a loose branch; got:\n%s", out)
	}
	if strings.Contains(out, "    other\n") {
		t.Errorf("worktree-checked-out branch should not be listed as a loose branch; got:\n%s", out)
	}
}

// TestStatus_NoUpstreamStatedExplicitly verifies that a branch with no upstream
// reports "no upstream" rather than erroring, and that a sole (current) branch
// produces no "other branches" line.
func TestStatus_NoUpstreamStatedExplicitly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128, Stderr: []byte("no upstream configured")}})
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("main\n")}, nil)
	// worktree list intentionally not scripted: query fails and the line is omitted.

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "main  (no upstream)") {
		t.Errorf("expected explicit no-upstream marker; got:\n%s", out)
	}
	if !strings.Contains(out, "(clean)") {
		t.Errorf("expected clean marker; got:\n%s", out)
	}
	// main is the current (primary) branch, so it is checked out and must not
	// appear as a loose branch — the sole-branch repo shows no branches section.
	if strings.Contains(out, "branches:") {
		t.Errorf("sole current branch should produce no branches section; got:\n%s", out)
	}
	if strings.Contains(out, "worktrees:") {
		t.Errorf("failed worktree query should omit the worktrees section; got:\n%s", out)
	}
}

// TestStatus_DetachedHead verifies a detached HEAD is reported with its short sha.
func TestStatus_DetachedHead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("HEAD\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--short", "HEAD"}, exec.Result{Stdout: []byte("deadbee\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("main\n")}, nil)
	// worktree list omitted (query fails harmlessly).

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "(detached HEAD at deadbee)") {
		t.Errorf("expected detached-HEAD marker with short sha; got:\n%s", out)
	}
	// A detached HEAD has no current branch, so main is checked out nowhere and
	// is listed as a loose branch.
	if !strings.Contains(out, "branches:") || !strings.Contains(out, "    main") {
		t.Errorf("expected main listed as a loose branch under detached HEAD; got:\n%s", out)
	}
}

// TestStatus_WarningOnStderr verifies that the no-terminal warning goes to
// errOut (stderr) and not to stdout.
func TestStatus_WarningOnStderr(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out, errOut bytes.Buffer
	if err := w.Status(context.Background(), &out, &errOut, StatusOptions{}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(errOut.String(), "no terminal") {
		t.Errorf("expected no-terminal warning on stderr; got stderr:\n%s\nstdout:\n%s", errOut.String(), out.String())
	}
	if strings.Contains(out.String(), "no terminal") {
		t.Errorf("warning must not appear on stdout; got:\n%s", out.String())
	}
}

// TestStatus_DetachedWorktree verifies a linked worktree with a detached HEAD is
// rendered as "<dir> (detached <short-sha>)" with its ahead/behind vs the
// default branch — the worktrees-section detached path plus the porcelain
// "detached" parse.
func TestStatus_DetachedWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}, exec.Result{Stdout: []byte("0\t0\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte("worktree " + repoDir + "\nHEAD aaa\nbranch refs/heads/main\n\nworktree /ws/wt-x\nHEAD 0123456789abcdef\ndetached\n")}, nil)
	// Detached worktree ahead/behind is measured from its sha vs the default branch.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "0123456789abcdef...refs/heads/main"}, exec.Result{Stdout: []byte("1\t2\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "worktrees:") {
		t.Errorf("expected worktrees section; got:\n%s", out)
	}
	if !strings.Contains(out, "wt-x (detached 0123456)  ↑1 ↓2") {
		t.Errorf("expected detached worktree line with short sha and delta; got:\n%s", out)
	}
}

// TestStatus_DetachedHeadNoSha verifies the primary detached-HEAD line degrades
// to "(detached HEAD)" (no sha) when the short-sha query is unavailable.
func TestStatus_DetachedHeadNoSha(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("HEAD\n")}, nil)
	// rev-parse --short HEAD intentionally unscripted: query fails, sha stays "".
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("main\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "(detached HEAD)") {
		t.Errorf("expected bare detached-HEAD marker; got:\n%s", out)
	}
	if strings.Contains(out, "(detached HEAD at") {
		t.Errorf("empty sha should not render an 'at <sha>' suffix; got:\n%s", out)
	}
}

// TestStatus_MalformedDeltaOutput verifies both ahead/behind queries degrade
// gracefully on unexpected git output: a single-field upstream count makes the
// primary line report "(no upstream)", and a single-field default-branch count
// drops the arrow suffix on a loose branch rather than rendering a partial delta.
func TestStatus_MalformedDeltaOutput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", repoDir, "status", "--short"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("feature\n")}, nil)
	// Malformed: one field instead of two -> aheadBehindCounts reports not-ok.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}, exec.Result{Stdout: []byte("5\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.Result{Stdout: []byte("feature\nstale\n")}, nil)
	f.AddResponse("git", []string{"-C", repoDir, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte("worktree " + repoDir + "\nHEAD aaa\nbranch refs/heads/feature\n")}, nil)
	// Malformed: one field -> deltaArrows returns "" -> no suffix on the loose branch.
	f.AddResponse("git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "refs/heads/stale...refs/heads/main"}, exec.Result{Stdout: []byte("3\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf, errBuf bytes.Buffer
	if err := w.Status(context.Background(), &buf, &errBuf, StatusOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "feature  (no upstream)") {
		t.Errorf("malformed upstream count should fall back to (no upstream); got:\n%s", out)
	}
	if !strings.Contains(out, "    stale\n") {
		t.Errorf("expected loose branch 'stale' with no delta suffix; got:\n%s", out)
	}
	if strings.Contains(out, "stale  ↑") {
		t.Errorf("malformed default-branch count should drop the arrow suffix; got:\n%s", out)
	}
}

// TestWorktreeList_IgnoresAttributeLinesBeforeHeader verifies the porcelain
// parser skips stray attribute lines that appear before any "worktree " header
// rather than attributing them to a phantom entry.
func TestWorktreeList_IgnoresAttributeLinesBeforeHeader(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	repoDir := filepath.Join(root, "foo")

	f := exec.NewFakeRunner()
	// Leading HEAD/branch lines with no preceding "worktree " header must be ignored.
	f.AddResponse("git", []string{"-C", repoDir, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte("HEAD deadbeef\nbranch refs/heads/stray\n\nworktree " + repoDir + "\nHEAD aaa\nbranch refs/heads/main\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	list, ok := w.worktreeList(context.Background(), repoDir)
	if !ok {
		t.Fatalf("worktreeList returned ok=false")
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one worktree entry (stray lines ignored); got %d: %+v", len(list), list)
	}
	if list[0].branch != "main" {
		t.Errorf("expected the real entry's branch to be main; got %q", list[0].branch)
	}
	for _, wt := range list {
		if wt.branch == "stray" {
			t.Errorf("stray pre-header attribute lines must not form an entry; got:\n%+v", list)
		}
	}
}
