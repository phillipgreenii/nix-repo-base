//go:build darwin

package osx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
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
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
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
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "sqlite3", Result: exec.Result{ExitCode: 1}})

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check should swallow FDA-not-granted; got %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected only the probe call, got %d", len(calls))
	}
}

// TestCheck_NoFDAWritesWarning verifies the FDA-not-granted warning is written
// to errOut (not stdout) and that stdout stays empty.
func TestCheck_NoFDAWritesWarning(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "sqlite3", Result: exec.Result{ExitCode: 1}})

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check should return nil; got %v", err)
	}
	if !strings.Contains(errOut.String(), "TCC check skipped") {
		t.Errorf("warning not on errOut; got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "Grant FDA: System Preferences") {
		t.Errorf("FDA grant hint missing from errOut; got: %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("out should be empty on FDA skip; got: %q", out.String())
	}
}

// TestCheck_MissingSqlite3ReturnsError proves a probe error that is NOT a
// *exec.CommandError (sqlite3 could not be executed — e.g. missing binary) is
// surfaced as a real error rather than being silently mislabeled as an FDA
// denial (bead pg2-clyzt).
func TestCheck_MissingSqlite3ReturnsError(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{},
		errors.New(`exec: "sqlite3": executable file not found in $PATH`))

	var out, errOut bytes.Buffer
	err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB})
	if err == nil {
		t.Fatal("expected an error when sqlite3 cannot be executed")
	}
	if strings.Contains(errOut.String(), "Full Disk Access") {
		t.Errorf("missing-binary must not emit the FDA-denied warning; got: %q", errOut.String())
	}
}

func TestCheck_PropagatesQueryError(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{ExitCode: 1}, errors.New("query failed"))

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err == nil {
		t.Fatal("expected query failure to propagate; got nil")
	}
}

func TestCheck_NoDuplicatesPrintsClean(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceListenEvent|/nix/store/aaa-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceCamera|/nix/store/bbb-cam/bin/camera|2000\n",
	)}, nil)

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(out.String(), "No TCC duplicates found") {
		t.Errorf("want no-duplicates message, got: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("errOut should be empty, got: %q", errOut.String())
	}
}

func TestCheck_DuplicatesPrintsReport(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
	)}, nil)

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"TCC Duplicate Report",
		"kTCCServiceListenEvent (Input Monitoring):",
		"sleepwatcher",
		"2 entries (1 stale)",
		"(current)",
		"(stale)",
		"remove stale entries manually",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestCheck_GoldenDuplicates is the byte-exact end-to-end golden test — it locks
// the full stdout output for a duplicate group.
func TestCheck_GoldenDuplicates(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
	)}, nil)

	want := "⚠️  TCC Duplicate Report\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"\n" +
		"kTCCServiceListenEvent (Input Monitoring):\n" +
		"  sleepwatcher — 2 entries (1 stale)\n" +
		"    ✓ /nix/store/new-sw/bin/sleepwatcher (current)\n" +
		"    ✗ /nix/store/old-sw/bin/sleepwatcher (stale)\n" +
		"\n" +
		"To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n"

	var out, errOut bytes.Buffer
	if err := New(f).Check(context.Background(), &out, &errOut, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := out.String(); got != want {
		t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestCheck_EmptyQueryOutput(t *testing.T) {
	// Query returns no rows (no nix store entries at all) → no-duplicates.
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte{}}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(out.String(), "No TCC duplicates found") {
		t.Errorf("empty output should produce no-duplicates message; got: %q", out.String())
	}
}

// ---- Ported bats scenarios (test-pn-osx-tcc-check.bats) ----

// Bats: "marks newest as current: higher last_modified gets checkmark"
func TestCheck_NewestMarkedCurrent(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceListenEvent|/nix/store/old-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/new-sw/bin/sleepwatcher|2000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "new-sw") || !strings.Contains(got, "(current)") {
		t.Error("newest path should be marked current")
	}
	// Verify old-sw appears as stale, not current.
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "old-sw") && strings.Contains(line, "(current)") {
			t.Error("old-sw should not be current")
		}
	}
}

