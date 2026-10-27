// Package report reads a completed pg-go-mutate run's --json report, for
// populating the TUI's local History popup only -- its counts MUST NOT be
// fed into internal/metrics or any other exported/durable record (design's
// no survivor/kill-count-outside-the-local-popup rule).
package report

import (
	"encoding/json"
	"os"
)

// reportJSON mirrors pg-go-mutate's real worklist schema
// (modules/pg-go-mutate/lib/pg-go-mutate-lib.bash's pgm_worklist_json): a
// top-level "statistics" object with plain "killed"/"survived" int fields --
// not "killedMutants"/"survivedMutants", which was an unverified guess in
// this task's own planning doc.
type reportJSON struct {
	Statistics struct {
		Killed   int `json:"killed"`
		Survived int `json:"survived"`
	} `json:"statistics"`
}

// ParseSurvivorCount reads the pg-go-mutate --json report at reportPath and
// returns its killed/survived mutant counts.
func ParseSurvivorCount(reportPath string) (killed, survived int, err error) {
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return 0, 0, err
	}
	var r reportJSON
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, 0, err
	}
	return r.Statistics.Killed, r.Statistics.Survived, nil
}

// Survivor is one worklist row of a pg-go-mutate --json report: a mutation the
// package's tests failed to catch. Line, Description (the mutation) and Type
// (the mutation operator) are exactly the fields a triage bead carries. The
// report's own "file" field is deliberately not read: a file-target run
// reports it relative to the target, which can be empty, and the caller
// already knows which file the report belongs to.
type Survivor struct {
	Line        int    `json:"line"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Worklist is what a triage bead takes from one report: the survivor rows, and
// Partial, which is true when the run did not decisively analyse everything
// (errored, timed-out or not-viable mutants). Partial is a yes/no flag on
// purpose; the underlying counts are run health and MUST NOT be recorded in a
// bead (go-test-gaps skill, "Reading the worklist").
type Worklist struct {
	Survivors []Survivor
	Partial   bool
	// BuildTagsNotRun is the report's build-tag caveat ("" when absent): tests
	// behind those tags were not run, so some survivors may be false gaps.
	BuildTagsNotRun string
}

type worklistJSON struct {
	Survivors  []Survivor `json:"survivors"`
	Statistics struct {
		NotViable int `json:"notViable"`
		TimedOut  int `json:"timedOut"`
		Errors    int `json:"errors"`
	} `json:"statistics"`
	BuildTagsNotRun *string `json:"buildTagsNotRun"`
}

// ParseWorklist reads the pg-go-mutate --json report at reportPath and returns
// its worklist. It reads the survivor rows and the run-health flag only; it
// never returns a killed/survived count.
func ParseWorklist(reportPath string) (Worklist, error) {
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return Worklist{}, err
	}
	var r worklistJSON
	if err := json.Unmarshal(raw, &r); err != nil {
		return Worklist{}, err
	}
	w := Worklist{
		Survivors: r.Survivors,
		Partial:   r.Statistics.NotViable > 0 || r.Statistics.TimedOut > 0 || r.Statistics.Errors > 0,
	}
	if r.BuildTagsNotRun != nil {
		w.BuildTagsNotRun = *r.BuildTagsNotRun
	}
	return w, nil
}
