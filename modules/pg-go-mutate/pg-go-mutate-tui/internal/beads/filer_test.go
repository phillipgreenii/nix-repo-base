package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
)

// fixture is a repository with one Go module holding one package of two source
// files and a test file. The `.git` entry is a bare marker: Filer only looks
// for it to find the repository root, and never runs git.
type fixture struct {
	repo, module, pkg string
	a, b              string
	stateDir          string
	ledger            *ledger.Ledger
}

func newFixture(t *testing.T, claudeMD string) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{repo: filepath.Join(root, "some-checkout")}
	f.module = filepath.Join(f.repo, "modules", "gate")
	f.pkg = filepath.Join(f.module, "internal", "gate")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if claudeMD != "" {
		write(filepath.Join(f.repo, "CLAUDE.md"), claudeMD)
	}
	write(filepath.Join(f.module, "go.mod"), "module fixture\n\ngo 1.22\n")
	f.a = filepath.Join(f.pkg, "a.go")
	f.b = filepath.Join(f.pkg, "b.go")
	write(f.a, "package gate\n")
	write(f.b, "package gate\n")
	write(filepath.Join(f.pkg, "a_test.go"), "package gate\n")
	f.stateDir = filepath.Join(root, "state")
	f.ledger = ledger.New(filepath.Join(f.stateDir, "ledger.jsonl"))
	return f
}

