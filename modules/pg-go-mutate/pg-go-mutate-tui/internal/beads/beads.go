// Package beads files the single per-package triage bead pg-go-mutate-tui owes
// once every file in a package has a recorded status. It never files an epic
// (an open epic never leaves `bd ready`): this package only ever calls
// BdClient.Create, with a plain task bead.
//
// The bead is the hand-off to an agent (operator ruling, Phillip, 2026-10-26,
// recorded verbatim on bead pg2-tkm9g0): the process "would hum along in the
// background doing the tests and when it completes a package, it would signal
// for an agent to do a deep dive on the results to generate the appropriate
// fixes". So the body carries the run's results (the worklist rows, or the
// per-file report paths when the rows would not fit a bead body) plus the
// instructions for working the bead, and NEVER a survivor/kill count, a
// percentage or a score.
package beads

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/report"
)

// BdClient is the seam over the real `bd` binary. Production code uses
// CLIClient; the package's pure-logic tests may inject a fake, while the test
// that proves the filing contract runs CLIClient against a real bd.
type BdClient interface {
	Create(title, body string, labels []string, priority int) (string, error)
}

// HowToWorkThisBead is the consumer procedure every triage bead carries. It is
// copied VERBATIM from the "Working a triage bead" section of the go-test-gaps
// skill (nix-agent-support, claude-marketplace/pg-go-mutate/skills/go-test-gaps/
// SKILL.md): the agent who works the bead has these instructions without
// loading the skill, so the two MUST stay identical.
const HowToWorkThisBead = `## How to work this bead

- Analyze the results recorded in this bead. Do NOT re-run ` + "`pg-go-mutate`" + ` or
  ` + "`pg-go-mutate-tui`" + `.
- Decide which tests would close the gaps. A row no assertion can kill (a no-op
  or equivalent mutant) needs no test; say so.
- File one new bead per fix with ` + "`--deps \"discovered-from:<this bead's id>\"`" + `
  (never a blocking edge). Its acceptance criteria name each missed path
  (` + "`file:line`" + ` and the behavior) and the test(s) that must exist.
- Close this bead with a reason that lists the fix beads filed, or that says
  why no fix is needed.
`

// bodyCap is the largest body FileTriageBead returns. bd caps a description at
// 65535 bytes; the margin keeps this package clear of that limit.
const bodyCap = 60000

// FileResult is one file's recorded outcome inside a completed package.
type FileResult struct {
	// File is the source file's path relative to the module root.
	File string
	// Status is the ledger status (done, no-tests, unhealthy, ...).
	Status string
	// ReportPath is the per-file report the sweep wrote ("" when none).
	ReportPath string
	// Worklist is the parsed report; meaningful only for a done file.
	Worklist report.Worklist
	// ReportUnreadable is true when a done file's report could not be parsed,
	// so its rows are missing from the bead.
	ReportUnreadable bool
}

// Triage is everything the bead body needs about one completed package.
type Triage struct {
	// Module is the Go module root relative to the repository root.
	Module string
	// PkgPath is the package directory relative to the module root, with no
	// leading "./" (the form the go-test-gaps skill and the sweep share).
	PkgPath string
	Files   []FileResult
}

// Title is the bead title for pkgPath. The go-test-gaps skill uses the same
// form for an operator-requested analysis.
func Title(pkgPath string) string {
	return "go-test-gaps triage: " + pkgPath
}

// FileTriageBead files a P3 triage bead for t, labeled go-test-gaps plus
// repoLabel. repoLabel is supplied by the caller and is NOT derived from any
// path, so the bead always carries the repo's canonical label rather than a raw
// path component.
func FileTriageBead(bd BdClient, t Triage, repoLabel string) (string, error) {
	if strings.TrimSpace(repoLabel) == "" {
		return "", fmt.Errorf("beads: no repo label for package %q; refusing to file an unlabeled bead", t.PkgPath)
	}
	body, err := Body(t)
	if err != nil {
		return "", err
	}
	return bd.Create(Title(t.PkgPath), body, []string{"go-test-gaps", repoLabel}, 3)
}

