// Package tui implements the pg-go-mutate-tui terminal interface: one
// primary screen (project tree + active runs) plus popups added by later
// tasks. Each screen's rendering is a pure string-building function, kept
// separate from the bubbletea Model/Update/View glue, so it is testable with
// plain string assertions and no terminal.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ProjectNode is one row of the primary screen's project/package tree. A
// node with Children represents a project containing packages (or a package
// containing further grouping); a leaf node is a package.
type ProjectNode struct {
	Name     string
	Included bool
	Packages int
	Files    int
	Tests    int
	Children []ProjectNode
}

// ActiveRun is one row of the primary screen's ACTIVE section: a file
// currently dispatched to a worker, and how long it has been running.
type ActiveRun struct {
	File    string
	Elapsed time.Duration
}

// PrimaryState is the state RenderPrimary renders. A later task's
// integration wiring populates it from the live Queue/Pool/Ledger; this
// struct and RenderPrimary have no dependency on that wiring and are pure.
type PrimaryState struct {
	Projects       []ProjectNode
	Active         []ActiveRun
	Concurrency    int
	ConcurrencyMax int
	QueueDepth     int
	LowWatermark   int
	HighWatermark  int
	Paused         bool
	RetryCountdown *time.Duration
}

// formatMMSS renders a non-negative duration as zero-padded MM:SS, e.g.
// 4m12s -> "04:12", 42s -> "00:42".
func formatMMSS(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	if total < 0 {
		total = 0
	}
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}

// RenderPrimary renders the primary screen (project tree + active runs) as
// plain text. It is a pure function, deliberately separated from the
// bubbletea Model/Update/View glue so it is testable with plain string
// assertions and no terminal.
func RenderPrimary(s PrimaryState) string {
	var b strings.Builder

	b.WriteString("pg-go-mutate-tui\n")
	status := "RUNNING"
	if s.Paused {
		status = "PAUSED"
	}
	fmt.Fprintf(&b, "concurrency %d/%d active   queue %s   %s\n",
		s.Concurrency, s.ConcurrencyMax, renderQueueLine(s), status)

	fmt.Fprintf(&b, "\nPROJECTS%*spkgs  files  tests\n", 44, "")
	for _, p := range s.Projects {
		renderProjectNode(&b, p, 0)
	}

	fmt.Fprintf(&b, "\nACTIVE (%d/%d)\n", len(s.Active), s.ConcurrencyMax)
	for _, a := range s.Active {
		fmt.Fprintf(&b, "  %-60s %s elapsed\n", a.File, formatMMSS(a.Elapsed))
	}

	b.WriteString("\n[↑↓] navigate  [tab] expand/collapse  [space] toggle selection  [R] force reload\n")
	b.WriteString("[Q] queue  [H] history  [B] beads  [p] pause/resume  [c] concurrency  [q] quit\n")

	return b.String()
}

// renderQueueLine renders the header's queue-depth clause. Per design §8 the
// countdown/search text is edge-triggered on crossing the low mark and MUST
// NOT appear while the queue is at or above it.
func renderQueueLine(s PrimaryState) string {
	plain := fmt.Sprintf("%d pending (low %d / high %d)", s.QueueDepth, s.LowWatermark, s.HighWatermark)
	if s.QueueDepth >= s.LowWatermark {
		return plain
	}
	if s.RetryCountdown != nil {
		return fmt.Sprintf("%s — nothing found, retrying in %s", plain, formatMMSS(*s.RetryCountdown))
	}
	return fmt.Sprintf("%s — searching for more...", plain)
}

// renderProjectNode writes one tree row (and, recursively, its children)
// to b, indenting each level of nesting.
func renderProjectNode(b *strings.Builder, n ProjectNode, depth int) {
	mark := "☐" // ☐
	if n.Included {
		mark = "☑" // ☑
	}
	indent := strings.Repeat("  ", depth)
	label := indent + mark + " " + n.Name
	fmt.Fprintf(b, " %-52s %5d  %5d  %5d\n", label, n.Packages, n.Files, n.Tests)
	for _, c := range n.Children {
		renderProjectNode(b, c, depth+1)
	}
}

// Model is the bubbletea model for the primary screen. Its View method is a
// thin wrapper that calls RenderPrimary; a later task's integration wiring
// populates its State from the live Queue/Pool/Ledger. This task's Model
// only needs to compile and route key events for the primary screen's own
// keys — Task 15 gives Q/H/B real meaning (popup switching), and Task 16
// wires real state (live pool pause/resume, concurrency, force reload) in.
type Model struct {
	State PrimaryState
}

// NewModel constructs a Model from an initial PrimaryState.
func NewModel(s PrimaryState) Model {
	return Model{State: s}
}

// Init implements tea.Model. The primary screen has no startup command of
// its own yet — a later task's wiring supplies one.
func (m Model) Init() tea.Cmd {
	return nil
}

// View implements tea.Model by rendering the current state.
func (m Model) View() string {
	return RenderPrimary(m.State)
}

// Update implements tea.Model, routing the primary screen's own key events.
// Some routes are real mutations already expressible in PrimaryState
// (pause/resume, selection toggle, quit); others are stubs a later task
// gives meaning to (expand/collapse, force reload, popup switching, the
// concurrency adjuster) — the point of this task is that every key routes
// without erroring, not that each already does something.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "q":
		return m, tea.Quit
	case "p":
		m.State.Paused = !m.State.Paused
		return m, nil
	case " ":
		if len(m.State.Projects) > 0 {
			m.State.Projects[0].Included = !m.State.Projects[0].Included
		}
		return m, nil
	case "tab", "R", "Q", "H", "B", "c":
		// Routed but intentionally inert here: expand/collapse (tab) and
		// force reload (R) need a live queue/tree the Model doesn't hold
		// yet; Q/H/B popup switching is Task 15's Screen field; c's
		// concurrency adjustment needs Task 16's live worker.Pool.
		return m, nil
	default:
		return m, nil
	}
}
