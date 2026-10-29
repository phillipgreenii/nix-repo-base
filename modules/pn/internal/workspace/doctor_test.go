// internal/workspace/doctor_test.go
package workspace

import (
	"context"
	"testing"
)

func TestSeverityString(t *testing.T) {
	if SevError.String() != "ERROR" || SevWarning.String() != "WARN" {
		t.Fatalf("severity strings wrong: %s %s", SevError, SevWarning)
	}
}

func TestReportExitCode(t *testing.T) {
	clean := &DoctorReport{}
	warn := &DoctorReport{Findings: []Finding{{Severity: SevWarning}}}
	err := &DoctorReport{Findings: []Finding{{Severity: SevError}}}
	if clean.ExitCode(false) != 0 {
		t.Fatal("clean -> 0")
	}
	if warn.ExitCode(false) != 0 {
		t.Fatal("warn (non-strict) -> 0")
	}
	if warn.ExitCode(true) != CheckFailedExitCode {
		t.Fatal("warn (strict) -> 10")
	}
	if err.ExitCode(false) != CheckFailedExitCode {
		t.Fatal("error -> 10")
	}
}

func TestReportExitCode_SkippedErrorNotCounted(t *testing.T) {
	r := &DoctorReport{Findings: []Finding{{Severity: SevError, Skipped: true}}}
	if r.HasErrors() {
		t.Fatal("a skipped SevError must not count as an error")
	}
	if r.ExitCode(false) != 0 {
		t.Fatalf("skipped error -> exit 0, got %d", r.ExitCode(false))
	}
	if r.ExitCode(true) != 0 {
		t.Fatalf("only-skipped findings -> exit 0 even under strict, got %d", r.ExitCode(true))
	}
}

func TestDoctorOrchestratorRunsChecks(t *testing.T) {
	env := &doctorEnv{}
	c := check{id: "stub", run: func(_ context.Context, _ *doctorEnv) []Finding {
		return []Finding{{CheckID: "stub", Severity: SevWarning, Message: "hi"}}
	}}
	got := runChecks(context.Background(), env, []check{c})
	if len(got) != 1 || got[0].CheckID != "stub" {
		t.Fatalf("runChecks: %+v", got)
	}
}
