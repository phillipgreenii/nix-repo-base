// internal/workspace/doctor_render_test.go
package workspace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderDoctor_JSONOnlyFindings(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Findings: []Finding{
		{CheckID: "tree-clean", Repo: "dep", Severity: SevError, Message: "dirty"},
	}}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if out["mode"] != "primary" {
		t.Fatalf("mode missing: %v", out)
	}
	if strings.Contains(buf.String(), "===") {
		t.Fatal("JSON output must not contain human chrome")
	}
}

func TestRenderDoctor_HumanCleanRun(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, &DoctorReport{Mode: "primary"}, DoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no errors") {
		t.Fatalf("clean run should reassure: %q", buf.String())
	}
}

func TestRenderDoctor_SkippedNotCountedAsError(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Findings: []Finding{
		{CheckID: "branch-synced", Repo: "dep", Severity: SevError, Skipped: true, Message: "remote comparison skipped"},
	}}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "SKIP") {
		t.Fatalf("skipped finding should render SKIP: %q", s)
	}
	if !strings.Contains(s, "no errors") {
		t.Fatalf("a report whose only finding is skipped should summarize as no errors: %q", s)
	}
}

func TestRenderDoctor_HumanGroupsAndMarks(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Findings: []Finding{
		{CheckID: "branch-synced", Repo: "dep", Severity: SevError, Message: "ahead", Manual: "git ..."},
		{CheckID: "repos-extra", Repo: "stray", Severity: SevWarning, Message: "extra", Fixable: true},
	}}
	var buf bytes.Buffer
	_ = RenderDoctor(&buf, r, DoctorOptions{})
	s := buf.String()
	for _, want := range []string{"=== dep ===", "=== stray ===", "ERROR", "WARN", "[manual]", "[fixable]"} {
		if !strings.Contains(s, want) {
			t.Fatalf("human output missing %q in:\n%s", want, s)
		}
	}
}

// Item 1: --fix surfaces a "fixed N items" summary line. report.Fixed carries
// the number of findings resolved by the fix pass (fixable-before minus residual
// after); renderHuman must report it so the user gets real feedback.
func TestRenderDoctor_FixSummaryReportsFixedCount(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Fixed: 2}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{Fix: true}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "fixed 2 items") {
		t.Fatalf("--fix summary should report fixed count: %q", s)
	}
}

// A clean --fix run (nothing to fix) must NOT print a spurious "fixed 0 items".
func TestRenderDoctor_FixSummaryOmittedWhenNothingFixed(t *testing.T) {
	r := &DoctorReport{Mode: "primary", Fixed: 0}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{Fix: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "fixed 0 items") {
		t.Fatalf("must not print 'fixed 0 items': %q", buf.String())
	}
}

// Offline nit: when checks were SKIPPED (--offline) AND errors remain, the
// summary must STILL carry the "K checks SKIPPED — remote equivalence NOT
// verified" caveat. Previously that caveat only fired in the nErr==0 branch, so
// with errors present it was hidden from the rollup.
func TestRenderDoctor_SkippedCaveatShownWithErrors(t *testing.T) {
	r := &DoctorReport{
		Mode:    "primary",
		Skipped: []string{"branch-synced"},
		Findings: []Finding{
			{CheckID: "tree-clean", Repo: "dep", Severity: SevError, Message: "dirty"},
			{CheckID: "branch-synced", Repo: "dep", Severity: SevError, Skipped: true, Message: "remote comparison skipped"},
		},
	}
	var buf bytes.Buffer
	if err := RenderDoctor(&buf, r, DoctorOptions{Offline: true}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "1 errors") {
		t.Fatalf("summary should report the error count: %q", s)
	}
	if !strings.Contains(s, "SKIPPED") || !strings.Contains(s, "remote equivalence NOT verified") {
		t.Fatalf("summary must retain the SKIPPED caveat even with errors present: %q", s)
	}
}
