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
