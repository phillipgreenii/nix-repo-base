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
