//go:build darwin

package osx

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// serviceLabels maps TCC service identifiers to human-readable display labels.
// Unknown services use the raw service string.
var serviceLabels = map[string]string{
	"kTCCServiceListenEvent":          "kTCCServiceListenEvent (Input Monitoring)",
	"kTCCServiceCamera":               "kTCCServiceCamera (Camera)",
	"kTCCServiceMicrophone":           "kTCCServiceMicrophone (Microphone)",
	"kTCCServiceAccessibility":        "kTCCServiceAccessibility (Accessibility)",
	"kTCCServiceSystemPolicyAllFiles": "kTCCServiceSystemPolicyAllFiles (Full Disk Access)",
}

// serviceDisplayName returns the human-readable label for a TCC service, or the
// raw service name if unknown.
func serviceDisplayName(service string) string {
	if label, ok := serviceLabels[service]; ok {
		return label
	}
	return service
}

// tccEntry is one row from the sqlite3 duplicates query.
type tccEntry struct {
	service      string
	client       string
	lastModified int64
}

// tccGroup is a set of entries sharing the same (service, binName) key.
type tccGroup struct {
	service      string
	binName      string
	entries      []tccEntry // insertion order (preserves ORDER BY service, client from query)
	newestClient string
	newestMod    int64
}

// parseTCCRows parses pipe-delimited sqlite3 output into tccEntry values.
// Malformed lines (fewer than 3 pipe-separated fields) are silently skipped.
// last_modified is parsed as int64; non-numeric values become 0.
func parseTCCRows(stdout []byte) []tccEntry {
	var out []tccEntry
	for line := range strings.SplitSeq(string(stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		out = append(out, tccEntry{
			service:      parts[0],
			client:       parts[1],
			lastModified: ts,
		})
	}
	return out
}

// groupTCCEntries groups entries by (service, basename(client)), retains only
// groups with more than one entry (i.e. duplicates), and sorts the result by
// (service, binName) ascending — matching the bash insertion-sort on key_list.
// The entry with the highest last_modified is the current one; the rest are stale.
func groupTCCEntries(entries []tccEntry) []tccGroup {
	type key struct{ service, binName string }
	order := []key{} // insertion order for stable iteration
	byKey := map[key]*tccGroup{}

	for _, e := range entries {
		binName := filepath.Base(e.client)
		k := key{e.service, binName}
		g, ok := byKey[k]
		if !ok {
			g = &tccGroup{service: e.service, binName: binName}
			byKey[k] = g
			order = append(order, k)
		}
		g.entries = append(g.entries, e)
		if e.lastModified > g.newestMod {
			g.newestMod = e.lastModified
			g.newestClient = e.client
		}
	}

	var groups []tccGroup
	for _, k := range order {
		g := byKey[k]
		if len(g.entries) > 1 {
			groups = append(groups, *g)
		}
	}

	// Sort by service ascending, then binName ascending (bash: services[k1] > services[k2]).
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].service != groups[j].service {
			return groups[i].service < groups[j].service
		}
		return groups[i].binName < groups[j].binName
	})

	return groups
}

// formatTCCReport formats the full duplicate report for len(groups) > 0.
// The returned string ends with a newline and is byte-for-byte identical to the
// original pn-osx-tcc-check.sh awk output.
func formatTCCReport(groups []tccGroup) string {
	var sb strings.Builder

	sb.WriteString("⚠️  TCC Duplicate Report\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("\n")

	for _, g := range groups {
		stale := len(g.entries) - 1
		display := serviceDisplayName(g.service)
		fmt.Fprintf(&sb, "%s:\n", display)
		fmt.Fprintf(&sb, "  %s — %d entries (%d stale)\n", g.binName, len(g.entries), stale)

		// Current first, then stales — in original insertion order within each pass.
		for _, e := range g.entries {
			if e.client == g.newestClient {
				fmt.Fprintf(&sb, "    ✓ %s (current)\n", e.client)
			}
		}
		for _, e := range g.entries {
			if e.client != g.newestClient {
				fmt.Fprintf(&sb, "    ✗ %s (stale)\n", e.client)
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("To clean up: System Preferences > Privacy & Security > [service] > remove stale entries manually.\n")
	return sb.String()
}
