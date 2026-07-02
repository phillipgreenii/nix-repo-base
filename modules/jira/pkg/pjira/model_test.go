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
	for _, k := range []string{"priority", "project", "created", "updated", "reporter", "assignee", "changelog", "comments"} {
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
