package report

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSurvivorCountReadsKilledAndSurvived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"statistics":{"killed":9,"survived":2}}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	killed, survived, err := ParseSurvivorCount(path)
	if err != nil {
		t.Fatal(err)
	}
	if killed != 9 || survived != 2 {
		t.Fatalf("expected killed=9 survived=2, got killed=%d survived=%d", killed, survived)
	}
}

func TestParseSurvivorCountErrorsOnMissingFile(t *testing.T) {
	_, _, err := ParseSurvivorCount(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for a missing report file")
	}
}

func TestParseWorklistReadsRowsAndRunHealthFlagButNoCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	body := `{"survivors":[{"file":"","line":12,"type":"conditional","description":"\"!=\" -> \"==\""}],` +
		`"statistics":{"killed":9,"survived":1,"survivedActionable":1,"notViable":0,"timedOut":2,"errors":0},` +
		`"buildTagsNotRun":"contract"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := ParseWorklist(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Survivors) != 1 || w.Survivors[0].Line != 12 || w.Survivors[0].Type != "conditional" || w.Survivors[0].Description != `"!=" -> "=="` {
		t.Fatalf("survivor row not read faithfully: %+v", w.Survivors)
	}
	if !w.Partial {
		t.Fatal("a nonzero timedOut count must set Partial")
	}
	if w.BuildTagsNotRun != "contract" {
		t.Fatalf("BuildTagsNotRun = %q", w.BuildTagsNotRun)
	}
}

func TestParseWorklistCleanRunIsNotPartialAndNullTagsAreEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	body := `{"survivors":[],"statistics":{"killed":3,"survived":0,"notViable":0,"timedOut":0,"errors":0},"buildTagsNotRun":null}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := ParseWorklist(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Survivors) != 0 || w.Partial || w.BuildTagsNotRun != "" {
		t.Fatalf("expected an empty, non-partial worklist, got %+v", w)
	}
}

func TestParseWorklistErrorsOnMissingAndMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ParseWorklist(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("expected an error for a missing report")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseWorklist(bad); err == nil {
		t.Fatal("expected an error for a malformed report")
	}
}
