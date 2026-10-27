package beads

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/report"
)

type createdBead struct {
	title, body string
	labels      []string
	priority    int
}

// fakeBd records Create calls. It is only for the pure body/label logic in this
// file; the contract with the real bd is proved in realbd_test.go.
type fakeBd struct {
	created []createdBead
	err     error
}

func (f *fakeBd) Create(title, body string, labels []string, priority int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, createdBead{title, body, labels, priority})
	return fmt.Sprintf("pg2-fake%d", len(f.created)), nil
}

func sampleTriage() Triage {
	return Triage{
		Module:  "modules/gate",
		PkgPath: "internal/gate",
		Files: []FileResult{
			{
				File: "internal/gate/a.go", Status: "done", ReportPath: "/state/reports/a.go.json",
				Worklist: report.Worklist{Survivors: []report.Survivor{
					{Line: 12, Type: "conditional", Description: `"!=" -> "=="`},
					{Line: 40, Type: "error_nilify", Description: "return err ->\n return nil"},
				}},
			},
			{File: "internal/gate/b.go", Status: "no-tests", ReportPath: "/state/reports/b.go.json"},
		},
	}
}

func TestFileTriageBeadUsesCanonicalLabelNotPathDerived(t *testing.T) {
	bd := &fakeBd{}
	if _, err := FileTriageBead(bd, sampleTriage(), "base"); err != nil {
		t.Fatal(err)
	}
	labels := bd.created[0].labels
	if len(labels) != 2 || labels[0] != "go-test-gaps" || labels[1] != "base" {
		t.Fatalf("labels = %v, want exactly [go-test-gaps base]", labels)
	}
}

func TestFileTriageBeadRefusesAnEmptyRepoLabel(t *testing.T) {
	bd := &fakeBd{}
	if _, err := FileTriageBead(bd, sampleTriage(), "  "); err == nil {
		t.Fatal("an unlabeled bead must not be filed")
	}
	if len(bd.created) != 0 {
		t.Fatal("nothing may be created without a repo label")
	}
}

func TestFileTriageBeadIsPriorityThreeTitledByPackage(t *testing.T) {
	bd := &fakeBd{}
	if _, err := FileTriageBead(bd, sampleTriage(), "base"); err != nil {
		t.Fatal(err)
	}
	if bd.created[0].priority != 3 {
		t.Fatalf("expected P3, got P%d", bd.created[0].priority)
	}
	if bd.created[0].title != "go-test-gaps triage: internal/gate" {
		t.Fatalf("title = %q", bd.created[0].title)
	}
}

func TestFileTriageBeadPropagatesTheClientError(t *testing.T) {
	bd := &fakeBd{err: fmt.Errorf("bd is down")}
	if _, err := FileTriageBead(bd, sampleTriage(), "base"); err == nil {
		t.Fatal("a Create failure must reach the caller")
	}
}

func TestBodyNamesPackageCarriesRowsAndReportPaths(t *testing.T) {
	body, err := Body(sampleTriage())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`internal/gate`",
		"`modules/gate`",
		"pg-go-mutate --json",
		"`internal/gate/a.go:12` | \"!=\" -> \"==\" | conditional",
		"`internal/gate/a.go:40` | return err -> return nil | error_nilify",
		"`/state/reports/a.go.json`",
		"`/state/reports/b.go.json`",
		"`internal/gate/b.go`: no-tests",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q\n%s", want, body)
		}
	}
}

func TestBodyCarriesTheSkillsHowToWorkThisBeadBlockVerbatim(t *testing.T) {
	golden, err := os.ReadFile("testdata/how-to-work-this-bead.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(golden) != HowToWorkThisBead {
		t.Fatalf("HowToWorkThisBead drifted from the go-test-gaps skill's block.\nconst:\n%s\ngolden:\n%s", HowToWorkThisBead, golden)
	}
	body, err := Body(sampleTriage())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, string(golden)) {
		t.Fatal("body must contain the block verbatim")
	}
	over, err := Body(hugeTriage())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(over, string(golden)) {
		t.Fatal("the over-the-cap body must still contain the block verbatim")
	}
}

func TestBodyNeverCarriesCountsPercentagesOrScores(t *testing.T) {
	tr := sampleTriage()
	tr.Files[0].Worklist.Partial = true
	for _, body := range []string{mustBody(t, tr), mustBody(t, hugeTriage())} {
		lower := strings.ToLower(body)
		for _, banned := range []string{"survived", "killed", "score", "percent", "%", "total", "count", "tally", "not-viable:", "timedout"} {
			if strings.Contains(lower, banned) {
				t.Errorf("body contains %q", banned)
			}
		}
	}
}

func mustBody(t *testing.T, tr Triage) string {
	t.Helper()
	b, err := Body(tr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// hugeTriage is a package whose rows alone exceed bd's 64 KB body cap.
func hugeTriage() Triage {
	var survivors []report.Survivor
	for i := 1; i <= 2000; i++ {
		survivors = append(survivors, report.Survivor{Line: i, Type: "conditional", Description: strings.Repeat("x", 40)})
	}
	return Triage{
		Module: "m", PkgPath: "big",
		Files: []FileResult{{File: "big/a.go", Status: "done", ReportPath: "/state/reports/big-a.go.json", Worklist: report.Worklist{Survivors: survivors}}},
	}
}

func TestBodyOverTheCapCarriesReportPathsInsteadOfRows(t *testing.T) {
	body := mustBody(t, hugeTriage())
	if len(body) > bodyCap {
		t.Fatalf("body is %d bytes, over the cap %d", len(body), bodyCap)
	}
	if strings.Contains(body, "`big/a.go:7`") {
		t.Fatal("rows must be dropped when they exceed the cap")
	}
	if !strings.Contains(body, "`/state/reports/big-a.go.json`") || !strings.Contains(body, "too large for one bead body") {
		t.Fatal("report paths and the reason must replace the rows")
	}
}

func TestBodyErrorsWhenEvenReportPathsExceedTheCap(t *testing.T) {
	tr := Triage{Module: "m", PkgPath: "p"}
	for i := 0; i < 1000; i++ {
		tr.Files = append(tr.Files, FileResult{File: fmt.Sprintf("p/f%d.go", i), Status: "done", ReportPath: "/state/" + strings.Repeat("r", 100) + fmt.Sprintf("%d.json", i)})
	}
	if _, err := Body(tr); err == nil {
		t.Fatal("a body that cannot fit even with report paths only must be an error, not truncated silently")
	}
}

func TestBodyFlagsPartialUnreadableAndBuildTagCaveats(t *testing.T) {
	tr := Triage{Module: "m", PkgPath: "p", Files: []FileResult{
		{File: "p/a.go", Status: "done", ReportPath: "/r/a.json", Worklist: report.Worklist{Partial: true, BuildTagsNotRun: "contract"}},
		{File: "p/b.go", Status: "done", ReportPath: "/r/b.json", ReportUnreadable: true},
	}}
	body := mustBody(t, tr)
	for _, want := range []string{"not decisively analysed", "could not be read", "tests behind those build tags were not run", "contract"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}

func TestBodyOfACleanPackageSaysNoSurvivorsWereReported(t *testing.T) {
	tr := Triage{Module: "m", PkgPath: "p", Files: []FileResult{{File: "p/a.go", Status: "done", ReportPath: "/r/a.json"}}}
	if !strings.Contains(mustBody(t, tr), "No file of this package reported a surviving mutation.") {
		t.Fatal("a clean package must say so")
	}
}