// Body renders the bead body: the package, the command, the worklist rows (or
// only the report paths when the rows would exceed bd's body cap), the
// per-file report paths, and the instructions for working the bead. It never
// carries a count, a percentage or a score.
func Body(t Triage) (string, error) {
	full := render(t, true)
	if len(full) <= bodyCap {
		return full, nil
	}
	short := render(t, false)
	if len(short) <= bodyCap {
		return short, nil
	}
	return "", fmt.Errorf("beads: triage body for package %q exceeds bd's body cap even with report paths only", t.PkgPath)
}

var statusMeaning = map[string]string{
	"no-tests":       "the package has no test files, so the finding is that this file has no tests",
	"not-enumerable": "the package's mutants could not be enumerated",
	"unhealthy":      "the package does not vet, or its tests already fail on unmutated source",
	"vanished":       "the file no longer exists",
	"failed":         "the run failed before producing a result",
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func render(t Triage, withRows bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pg-go-mutate-tui has analysed every file in package `%s`.\n\n", t.PkgPath)
	fmt.Fprintf(&b, "- Module (relative to the repository root): `%s`\n", t.Module)
	fmt.Fprintf(&b, "- Package (relative to the module root): `%s`\n", t.PkgPath)
	b.WriteString("- Command: `pg-go-mutate --json <file>`, run once per file of the package; the report of each file is listed under \"Reports\".\n\n")

	b.WriteString("## Worklist\n\n")
	b.WriteString("Each row is a mutation the tests did not catch, that is, an assertion the tests do not make. It is a lead to judge, not a verdict.\n\n")
	rows := 0
	var partial, unreadable, tags []string
	for _, f := range t.Files {
		if f.Status != "done" {
			continue
		}
		if f.ReportUnreadable {
			unreadable = append(unreadable, f.File)
			continue
		}
		if f.Worklist.Partial {
			partial = append(partial, f.File)
		}
		if f.Worklist.BuildTagsNotRun != "" {
			tags = append(tags, fmt.Sprintf("`%s` (tags not run: %s)", f.File, f.Worklist.BuildTagsNotRun))
		}
		if !withRows {
			continue
		}
		for _, s := range f.Worklist.Survivors {
			fmt.Fprintf(&b, "- `%s:%d` | %s | %s\n", f.File, s.Line, oneLine(s.Description), oneLine(s.Type))
			rows++
		}
	}
	switch {
	case !withRows:
		b.WriteString("The rows are too large for one bead body. Read them from the `survivors` array of each report listed under \"Reports\".\n")
	case rows == 0:
		b.WriteString("No file of this package reported a surviving mutation.\n")
	}
	b.WriteString("\n")

	var other []FileResult
	for _, f := range t.Files {
		if f.Status != "done" {
			other = append(other, f)
		}
	}
	if len(other) > 0 {
		b.WriteString("## Files without a worklist\n\n")
		for _, f := range other {
			if m, ok := statusMeaning[f.Status]; ok {
				fmt.Fprintf(&b, "- `%s`: %s (%s)\n", f.File, f.Status, m)
			} else {
				fmt.Fprintf(&b, "- `%s`: %s\n", f.File, f.Status)
			}
		}
		b.WriteString("\n")
	}

	if len(partial) > 0 || len(unreadable) > 0 || len(tags) > 0 {
		b.WriteString("## Caveats\n\n")
		for _, f := range partial {
			fmt.Fprintf(&b, "- `%s`: part of the run was not decisively analysed (errored, timed-out or not-viable mutants); its rows may be incomplete.\n", f)
		}
		for _, f := range unreadable {
			fmt.Fprintf(&b, "- `%s`: its report could not be read, so it has no rows here.\n", f)
		}
		for _, f := range tags {
			fmt.Fprintf(&b, "- %s: tests behind those build tags were not run, so some rows may be false gaps.\n", f)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Reports\n\n")
	wrote := false
	for _, f := range t.Files {
		if f.ReportPath == "" {
			continue
		}
		wrote = true
		fmt.Fprintf(&b, "- `%s`: `%s`\n", f.File, f.ReportPath)
	}
	if !wrote {
		b.WriteString("No report was written for this package.\n")
	}
	b.WriteString("\n")
	b.WriteString(HowToWorkThisBead)
	return b.String()
}