// Bats: "multiple services: both service names appear"
func TestCheck_MultipleServices(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceCamera|/nix/store/a-cam/bin/camera|1000\n" +
			"kTCCServiceCamera|/nix/store/b-cam/bin/camera|2000\n" +
			"kTCCServiceListenEvent|/nix/store/a-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/b-sw/bin/sleepwatcher|2000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "kTCCServiceCamera") {
		t.Error("Camera service missing")
	}
	if !strings.Contains(got, "kTCCServiceListenEvent") {
		t.Error("ListenEvent service missing")
	}
}

// Bats: "non-nix entries ignored: paths outside /nix/store produce no output".
// Enforced at the SQL level (WHERE client LIKE '/nix/store/%'); a zero-row
// result yields the no-duplicates message.
func TestCheck_NonNixEntriesIgnoredAtQueryLevel(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte{}}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(out.String(), "No TCC duplicates found") {
		t.Error("empty rows should yield no-duplicates message")
	}
}

// Bats: "groups different versions of same binary together" (4 entries, 2 versions).
func TestCheck_FourEntriesTwoVersionsOneGroup(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceMicrophone|/nix/store/aaa-bash-5.2p37/bin/bash|1000\n" +
			"kTCCServiceMicrophone|/nix/store/bbb-bash-5.2p37/bin/bash|2000\n" +
			"kTCCServiceMicrophone|/nix/store/ccc-bash-5.3p3/bin/bash|3000\n" +
			"kTCCServiceMicrophone|/nix/store/ddd-bash-5.3p3/bin/bash|4000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "4 entries") {
		t.Errorf("want 4 entries in one group; got:\n%s", got)
	}
	if !strings.Contains(got, "3 stale") {
		t.Errorf("want 3 stale; got:\n%s", got)
	}
	if !strings.Contains(got, "ddd-bash-5.3p3") || !strings.Contains(got, "(current)") {
		t.Error("ddd-bash-5.3p3 should be current (highest lastModified)")
	}
}

// Bats: "cleanup instructions: duplicates found includes System Preferences and remove stale entries".
func TestCheck_CleanupInstructionsPresent(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceListenEvent|/nix/store/a-sw/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/b-sw/bin/sleepwatcher|2000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "System Preferences") {
		t.Error("missing System Preferences in cleanup line")
	}
	if !strings.Contains(got, "remove stale entries manually") {
		t.Error("missing 'remove stale entries manually' in cleanup line")
	}
}

// Bats: "disabled entries excluded: all-disabled duplicates produce no output".
// auth_value != 2 are excluded by the WHERE clause → empty result → no-duplicates.
func TestCheck_DisabledEntriesExcludedAtQueryLevel(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte{}}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(out.String(), "No TCC duplicates found") {
		t.Error("disabled entries should yield no-duplicates message")
	}
}

// Bats: "mixed enabled/disabled with single enabled produces no output".
// Only 1 enabled entry after the WHERE filter → no duplicate group.
func TestCheck_SingleEnabledEntryNoDuplicate(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceCamera|/nix/store/bbb-bash-5.3p3/bin/bash|2000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(out.String(), "No TCC duplicates found") {
		t.Error("single enabled entry should yield no-duplicates message")
	}
}

// Bats: "only enabled duplicates are reported" (2 enabled rows pass the WHERE filter).
func TestCheck_OnlyEnabledDuplicatesReported(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sqlite3", []string{testDB, tccFDAProbeQuery}, exec.Result{}, nil)
	f.AddResponse("sqlite3", []string{testDB, tccDuplicatesQuery}, exec.Result{Stdout: []byte(
		"kTCCServiceCamera|/nix/store/bbb-bash-5.3p3/bin/bash|2000\n" +
			"kTCCServiceCamera|/nix/store/ccc-bash-5.3p3/bin/bash|3000\n",
	)}, nil)

	var out bytes.Buffer
	if err := New(f).Check(context.Background(), &out, io.Discard, CheckOptions{DBPath: testDB}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "2 entries") {
		t.Errorf("want 2 entries; got:\n%s", got)
	}
	if !strings.Contains(got, "1 stale") {
		t.Errorf("want 1 stale; got:\n%s", got)
	}
	if strings.Contains(got, "aaa-bash") {
		t.Error("disabled aaa-bash entry should not appear (filtered by SQL)")
	}
}
