//go:build darwin

package osx

import (
	"strings"
	"testing"
)

func TestParseTCCRows_Basic(t *testing.T) {
	input := []byte(
		"kTCCServiceListenEvent|/nix/store/old111-sleepwatcher/bin/sleepwatcher|1000\n" +
			"kTCCServiceListenEvent|/nix/store/new222-sleepwatcher/bin/sleepwatcher|2000\n",
	)
	rows := parseTCCRows(input)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].service != "kTCCServiceListenEvent" || rows[0].lastModified != 1000 {
		t.Fatalf("row[0] wrong: %+v", rows[0])
	}
	if rows[1].lastModified != 2000 {
		t.Fatalf("row[1] lastModified wrong: %+v", rows[1])
	}
}

func TestParseTCCRows_SkipsMalformed(t *testing.T) {
	input := []byte("only_one_field\nok|/nix/store/x/bin/x|999\n")
	rows := parseTCCRows(input)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
}

func TestParseTCCRows_Empty(t *testing.T) {
	if got := parseTCCRows([]byte{}); len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func TestParseTCCRows_NonNumericLastModified(t *testing.T) {
	// Non-numeric last_modified parses to 0 rather than dropping the row.
	rows := parseTCCRows([]byte("kTCCServiceCamera|/nix/store/a/bin/x|notanumber\n"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].lastModified != 0 {
		t.Fatalf("want lastModified 0 for non-numeric, got %d", rows[0].lastModified)
	}
}

func TestGroupTCCEntries_NoDuplicates(t *testing.T) {
	entries := []tccEntry{
		{service: "kTCCServiceListenEvent", client: "/nix/store/aaa-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
		{service: "kTCCServiceListenEvent", client: "/nix/store/bbb-other/bin/tool", lastModified: 2000},
	}
	if got := groupTCCEntries(entries); len(got) != 0 {
		t.Fatalf("want no groups (unique bins), got %v", got)
	}
}

func TestGroupTCCEntries_DetectsDuplicates(t *testing.T) {
	entries := []tccEntry{
		{service: "kTCCServiceListenEvent", client: "/nix/store/old111-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
		{service: "kTCCServiceListenEvent", client: "/nix/store/old222-sleepwatcher/bin/sleepwatcher", lastModified: 2000},
		{service: "kTCCServiceListenEvent", client: "/nix/store/new333-sleepwatcher/bin/sleepwatcher", lastModified: 3000},
	}
	groups := groupTCCEntries(entries)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	g := groups[0]
	if len(g.entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(g.entries))
	}
	if g.newestClient != "/nix/store/new333-sleepwatcher/bin/sleepwatcher" {
		t.Fatalf("wrong newestClient: %q", g.newestClient)
	}
	if g.binName != "sleepwatcher" {
		t.Fatalf("wrong binName: %q", g.binName)
	}
}

func TestGroupTCCEntries_GroupsByBasenameAcrossVersions(t *testing.T) {
	// bash: "bin_name = path_parts[n]" — last component only.
	// bash-5.2p37 and bash-5.3p3 both have basename "bash" and same service → one group of 4.
	entries := []tccEntry{
		{service: "kTCCServiceMicrophone", client: "/nix/store/aaa-bash-5.2p37/bin/bash", lastModified: 1000},
		{service: "kTCCServiceMicrophone", client: "/nix/store/bbb-bash-5.2p37/bin/bash", lastModified: 2000},
		{service: "kTCCServiceMicrophone", client: "/nix/store/ccc-bash-5.3p3/bin/bash", lastModified: 3000},
		{service: "kTCCServiceMicrophone", client: "/nix/store/ddd-bash-5.3p3/bin/bash", lastModified: 4000},
	}
	groups := groupTCCEntries(entries)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if len(groups[0].entries) != 4 {
		t.Fatalf("want 4 entries, got %d", len(groups[0].entries))
	}
	if groups[0].newestClient != "/nix/store/ddd-bash-5.3p3/bin/bash" {
		t.Fatalf("wrong newestClient: %q", groups[0].newestClient)
	}
}

func TestGroupTCCEntries_SortedByServiceThenBinName(t *testing.T) {
	entries := []tccEntry{
		{service: "kTCCServiceListenEvent", client: "/nix/store/a-z/bin/z", lastModified: 1},
		{service: "kTCCServiceListenEvent", client: "/nix/store/b-z/bin/z", lastModified: 2},
		{service: "kTCCServiceCamera", client: "/nix/store/a-cam/bin/camera", lastModified: 1},
		{service: "kTCCServiceCamera", client: "/nix/store/b-cam/bin/camera", lastModified: 2},
	}
	groups := groupTCCEntries(entries)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	// kTCCServiceCamera < kTCCServiceListenEvent lexicographically
	if groups[0].service != "kTCCServiceCamera" {
		t.Fatalf("first group should be Camera, got %q", groups[0].service)
	}
	if groups[1].service != "kTCCServiceListenEvent" {
		t.Fatalf("second group should be ListenEvent, got %q", groups[1].service)
	}
}

func TestServiceDisplayName_KnownAndUnknown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"kTCCServiceListenEvent", "kTCCServiceListenEvent (Input Monitoring)"},
		{"kTCCServiceCamera", "kTCCServiceCamera (Camera)"},
		{"kTCCServiceMicrophone", "kTCCServiceMicrophone (Microphone)"},
		{"kTCCServiceAccessibility", "kTCCServiceAccessibility (Accessibility)"},
		{"kTCCServiceSystemPolicyAllFiles", "kTCCServiceSystemPolicyAllFiles (Full Disk Access)"},
		{"kTCCServiceUnknownService", "kTCCServiceUnknownService"},
	}
	for _, c := range cases {
		if got := serviceDisplayName(c.in); got != c.want {
			t.Errorf("serviceDisplayName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTCCReport_GoldenSingleGroup(t *testing.T) {
	groups := []tccGroup{
		{
			service: "kTCCServiceListenEvent",
			binName: "sleepwatcher",
			entries: []tccEntry{
				{service: "kTCCServiceListenEvent", client: "/nix/store/old-sleepwatcher/bin/sleepwatcher", lastModified: 1000},
				{service: "kTCCServiceListenEvent", client: "/nix/store/new-sleepwatcher/bin/sleepwatcher", lastModified: 2000},
			},
			newestClient: "/nix/store/new-sleepwatcher/bin/sleepwatcher",
			newestMod:    2000,
		},
	}
	got := formatTCCReport(groups)

	// Golden: validate exact structure
	wantLines := []string{
		"⚠️  TCC Duplicate Report",
		"━━━━━━━━━━━━━━━━━━━━━━━━",
		"",
		"kTCCServiceListenEvent (Input Monitoring):",
		"  sleepwatcher — 2 entries (1 stale)",
		"    ✓ /nix/store/new-sleepwatcher/bin/sleepwatcher (current)",
		"    ✗ /nix/store/old-sleepwatcher/bin/sleepwatcher (stale)",
		"",
		"To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in output:\n%s", line, got)
		}
	}
}

func TestFormatTCCReport_GoldenMultipleGroups(t *testing.T) {
	groups := []tccGroup{
		{
			service: "kTCCServiceCamera", binName: "camera",
			entries: []tccEntry{
				{service: "kTCCServiceCamera", client: "/nix/store/a-cam/bin/camera", lastModified: 1000},
				{service: "kTCCServiceCamera", client: "/nix/store/b-cam/bin/camera", lastModified: 2000},
			},
			newestClient: "/nix/store/b-cam/bin/camera", newestMod: 2000,
		},
		{
			service: "kTCCServiceListenEvent", binName: "sleepwatcher",
			entries: []tccEntry{
				{service: "kTCCServiceListenEvent", client: "/nix/store/x-sw/bin/sleepwatcher", lastModified: 500},
				{service: "kTCCServiceListenEvent", client: "/nix/store/y-sw/bin/sleepwatcher", lastModified: 600},
			},
			newestClient: "/nix/store/y-sw/bin/sleepwatcher", newestMod: 600,
		},
	}
	got := formatTCCReport(groups)
	if !strings.Contains(got, "kTCCServiceCamera (Camera):") {
		t.Errorf("missing Camera group header")
	}
	if !strings.Contains(got, "kTCCServiceListenEvent (Input Monitoring):") {
		t.Errorf("missing ListenEvent group header")
	}
	// Both groups appear, cleanup line appears once at the end.
	if strings.Count(got, "To clean up:") != 1 {
		t.Errorf("cleanup line should appear exactly once")
	}
}

// TestFormatTCCReport_ExactBytes is the byte-exact golden test — it locks the
// full output for the single-group case. If this fails, the output format has
// drifted from the bash (em dash U+2014, check mark U+2713, ballot X U+2717,
// heavy horizontal U+2501, exact spacing).
func TestFormatTCCReport_ExactBytes(t *testing.T) {
	groups := []tccGroup{
		{
			service: "kTCCServiceListenEvent", binName: "sleepwatcher",
			entries: []tccEntry{
				{service: "kTCCServiceListenEvent", client: "/nix/store/old-sw/bin/sleepwatcher", lastModified: 1000},
				{service: "kTCCServiceListenEvent", client: "/nix/store/new-sw/bin/sleepwatcher", lastModified: 2000},
			},
			newestClient: "/nix/store/new-sw/bin/sleepwatcher", newestMod: 2000,
		},
	}
	want := "⚠️  TCC Duplicate Report\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"\n" +
		"kTCCServiceListenEvent (Input Monitoring):\n" +
		"  sleepwatcher — 2 entries (1 stale)\n" +
		"    ✓ /nix/store/new-sw/bin/sleepwatcher (current)\n" +
		"    ✗ /nix/store/old-sw/bin/sleepwatcher (stale)\n" +
		"\n" +
		"To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n"
	if got := formatTCCReport(groups); got != want {
		t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, got)
	}
}
