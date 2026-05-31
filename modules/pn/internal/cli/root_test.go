package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestExecute_RejectsDevVersion(t *testing.T) {
	var stderr bytes.Buffer
	err := executeWithVersion("dev", []string{"--version"}, &stderr, &stderr)
	if err == nil {
		t.Fatal("expected error when version is 'dev'; got nil")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("expected error mentioning 'version'; got %q", err.Error())
	}
}

func TestExecute_AcceptsRealVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := executeWithVersion("20260531-abc1234", []string{"--version"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected nil error; got %v", err)
	}
}
