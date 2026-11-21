package traceexplain

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// fmtDur renders seconds as text: the value is rounded to a whole second,
// then shown as 40s, 6m40s or 1h20m.
func fmtDur(sec float64) string {
	n := int(math.Round(sec))
	switch {
	case n < 60:
		return fmt.Sprintf("%ds", n)
	case n < 3600:
		return fmt.Sprintf("%dm%02ds", n/60, n%60)
	default:
		return fmt.Sprintf("%dh%02dm", n/3600, (n%3600)/60)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// RenderText prints the report: the INCOMPLETE TRACE line (when r.Incomplete)
// first, then header when non-empty, then the report itself.
func RenderText(w io.Writer, r Report, header string) error {
	var b strings.Builder
	if r.Incomplete {
		b.WriteString("INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded\n")
	}
	if header != "" {
		b.WriteString(header + "\n")
	}

	title := "Partial trace"
	if r.Verb != "" {
		title = "pn " + r.Verb
		if r.Scope != "" {
			title += " (scope " + r.Scope + ")"
		}
		title += ", trace"
	}
	fmt.Fprintf(&b, "%s %s, started %s, wall %s\n", title, r.TraceID, r.Start.UTC().Format(time.RFC3339), fmtDur(r.WallSeconds))

	repos := append([]RepoReport(nil), r.Repos...)
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].WallSeconds > repos[j].WallSeconds })

	if len(repos) > 0 {
		b.WriteString(slowestLine(r, repos[0]) + "\n")
	}
	for _, rr := range repos {
		writeRepo(&b, rr)
	}
	if !r.Incomplete {
		out := "Outside repos: " + fmtDur(r.OutsideReposSeconds)
		if len(r.Hooks) > 0 {
			hs := make([]string, 0, len(r.Hooks))
			for _, h := range r.Hooks {
				hs = append(hs, fmt.Sprintf("%s %s %s", h.When, h.Name, fmtDur(h.Seconds)))
			}
			out += " (hooks: " + strings.Join(hs, ", ") + ")"
		}
		b.WriteString(out + "\n")
	}
	fmt.Fprintf(&b, "Nix (summed across parallel work, can exceed wall): substitutes %s, build-lock wait %s\n",
		fmtDur(r.SubstituteSecondsSummed), fmtDur(r.LockWaitSecondsSummed))

	if len(r.TopBuilds) > 0 {
		b.WriteString("\nTop local builds:\n")
		for _, bd := range r.TopBuilds {
			where := bd.Repo
			if bd.Step != "" {
				where += " / " + bd.Step
			}
			fmt.Fprintf(&b, "  %s  %s  %s\n", bd.Drv, fmtDur(bd.Seconds), where)
		}
	}
	if len(r.Anomalies) > 0 {
		b.WriteString("\nAnomalies:\n")
		for _, a := range r.Anomalies {
			b.WriteString("  " + a + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func slowestLine(r Report, rr RepoReport) string {
	line := fmt.Sprintf("Slowest: %s %s", rr.Name, fmtDur(rr.WallSeconds))
	if r.WallSeconds > 0 {
		line += fmt.Sprintf(" (%d%%)", int(math.Round(100*rr.WallSeconds/r.WallSeconds)))
	}
	var parts []string
	if rr.UpdateLocks != nil && len(rr.UpdateLocks.Steps) > 0 {
		s := slowestSteps(rr.UpdateLocks.Steps)[0]
		parts = append(parts, fmt.Sprintf("step %s %s", s.Name, fmtDur(s.Seconds)))
	}
	for _, bd := range r.TopBuilds { // TopBuilds is slowest first
		if bd.Repo == rr.Name {
			parts = append(parts, fmt.Sprintf("top local build %s %s", bd.Drv, fmtDur(bd.Seconds)))
			break
		}
	}
	if len(parts) > 0 {
		line += " — " + strings.Join(parts, "; ")
	}
	return line
}

// slowestSteps orders steps slowest first for the text report. The JSON keeps
// start order; the difference is intended (text answers "what was slow", JSON
// preserves the sequence).
func slowestSteps(steps []Step) []Step {
	out := append([]Step(nil), steps...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seconds > out[j].Seconds })
	return out
}

func writeRepo(b *strings.Builder, rr RepoReport) {
	fmt.Fprintf(b, "  %s  wall %s", rr.Name, fmtDur(rr.WallSeconds))
	fmt.Fprintf(b, "  relock %s  git %s  unattributed %s\n",
		fmtDur(rr.RelockSeconds), fmtDur(rr.GitSeconds), fmtDur(rr.UnattributedSeconds))
	if ul := rr.UpdateLocks; ul != nil {
		fmt.Fprintf(b, "    update-locks %s (setup %s, finalize %s)", fmtDur(ul.Seconds),
			fmtDur(ul.SetupSeconds), fmtDur(ul.FinalizeSeconds))
		if ul.BetweenStepsSeconds > 0 {
			fmt.Fprintf(b, ", between steps %s", fmtDur(ul.BetweenStepsSeconds))
		}
		b.WriteString("\n")
		for _, s := range slowestSteps(ul.Steps) {
			fmt.Fprintf(b, "    step %s %s %s\n", s.Name, fmtDur(s.Seconds), s.Outcome)
		}
		if ul.StepsMissing {
			b.WriteString("    no step data: update-locks.sh printed no UL_STEP lines (it exited before its first step, or its lib predates UL_STEP)\n")
		}
	}
	writeNix(b, rr.Nix)
}

// maxNixLines caps the individual nix invocation lines printed per repo; the
// aggregate per-subcommand lines are always printed and --json keeps the full
// list.
const maxNixLines = 5

func writeNix(b *strings.Builder, calls []NixCall) {
	type agg struct {
		sub   string
		n     int
		total float64
	}
	var aggs []*agg
	idx := map[string]*agg{}
	for _, n := range calls {
		a := idx[n.Subcommand]
		if a == nil {
			a = &agg{sub: n.Subcommand}
			idx[n.Subcommand] = a
			aggs = append(aggs, a)
		}
		a.n++
		a.total += n.Seconds
	}
	sort.SliceStable(aggs, func(i, j int) bool { return aggs[i].total > aggs[j].total })
	for _, a := range aggs {
		fmt.Fprintf(b, "    nix %s x%d %s\n", a.sub, a.n, fmtDur(a.total))
	}
	slow := append([]NixCall(nil), calls...)
	sort.SliceStable(slow, func(i, j int) bool { return slow[i].Seconds > slow[j].Seconds })
	for i, n := range slow {
		if i == maxNixLines {
			fmt.Fprintf(b, "    (+%d more)\n", len(slow)-maxNixLines)
			break
		}
		fmt.Fprintf(b, "    nix %s %s (plan: %s, %s)\n", n.Subcommand, fmtDur(n.Seconds),
			plural(n.PlanBuilds, "build", "builds"), plural(n.PlanFetches, "fetch", "fetches"))
	}
}
