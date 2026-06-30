// internal/workspace/doctor_render.go
package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type jsonFinding struct {
	Check    string `json:"check"`
	Repo     string `json:"repo,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Fixable  bool   `json:"fixable"`
	Manual   string `json:"manual,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
}

// RenderDoctor writes the report to w as JSON (opts.JSON) or a human report.
func RenderDoctor(w io.Writer, report *DoctorReport, opts DoctorOptions) error {
	if opts.JSON {
		out := struct {
			Mode     string        `json:"mode"`
			Findings []jsonFinding `json:"findings"`
			Skipped  []string      `json:"skipped"`
			Plan     []string      `json:"plan,omitempty"`
		}{Mode: report.Mode, Skipped: report.Skipped, Plan: report.Plan}
		for _, f := range report.Findings {
			out.Findings = append(out.Findings, jsonFinding{
				Check: f.CheckID, Repo: f.Repo, Severity: f.Severity.String(),
				Message: f.Message, Fixable: f.Fixable, Manual: f.Manual, Skipped: f.Skipped,
			})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return renderHuman(w, report, opts)
}

func renderHuman(w io.Writer, report *DoctorReport, opts DoctorOptions) error {
	// Mode banner.
	if report.Mode == "worktree" {
		fmt.Fprintln(w, "workspace doctor — worktree set (relaxed: dirty=warning, remote-sync not checked)")
	} else {
		fmt.Fprintln(w, "workspace doctor — primary checkouts (origin/<branch> is the baseline)")
	}

	// Dry-run plan.
	if opts.DryRun && len(report.Plan) > 0 {
		fmt.Fprintln(w, "\nfix plan (--dry-run, nothing applied):")
		for _, p := range report.Plan {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}

	// Group findings by repo ("" -> workspace).
	groups := map[string][]Finding{}
	var order []string
	for _, f := range report.Findings {
		key := f.Repo
		if key == "" {
			key = "workspace"
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], f)
	}
	sort.Strings(order)

	var nErr, nWarn int
	for _, key := range order {
		fmt.Fprintf(w, "\n=== %s ===\n", key)
		for _, f := range groups[key] {
			tag := "[manual]"
			switch {
			case f.Skipped:
				tag = "[—]"
			case f.Applied:
				tag = "[fixed]"
			case opts.DryRun && f.Fixable:
				tag = "[would fix]"
			case f.Fixable:
				tag = "[fixable]"
			}
			sev := f.Severity.String()
			if f.Skipped {
				sev = "SKIP"
			} else if f.Severity == SevError {
				nErr++
			} else {
				nWarn++
			}
			fmt.Fprintf(w, "  %-5s %-20s %s %s\n", sev, f.CheckID, f.Message, tag)
			if tag == "[manual]" && f.Manual != "" {
				fmt.Fprintf(w, "          ↳ %s\n", f.Manual)
			}
		}
	}

	// Summary.
	fmt.Fprintln(w)
	switch {
	case nErr == 0 && len(report.Skipped) > 0:
		fmt.Fprintf(w, "workspace doctor: no errors (%d warnings), %d checks SKIPPED. remote equivalence NOT verified.\n", nWarn, len(report.Skipped))
	case nErr == 0:
		fmt.Fprintf(w, "workspace doctor: no errors (%d warnings). local and remote builds will match.\n", nWarn)
	default:
		fmt.Fprintf(w, "workspace doctor: %d errors, %d warnings.\n", nErr, nWarn)
	}
	return nil
}