func (f *fixture) record(t *testing.T, file, hash, status, reportPath string) {
	t.Helper()
	if err := f.ledger.Append(ledger.Record{FilePath: file, PackageHash: hash, Status: status, ReportPath: reportPath}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) filer(bd BdClient, labels map[string]string) *Filer {
	return &Filer{
		Ledger:     f.ledger,
		NewClient:  func(string) BdClient { return bd },
		RepoLabels: labels,
		FiledPath:  filepath.Join(f.stateDir, "triage-filed.jsonl"),
	}
}

const claudeWithLabel = "# Rules\n\nEvery bead filed against this repo MUST carry the repo label `base`.\n"

func TestFilerFilesNothingUntilEveryFileInThePackageHasAStatus(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	bd := &fakeBd{}
	fl := fx.filer(bd, nil)

	fx.record(t, fx.a, "h1", "done", "")
	fl.OnRecorded(fx.a)
	if len(bd.created) != 0 {
		t.Fatalf("filed with one of two files recorded: %d beads", len(bd.created))
	}

	fx.record(t, fx.b, "h1", "no-tests", "")
	fl.OnRecorded(fx.b)
	if len(bd.created) != 1 {
		t.Fatalf("want exactly one bead once both files are recorded, got %d", len(bd.created))
	}
	c := bd.created[0]
	if c.title != "go-test-gaps triage: internal/gate" || c.priority != 3 {
		t.Fatalf("bead = %+v", c)
	}
	if got := strings.Join(c.labels, ","); got != "go-test-gaps,base" {
		t.Fatalf("labels = %s", got)
	}
	if !strings.Contains(c.body, "`modules/gate`") {
		t.Fatalf("body must name the module relative to the repo root:\n%s", c.body)
	}
}

func TestFilerFilesAtMostOneBeadPerPackageAcrossRepeatsAndRestarts(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	bd := &fakeBd{}
	fl := fx.filer(bd, nil)
	fx.record(t, fx.a, "h1", "done", "")
	fx.record(t, fx.b, "h1", "done", "")
	fl.OnRecorded(fx.b)
	fl.OnRecorded(fx.a) // a second worker finishing at the same moment
	fl.OnRecorded(fx.b)
	if len(bd.created) != 1 {
		t.Fatalf("repeat events filed %d beads, want 1", len(bd.created))
	}

	restarted := fx.filer(bd, nil) // same marker file, fresh process
	restarted.OnRecorded(fx.a)
	if len(bd.created) != 1 {
		t.Fatalf("a restart filed a second bead: %d", len(bd.created))
	}
}

func TestFilerWaitsWhenTheFilesWereAnalysedAtDifferentPackageHashes(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	bd := &fakeBd{}
	fl := fx.filer(bd, nil)
	fx.record(t, fx.a, "h1", "done", "")
	fx.record(t, fx.b, "h2", "done", "") // the package was edited between the two runs
	fl.OnRecorded(fx.b)
	if len(bd.created) != 0 {
		t.Fatal("a package analysed at two different contents has no consistent run to hand over")
	}
	fx.record(t, fx.a, "h2", "done", "") // re-analysed
	fl.OnRecorded(fx.a)
	if len(bd.created) != 1 {
		t.Fatalf("want one bead after the package is consistent, got %d", len(bd.created))
	}
}

func TestFilerFilesAgainWhenThePackageIsReanalysedAtANewHash(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	bd := &fakeBd{}
	fl := fx.filer(bd, nil)
	fx.record(t, fx.a, "h1", "done", "")
	fx.record(t, fx.b, "h1", "done", "")
	fl.OnRecorded(fx.b)
	fx.record(t, fx.a, "h2", "done", "")
	fx.record(t, fx.b, "h2", "done", "")
	fl.OnRecorded(fx.b)
	if len(bd.created) != 2 {
		t.Fatalf("a new analysis of changed code is a new result to triage; got %d beads", len(bd.created))
	}
}

func TestFilerRetriesAFailedFilingOnTheNextEvent(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	bd := &fakeBd{err: fmt.Errorf("bd is down")}
	fl := fx.filer(bd, nil)
	fx.record(t, fx.a, "h1", "done", "")
	fx.record(t, fx.b, "h1", "done", "")
	fl.OnRecorded(fx.b)
	if len(bd.created) != 0 {
		t.Fatal("nothing can be created while bd is down")
	}
	bd.err = nil
	// An unrelated package's file arrives; the pending package is retried.
	other := filepath.Join(fx.module, "internal", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.record(t, filepath.Join(other, "x.go"), "h9", "done", "")
	fl.OnRecorded(filepath.Join(other, "x.go"))
	titles := map[string]bool{}
	for _, c := range bd.created {
		titles[c.title] = true
	}
	if !titles["go-test-gaps triage: internal/gate"] || len(bd.created) != 2 {
		t.Fatalf("the failed package must be filed on the next event: %+v", titles)
	}
}

func TestFilerNeverFilesWithoutAResolvableRepoLabel(t *testing.T) {
	fx := newFixture(t, "# no label stated here\n")
	bd := &fakeBd{}
	fl := fx.filer(bd, nil)
	fx.record(t, fx.a, "h1", "done", "")
	fx.record(t, fx.b, "h1", "done", "")
	fl.OnRecorded(fx.b)
	if len(bd.created) != 0 {
		t.Fatal("the label must never be guessed from the path: no label, no bead")
	}
}

func TestResolveRepoLabelPrefersConfigThenClaudeMDNeverTheDirectoryName(t *testing.T) {
	fx := newFixture(t, claudeWithLabel)
	got, err := ResolveRepoLabel(fx.repo, map[string]string{"some-checkout": "configured"})
	if err != nil || got != "configured" {
		t.Fatalf("config entry must win: %q, %v", got, err)
	}
	got, err = ResolveRepoLabel(fx.repo, nil)
	if err != nil || got != "base" {
		t.Fatalf("CLAUDE.md label expected: %q, %v", got, err)
	}
	if got == filepath.Base(fx.repo) {
		t.Fatal("label must not be the directory name")
	}
	bare := newFixture(t, "")
	if _, err := ResolveRepoLabel(bare.repo, nil); err == nil {
		t.Fatal("a repo with no config entry and no CLAUDE.md is an error")
	}
}

func TestFindUpAcceptsAGitFileForALinkedWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findUp(deep, ".git"); got != root {
		t.Fatalf("findUp = %q, want %q", got, root)
	}
	if got := findUp(deep, "no-such-marker-anywhere"); got != "" {
		t.Fatalf("findUp for an absent marker = %q, want empty", got)
	}
}
