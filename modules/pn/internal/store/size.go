package store

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

var diskutilBytesRE = regexp.MustCompile(`\((\d+) Bytes\)`)

// storeSize reports the /nix/store APFS volume's own used bytes. df resolves the
// block device; diskutil reports the per-volume used space (df alone reports the
// shared container, hence diskutil). macOS-only — returns "0 B" off darwin.
func storeSize(ctx context.Context, r exec.Runner) string {
	dfRes, err := r.Run(ctx, "df", []string{"/nix/store"}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	lines := strings.Split(strings.TrimRight(string(dfRes.Stdout), "\n"), "\n")
	if len(lines) < 2 {
		return formatSize(0)
	}
	fields := strings.Fields(lines[1])
	if len(fields) == 0 {
		return formatSize(0)
	}
	device := fields[0]
	duRes, err := r.Run(ctx, "diskutil", []string{"info", device}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	m := diskutilBytesRE.FindSubmatch(duRes.Stdout)
	if m == nil {
		return formatSize(0)
	}
	bytes, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return formatSize(bytes)
}

// evalSymlinks is the testability seam for filepath.EvalSymlinks. Tests can
// override this to inject a controlled resolution without touching the
// filesystem (so scripted FakeRunner responses match the raw profile path).
var evalSymlinks = filepath.EvalSymlinks

// profileClosureSize returns the human-readable closure size, or "unknown".
func profileClosureSize(ctx context.Context, r exec.Runner, profile string) string {
	resolved := profile
	if rp, err := evalSymlinks(profile); err == nil {
		resolved = rp
	}
	res, err := r.Run(ctx, "nix", []string{"path-info", "-S", resolved}, exec.RunOptions{})
	if err != nil {
		return "unknown"
	}
	bytes := secondField(res.Stdout)
	if bytes <= 0 {
		return "unknown"
	}
	return formatSize(bytes)
}

func deadPathsSize(ctx context.Context, r exec.Runner) string {
	res, err := r.Run(ctx, "sudo", []string{"nix-store", "--gc", "--print-dead"}, exec.RunOptions{})
	if err != nil {
		return formatSize(0)
	}
	paths := nonEmptyLines(res.Stdout)
	if len(paths) == 0 {
		return formatSize(0)
	}
	return formatSize(sumPathInfoSizes(ctx, r, paths))
}

func runtimeRootsSummary(ctx context.Context, r exec.Runner) string {
	res, err := r.Run(ctx, "nix-store", []string{"--gc", "--print-roots"}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	var lsof, files []string
	for _, line := range nonEmptyLines(res.Stdout) {
		fields := strings.Fields(line)
		// Store path is awk field $3 of "<root> -> <storepath>".
		// ACCEPTED DEVIATION (NB1): bash `awk '{print $3}'` would emit ""
		// for a <3-field line and still classify it; we skip such lines.
		// Real `nix-store --gc --print-roots` output is always 3 fields, so
		// this never differs in practice; documented to avoid a silent gap.
		if len(fields) < 3 {
			continue
		}
		storePath := fields[2]
		if strings.Contains(line, "{lsof}") {
			lsof = append(lsof, storePath)
		} else {
			files = append(files, storePath)
		}
	}
	lsofOnly := subtractSorted(uniqueSorted(lsof), uniqueSorted(files))
	if len(lsofOnly) == 0 {
		return ""
	}
	size := formatSize(sumPathInfoSizes(ctx, r, lsofOnly))
	word := "paths"
	if len(lsofOnly) == 1 {
		word = "path"
	}
	return fmt.Sprintf("%d store %s held only by running processes (up to %s reclaimable)\n"+
		"  Tip: Restarting applications and re-running may free additional space", len(lsofOnly), word, size)
}

// --- small helpers ---

// secondField parses the 2nd whitespace field of the FIRST line of b as an int64.
func secondField(b []byte) int64 {
	first := string(b)
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	return secondFieldFromLine(first)
}

func nonEmptyLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sumPathInfoSizes runs `nix path-info -S <paths...>` once and sums column 2.
func sumPathInfoSizes(ctx context.Context, r exec.Runner, paths []string) int64 {
	res, err := r.Run(ctx, "nix", append([]string{"path-info", "-S"}, paths...), exec.RunOptions{})
	if err != nil {
		return 0
	}
	var sum int64
	for _, line := range nonEmptyLines(res.Stdout) {
		sum += secondFieldFromLine(line)
	}
	return sum
}

func secondFieldFromLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[1], 10, 64)
	return n
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// subtractSorted returns elements of a not present in b.
func subtractSorted(a, b []string) []string {
	bset := map[string]struct{}{}
	for _, s := range b {
		bset[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := bset[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
