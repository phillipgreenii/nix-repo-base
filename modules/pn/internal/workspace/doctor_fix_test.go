// internal/workspace/doctor_fix_test.go
package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestFixOrderRanks(t *testing.T) {
	if !(fixOrder("repos-present") < fixOrder("lock-present") &&
		fixOrder("lock-present") < fixOrder("flake-lock-fresh")) {
		t.Fatal("fix order ranks wrong")
	}
}

func TestApplyFixes_DryRunMutatesNothing(t *testing.T) {
	ran := false
	report := &DoctorReport{Findings: []Finding{
		{
			CheckID: "lock-present", Severity: SevWarning, Fixable: true,
			fix: func(context.Context) error { ran = true; return nil },
		},
	}}
	env := &doctorEnv{ws: &Workspace{config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}}}
	applyFixes(context.Background(), env, report, DoctorOptions{Fix: true, DryRun: true})
	if ran {
		t.Fatal("dry-run must not execute fixes")
	}
	if len(report.Plan) == 0 {
		t.Fatal("dry-run must record a plan")
	}
}

func TestApplyFixes_RunsInOrderAndReRuns(t *testing.T) {
	var order []string
	mk := func(id string) Finding {
		return Finding{
			CheckID: id, Severity: SevError, Fixable: true,
			fix: func(context.Context) error { order = append(order, id); return nil },
		}
	}
	report := &DoctorReport{Findings: []Finding{mk("flake-lock-fresh"), mk("repos-present")}}
	// stub registry so the re-run returns no findings
	ws := &Workspace{
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, root: t.TempDir(),
		registerChecksFn: func() []check { return nil },
	}
	env := &doctorEnv{ws: ws}
	applyFixes(context.Background(), env, report, DoctorOptions{Fix: true})
	if len(order) != 2 || order[0] != "repos-present" || order[1] != "flake-lock-fresh" {
		t.Fatalf("fixes ran out of order: %v", order)
	}
}

func TestApplyFixes_FixErrorIsReported(t *testing.T) {
	report := &DoctorReport{Findings: []Finding{
		{
			CheckID: "lock-present", Severity: SevWarning, Fixable: true,
			fix: func(context.Context) error { return errors.New("boom") },
		},
	}}
	ws := &Workspace{
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, root: t.TempDir(),
		registerChecksFn: func() []check { return nil },
	}
	applyFixes(context.Background(), &doctorEnv{ws: ws}, report, DoctorOptions{Fix: true})
	if !hasFinding(report.Findings, "fix-failed", SevError) {
		t.Fatalf("fix error should surface as fix-failed: %+v", report.Findings)
	}
}
