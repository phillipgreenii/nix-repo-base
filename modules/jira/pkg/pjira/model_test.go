package pjira

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIssue_JSONRoundTrip_OmitsEmptyOptionals(t *testing.T) {
	in := Issue{Key: "ENG-1", Summary: "s", Status: "Open", IssueType: "Bug", Labels: []string{}, URL: "u"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Optional fields must be omitted when empty.
	for _, k := range []string{"priority", "project", "created", "updated", "reporter", "assignee", "changelog", "comments", "status_category", "parent"} {
		if got := string(b); strings.Contains(got, `"`+k+`"`) {
			t.Errorf("expected %q omitted, got %s", k, got)
		}
	}
	var out Issue
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Key != "ENG-1" || out.IssueType != "Bug" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

// TestIssue_JSONIncludesStatusCategoryAndParent pins the wire names and that
// both fields appear when set.
func TestIssue_JSONIncludesStatusCategoryAndParent(t *testing.T) {
	b, err := json.Marshal(Issue{Key: "ENG-1", StatusCategory: "done", Parent: "ENG-100", Labels: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"status_category":"done"`, `"parent":"ENG-100"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("expected %s in %s", want, b)
		}
	}
}
