package tui

import (
	"fmt"
	"strings"
	"time"
)

// QueuePackageRow is one package's grouping in the Queue popup: a package
// path and the files within it still queued to run.
type QueuePackageRow struct {
	PkgPath string
	Files   []string
}

// QueueState is the state RenderQueue renders.
type QueueState struct {
	Packages []QueuePackageRow
}

// HistoryRow is one completed run's row in the History popup. Killed and
// Survived are the per-run kill/survived counts (design's exempted case,
// §9) — a later task populates these two ints by parsing a completed run's
// report JSON; this task only defines the field and renders it.
type HistoryRow struct {
	Status   string
	File     string
	Killed   int
	Survived int
	Elapsed  time.Duration
}

// HistoryState is the state RenderHistory renders.
type HistoryState struct {
	Rows []HistoryRow
}

// BeadRow is one bead's row in the Beads popup.
type BeadRow struct {
	ID     string
	Title  string
	Status string
}

// BeadsState is the state RenderBeads renders.
type BeadsState struct {
	Rows []BeadRow
}

// RenderQueue renders the Queue popup (files grouped by package) as plain
// text. It is a pure function, matching RenderPrimary's pattern.
func RenderQueue(s QueueState) string {
	var b strings.Builder

	b.WriteString("QUEUE\n")
	for _, p := range s.Packages {
		fmt.Fprintf(&b, "%s\n", p.PkgPath)
		for _, f := range p.Files {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	b.WriteString("\n[esc] back\n")

	return b.String()
}

// RenderHistory renders the History popup (completed runs, including the
// per-run kill/survived counts) as plain text. It is a pure function,
// matching RenderPrimary's pattern.
func RenderHistory(s HistoryState) string {
	var b strings.Builder

	b.WriteString("HISTORY\n")
	for _, r := range s.Rows {
		fmt.Fprintf(&b, "  %-6s %-40s killed %d  survived %d  %s elapsed\n",
			r.Status, r.File, r.Killed, r.Survived, formatMMSS(r.Elapsed))
	}
	b.WriteString("\n[esc] back\n")

	return b.String()
}

// RenderBeads renders the Beads popup as plain text. It is a pure function,
// matching RenderPrimary's pattern.
func RenderBeads(s BeadsState) string {
	var b strings.Builder

	b.WriteString("BEADS\n")
	for _, r := range s.Rows {
		fmt.Fprintf(&b, "  %-10s %-8s %s\n", r.ID, r.Status, r.Title)
	}
	b.WriteString("\n[esc] back\n")

	return b.String()
}
