//go:build darwin

package osx

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const testDB = "/tmp/test-TCC.db"

func TestNew_ExposesRunner(t *testing.T) {
	f := exec.NewFakeRunner()
	if New(f) == nil {
		t.Fatal("New returned nil")
	}
}

func TestCheck_ProbesThenQueries(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
	f.AddResponse(
		"sqlite3",
		[]string{testDB, "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"},
		exec.Result{},
		nil,
	)
	if err := New(f).Check(context.Background(), CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 sqlite3 calls (probe + query), got %d", len(calls))
	}
}

func TestCheck_NoFDAExitsCleanly(t *testing.T) {
	// Probe fails — bash exits 0 with a warning. We mirror that: no error.
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{ExitCode: 1}, errors.New("FDA not granted"))

	if err := New(f).Check(context.Background(), CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check should swallow FDA-not-granted; got %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected only the probe call, got %d", len(calls))
	}
}

func TestCheck_PropagatesQueryError(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, "SELECT 1 FROM access LIMIT 1"}, exec.Result{}, nil)
	f.AddResponse(
		"sqlite3",
		[]string{testDB, "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"},
		exec.Result{ExitCode: 1},
		errors.New("query failed"),
	)
	if err := New(f).Check(context.Background(), CheckOptions{DBPath: testDB}); err == nil {
		t.Fatal("expected query failure to propagate; got nil")
	}
}
