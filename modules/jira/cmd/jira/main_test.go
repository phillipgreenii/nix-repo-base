package main

import (
	"bytes"
	"testing"
)

func TestRootCmd_Help(t *testing.T) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("root --help: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("jira")) {
		t.Errorf("help output missing tool name; got:\n%s", out.String())
	}
}
