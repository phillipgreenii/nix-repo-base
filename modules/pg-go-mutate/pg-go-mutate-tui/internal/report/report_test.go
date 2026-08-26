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
