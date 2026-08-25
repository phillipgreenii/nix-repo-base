// Package beads files a single per-package triage bead once pg-go-mutate-tui
// has finished analysing every file in a package. It never files an epic —
// this package only ever calls BdClient.Create — and the bead body carries
// the status tally only, never a survivor/kill count.
package beads

import "fmt"

// BdClient is the seam over the real `bd` binary, so this package's own
// tests never shell out to it.
type BdClient interface {
	Create(title, body string, labels []string, priority int) (string, error)
}

// FileTriageBead files a P3 triage bead for pkgPath, carrying the status
// tally (e.g. done/failed/timeout counts) in its body. repoLabel is a plain
// string supplied by the caller — it is NOT derived from pkgPath — so the
// bead always carries the canonical repo label rather than a raw path
// component.
func FileTriageBead(bd BdClient, pkgPath string, tally map[string]int, repoLabel string) (string, error) {
	title := fmt.Sprintf("go-test-gaps triage: %s", pkgPath)
	body := fmt.Sprintf("pg-go-mutate-tui has analysed every file in `%s`.\n\nFiles by status:\n", pkgPath)
	for status, count := range tally {
		body += fmt.Sprintf("  - %s: %d\n", status, count)
	}
	return bd.Create(title, body, []string{"go-test-gaps", repoLabel}, 3)
}
