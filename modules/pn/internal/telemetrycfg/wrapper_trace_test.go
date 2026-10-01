package telemetrycfg

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSudoWrapper(t *testing.T) {
	fakeStore := func(real string) func(string) (string, error) {
		return func(string) (string, error) { return real, nil }
	}
	if got, err := ValidateSudoWrapper("/Users/u/.nix-profile/bin/w", fakeStore("/nix/store/abc-w/bin/w")); err != nil || got != "/nix/store/abc-w/bin/w" {
		t.Fatalf("store path should pass: %q, %v", got, err)
	}
	for name, tc := range map[string]struct {
		path string
		eval func(string) (string, error)
	}{
		"empty":              {"", nil},
		"relative":           {"bin/w", fakeStore("/nix/store/x/w")},
		"resolves elsewhere": {"/nix/store/abc-w/bin/w", fakeStore("/Users/u/evil")},
		"prefix trick":       {"/x/w", fakeStore("/nix/storeevil/w")},
		"dotdot escape":      {"/x/w", fakeStore("/nix/store/../tmp/evil")},
	} {
		if _, err := ValidateSudoWrapper(tc.path, tc.eval); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
}

// TestValidateSudoWrapper_RealSymlink uses the real EvalSymlinks: a symlink in
// a user-writable dir that points outside /nix/store MUST be refused, even if
// the link itself looks harmless.
func TestValidateSudoWrapper_RealSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "evil")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "pg-nix-log-wrapped")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSudoWrapper(link, nil); err == nil {
		t.Fatal("symlink to a user-writable binary must be refused under sudo")
	}
}

func TestRunState_NilSafeAndIgnoresZeroID(t *testing.T) {
	var s *RunState
	s.SetTraceID("abc")
	if s.TraceID() != "" {
		t.Fatal("nil RunState must be inert")
	}
	s = NewRunState(Resolution{})
	s.SetTraceID("00000000000000000000000000000000")
	if s.TraceID() != "" {
		t.Fatal("all-zero trace id is invalid and must be ignored")
	}
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	s.SetTraceID(id)
	if s.TraceID() != id {
		t.Fatalf("TraceID = %q", s.TraceID())
	}
	ctx := WithRunState(context.Background(), s)
	if RunStateFrom(ctx) != s {
		t.Fatal("RunStateFrom must return the stored state")
	}
	if RunStateFrom(context.Background()) != nil {
		t.Fatal("RunStateFrom without state must be nil")
	}
}

func TestWriteHint(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	withID := NewRunState(Resolution{})
	withID.SetTraceID(id)
	noID := NewRunState(Resolution{})

	tests := []struct {
		name    string
		s       *RunState
		verbose bool
		env     map[string]string
		want    string
	}{
		{"off by default", withID, false, nil, ""},
		{"verbose", withID, true, nil, "trace: " + id + "\n"},
		{"PN_TRACE_HINT=1", withID, false, map[string]string{EnvTraceHint: "1"}, "trace: " + id + "\n"},
		{"PN_TRACE_HINT=0 is off", withID, false, map[string]string{EnvTraceHint: "0"}, ""},
		{"no trace id, nothing printed", noID, true, nil, ""},
		{"nil state", nil, true, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			wrote := WriteHint(&buf, tc.s, tc.verbose, envMap(tc.env))
			if buf.String() != tc.want || wrote != (tc.want != "") {
				t.Fatalf("got %q (wrote=%v); want %q", buf.String(), wrote, tc.want)
			}
			if strings.Contains(buf.String(), "\n\n") {
				t.Fatal("hint must be exactly one line")
			}
		})
	}
}
